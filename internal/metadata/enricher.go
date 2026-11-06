package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"medianode/internal/database"
	"medianode/internal/database/sqlc"
	"medianode/internal/images"
	"medianode/internal/media"
)

const (
	pendingBatch = 20
	sortBatch    = 500
)

// errNoMatch indica que el proveedor no devolvió una coincidencia segura.
var errNoMatch = errors.New("sin coincidencia segura")

// Options ajusta los tiempos de la cola. Los valores cero toman los de
// producción.
type Options struct {
	Poll          time.Duration // Revisión periódica de la cola sin avisos.
	MinBackoff    time.Duration // Primera espera tras un fallo pasajero.
	MaxBackoff    time.Duration
	EvictInterval time.Duration
	// MaxAge es la antigüedad a la que se vuelven a pedir los metadatos y las
	// imágenes. Las condiciones de TMDB prohíben guardar su contenido más de
	// seis meses; cinco dejan margen.
	MaxAge time.Duration
}

func (o *Options) defaults() {
	if o.Poll <= 0 {
		o.Poll = 5 * time.Minute
	}
	if o.MinBackoff <= 0 {
		o.MinBackoff = time.Minute
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = time.Hour
	}
	if o.EvictInterval <= 0 {
		o.EvictInterval = 24 * time.Hour
	}
	if o.MaxAge <= 0 {
		o.MaxAge = 150 * 24 * time.Hour
	}
}

// Status es el estado visible de la cola.
type Status struct {
	Provider    string // Vacío si no hay proveedor configurado.
	PausedUntil time.Time
	LastError   string
}

// Enricher es la cola de enriquecimiento de §7.2. La cola es la propia base:
// los elementos principales con metadata_status = pending y sin bloquear. Así
// sobrevive a los reinicios sin estado propio.
type Enricher struct {
	db       *database.DB
	provider Provider
	images   *images.Manager
	lang     string
	opts     Options
	log      zerolog.Logger

	// OnUpdated avisa de que cambió el catálogo de una biblioteca.
	OnUpdated func(libraryID string)

	wake   chan struct{}
	mu     sync.Mutex
	status Status
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewEnricher crea la cola. Con provider nil solo descarga imágenes pendientes
// y limpia: los elementos se quedan con los datos del archivo.
func NewEnricher(db *database.DB, provider Provider, imgs *images.Manager, lang string, opts Options, log zerolog.Logger) *Enricher {
	opts.defaults()
	e := &Enricher{db: db, provider: provider, images: imgs, lang: lang, opts: opts, log: log, wake: make(chan struct{}, 1)}
	if provider != nil {
		e.status.Provider = provider.Name()
	}
	return e
}

// Provider devuelve el proveedor configurado, o nil.
func (e *Enricher) Provider() Provider { return e.provider }

// Status devuelve el estado de la cola.
func (e *Enricher) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status
}

// Notify avisa de que puede haber elementos pendientes. Nunca bloquea.
func (e *Enricher) Notify() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// Name implementa lifecycle.Component.
func (e *Enricher) Name() string { return "metadata" }

// Start rellena los títulos ordenables que falten y lanza la cola.
func (e *Enricher) Start(ctx context.Context) error {
	if err := FillSortTitles(ctx, e.db, e.lang); err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	e.cancel = cancel
	e.wg.Go(func() { e.run(runCtx) })
	return nil
}

// Stop detiene la cola. Un elemento a medias sigue pendiente.
func (e *Enricher) Stop(ctx context.Context) error {
	if e.cancel != nil {
		e.cancel()
	}
	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Enricher) run(ctx context.Context) {
	backoff := e.opts.MinBackoff
	var lastEvict time.Time
	for {
		err := e.drain(ctx)
		if ctx.Err() != nil {
			return
		}
		wait := e.opts.Poll
		paused := pauses(err)
		if paused {
			wait = backoff
			backoff = min(backoff*2, e.opts.MaxBackoff)
			e.log.Warn().Err(err).Dur("espera", wait).Msg("proveedor de metadatos no disponible; la cola espera")
		} else {
			backoff = e.opts.MinBackoff
			if err != nil {
				e.log.Error().Err(err).Msg("error en la cola de metadatos")
			}
		}
		e.setStatus(paused, wait, err)

		if time.Since(lastEvict) >= e.opts.EvictInterval {
			lastEvict = time.Now()
			if n, err := e.requeueStale(ctx); err != nil {
				e.log.Error().Err(err).Msg("no se pudieron renovar los metadatos antiguos")
			} else if n > 0 {
				e.log.Info().Int("elementos", n).Msg("metadatos antiguos en cola para renovarse")
				if !paused {
					wait = 0 // Se procesan ya, en la siguiente vuelta.
				}
			}
			e.evict(ctx)
		}

		timer := time.NewTimer(wait)
		// En pausa, un aviso no adelanta el reintento: el proveedor sigue caído.
		wake := e.wake
		if paused {
			wake = nil
		}
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (e *Enricher) setStatus(paused bool, wait time.Duration, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.status.PausedUntil = time.Time{}
	e.status.LastError = ""
	if paused {
		e.status.PausedUntil = time.Now().Add(wait)
	}
	if err != nil {
		e.status.LastError = describeError(err)
	}
}

// describeError es el texto que ve el administrador: sin detalles internos.
func describeError(err error) string {
	switch {
	case errors.Is(err, ErrUnauthorized):
		return "El proveedor rechazó la clave de API"
	case errors.Is(err, ErrUnavailable), errors.Is(err, images.ErrTransient):
		return "El proveedor no responde"
	}
	return "Error interno en la cola de metadatos"
}

// pauses indica si el error obliga a esperar antes de seguir.
func pauses(err error) bool {
	return errors.Is(err, ErrUnavailable) || errors.Is(err, ErrUnauthorized) || errors.Is(err, images.ErrTransient)
}

// drain procesa la cola hasta vaciarla y descarga las imágenes pendientes.
// Cada elemento sale de pending salvo ante un fallo pasajero, que corta la
// tanda: así el bucle siempre avanza.
func (e *Enricher) drain(ctx context.Context) error {
	if e.provider != nil {
		if err := e.mergeDuplicates(ctx); err != nil {
			return err
		}
		for {
			rows, err := e.db.R.ListPendingMetadata(ctx, pendingBatch)
			if err != nil {
				return fmt.Errorf("leer cola: %w", err)
			}
			if len(rows) == 0 {
				break
			}
			for _, r := range rows {
				if err := e.enrich(ctx, r); err != nil {
					return err
				}
			}
		}
	}
	if e.images == nil {
		return nil
	}
	_, err := e.images.FetchPending(ctx)
	return err
}

func (e *Enricher) enrich(ctx context.Context, r sqlc.ListPendingMetadataRow) error {
	kind := media.ItemType(r.Type)
	externalID, err := e.db.R.GetExternalID(ctx, sqlc.GetExternalIDParams{MediaItemID: r.ID, Provider: e.provider.Name()})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("leer identificador: %w", err)
	}
	if externalID == "" {
		matches, err := e.provider.Search(ctx, Query{Kind: kind, Title: r.Title, Year: r.Year})
		if err != nil {
			return e.settle(ctx, r, err)
		}
		m := choose(Query{Kind: kind, Title: r.Title, Year: r.Year}, matches)
		if m == nil {
			return e.settle(ctx, r, errNoMatch)
		}
		externalID = m.ExternalID
	}
	applied, err := e.fetchAndApply(ctx, r.ID, kind, externalID)
	if err != nil {
		return e.settle(ctx, r, err)
	}
	if applied && e.OnUpdated != nil {
		e.OnUpdated(r.LibraryID)
	}
	return nil
}

// settle decide el estado tras un fallo. Los pasajeros se devuelven para que
// la cola espere; el resto deja el elemento fuera de la cola.
func (e *Enricher) settle(ctx context.Context, r sqlc.ListPendingMetadataRow, err error) error {
	if pauses(err) || ctx.Err() != nil {
		return err
	}
	status := media.MetadataFailed
	switch {
	case errors.Is(err, errNoMatch):
		status = media.MetadataUnmatched
	case errors.Is(err, ErrNotFound):
	default:
		e.log.Error().Err(err).Str("item_id", r.ID).Msg("metadatos no aplicados")
	}
	if serr := e.db.Q.SetMetadataStatus(ctx, sqlc.SetMetadataStatusParams{
		ID: r.ID, MetadataStatus: string(status), UpdatedAt: database.Now(),
	}); serr != nil {
		return fmt.Errorf("guardar estado: %w", serr)
	}
	return nil
}

// fetchAndApply pide los detalles, y las temporadas de una serie, y los guarda
// en una transacción. Las peticiones van fuera de ella (§6.3).
func (e *Enricher) fetchAndApply(ctx context.Context, itemID string, kind media.ItemType, externalID string) (bool, error) {
	if kind == media.ItemSeries {
		// Dos series con el mismo identificador son una: se unen y la que queda
		// vuelve a la cola, que repetirá esto con todas sus temporadas.
		var merged bool
		err := e.db.InTx(ctx, func(q *sqlc.Queries) error {
			var err error
			merged, err = mergeSameSeries(ctx, q, e.provider.Name(), externalID, itemID)
			return err
		})
		if err != nil || merged {
			return merged, err
		}
	}
	d, err := e.provider.Details(ctx, externalID, kind)
	if err != nil {
		return false, err
	}
	var seasons []*Season
	if kind == media.ItemSeries {
		local, err := e.db.R.ListChildNumbers(ctx, &itemID)
		if err != nil {
			return false, fmt.Errorf("leer temporadas: %w", err)
		}
		for _, ls := range local {
			if ls.SeasonNumber == nil {
				continue
			}
			s, err := e.provider.Season(ctx, d.ExternalID, *ls.SeasonNumber)
			if errors.Is(err, ErrNotFound) {
				continue // Temporada local que el proveedor no tiene.
			}
			if err != nil {
				return false, err
			}
			seasons = append(seasons, s)
		}
		if seasons, err = e.withAbsoluteSeasons(ctx, d.ExternalID, local, seasons); err != nil {
			return false, err
		}
	}
	var applied bool
	err = e.db.InTx(ctx, func(q *sqlc.Queries) error {
		applied, err = apply(ctx, q, e.provider.Name(), e.lang, itemID, d, seasons)
		return err
	})
	if applied {
		e.Notify() // Hay imágenes que descargar.
	}
	return applied, err
}

// maxAbsoluteSeasons acota las temporadas que se piden para numerar en absoluto.
const maxAbsoluteSeasons = 40

// withAbsoluteSeasons trae el resto de temporadas del proveedor cuando la serie
// local es una sola temporada con numeración corrida (anime: 1 a 95) y el
// proveedor la reparte en varias. applySeasons las usa para colocar cada
// episodio por su número absoluto.
func (e *Enricher) withAbsoluteSeasons(ctx context.Context, seriesID string, local []sqlc.ListChildNumbersRow, seasons []*Season) ([]*Season, error) {
	if len(local) != 1 || local[0].SeasonNumber == nil || *local[0].SeasonNumber != 1 {
		return seasons, nil
	}
	eps, err := e.db.R.ListChildNumbers(ctx, &local[0].ID)
	if err != nil {
		return nil, fmt.Errorf("leer episodios: %w", err)
	}
	var maxLocal, maxRemote int64
	for _, le := range eps {
		if le.EpisodeNumber != nil && *le.EpisodeNumber > maxLocal {
			maxLocal = *le.EpisodeNumber
		}
	}
	for _, s := range seasons {
		for _, ep := range s.Episodes {
			maxRemote = max(maxRemote, ep.Number)
		}
	}
	if maxLocal <= maxRemote {
		return seasons, nil
	}
	for n := int64(2); n <= maxAbsoluteSeasons; n++ {
		s, err := e.provider.Season(ctx, seriesID, n)
		if errors.Is(err, ErrNotFound) {
			break
		}
		if err != nil {
			return nil, err
		}
		seasons = append(seasons, s)
	}
	return seasons, nil
}

// requeueStale devuelve a la cola los elementos con metadatos más antiguos que
// MaxAge y olvida sus imágenes descargadas, que se vuelven a pedir. Los
// bloqueados no se tocan: sus datos los fijó el administrador.
func (e *Enricher) requeueStale(ctx context.Context) (int, error) {
	if e.provider == nil {
		return 0, nil
	}
	before := database.FormatTime(time.Now().Add(-e.opts.MaxAge))
	total := 0
	for {
		ids, err := e.db.R.ListStaleMetadata(ctx, sqlc.ListStaleMetadataParams{UpdatedAt: before, Limit: sortBatch})
		if err != nil || len(ids) == 0 {
			return total, err
		}
		err = e.db.InTx(ctx, func(q *sqlc.Queries) error {
			now := database.Now()
			for _, id := range ids {
				if err := q.SetMetadataStatus(ctx, sqlc.SetMetadataStatusParams{
					ID: id, MetadataStatus: string(media.MetadataPending), UpdatedAt: now,
				}); err != nil {
					return err
				}
				if err := q.ClearTreeImages(ctx, id); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return total, err
		}
		total += len(ids)
	}
}

func (e *Enricher) evict(ctx context.Context) {
	if e.images != nil {
		n, err := e.images.Evict(ctx)
		if err != nil {
			e.log.Error().Err(err).Msg("no se pudieron limpiar las imágenes")
		} else if n > 0 {
			e.log.Info().Int("archivos", n).Msg("imágenes huérfanas eliminadas")
		}
	}
	err := e.db.InTx(ctx, func(q *sqlc.Queries) error {
		if _, err := q.DeleteOrphanPeople(ctx); err != nil {
			return err
		}
		_, err := q.DeleteOrphanGenres(ctx)
		return err
	})
	if err != nil {
		e.log.Error().Err(err).Msg("no se pudieron limpiar personas y géneros")
	}
}

// FillSortTitles genera los títulos ordenables vacíos. La migración 6 los
// vacía para regenerarlos con media.SortTitle.
func FillSortTitles(ctx context.Context, db *database.DB, lang string) error {
	for {
		rows, err := db.R.ListMissingSortTitles(ctx, sortBatch)
		if err != nil {
			return fmt.Errorf("leer títulos: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		err = db.InTx(ctx, func(q *sqlc.Queries) error {
			for _, r := range rows {
				sort := media.SortTitle(r.Title, lang)
				if err := q.SetSortTitle(ctx, sqlc.SetSortTitleParams{ID: r.ID, SortTitle: &sort}); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("guardar títulos: %w", err)
		}
	}
}
