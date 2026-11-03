// Package scanner indexa las bibliotecas: recorre sus rutas, analiza con
// FFprobe lo nuevo o modificado, detecta ausencias y movimientos e identifica
// cada archivo por su nombre, siempre sin red.
package scanner

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"medianode/internal/core"
	"medianode/internal/database"
	"medianode/internal/database/sqlc"
	"medianode/internal/media"
	"medianode/internal/probe"
)

// Prober analiza un archivo. probe.Runner lo implementa.
type Prober interface {
	Run(ctx context.Context, path string) (*probe.Result, error)
}

// Options ajusta el escáner.
type Options struct {
	Workers   int           // análisis simultáneos
	Retention time.Duration // tiempo que se conserva un archivo ausente
	MaxDepth  int           // niveles de carpeta bajo cada raíz
	BatchSize int           // archivos por transacción
}

// Event notifica un cambio a quien escuche. El bus de eventos llega en la
// Fase 6; hasta entonces solo se registra.
type Event struct {
	Name      string
	LibraryID string
	JobID     string
}

// Nombres de evento de §17.
const (
	EventScanStarted   = "library.scan.started"
	EventScanCompleted = "library.scan.completed"
	EventLibraryUpdate = "library.updated"
)

// Scanner coordina los escaneos. Hay como mucho uno por biblioteca.
type Scanner struct {
	db      *database.DB
	log     zerolog.Logger
	prober  Prober
	opts    Options
	now     func() time.Time
	OnEvent func(Event)

	base   context.Context
	stop   context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.Mutex
	runs   map[string]*run
	locked map[string]int
}

type run struct {
	job    sqlc.ScanJob
	cancel context.CancelFunc
	done   chan struct{}
}

// New crea el escáner.
func New(db *database.DB, prober Prober, opts Options, log zerolog.Logger) *Scanner {
	if opts.Workers < 1 {
		opts.Workers = 1
	}
	if opts.MaxDepth < 1 {
		opts.MaxDepth = 32
	}
	if opts.BatchSize < 1 {
		opts.BatchSize = 200
	}
	base, stop := context.WithCancel(context.Background())
	return &Scanner{
		db: db, log: log.With().Str("module", "scanner").Logger(), prober: prober, opts: opts,
		now: time.Now, base: base, stop: stop,
		runs: map[string]*run{}, locked: map[string]int{},
	}
}

// Recover marca como interrumpidos los escaneos que quedaron en curso tras una
// caída. Debe llamarse al arrancar, antes de iniciar ninguno.
func (s *Scanner) Recover(ctx context.Context) error {
	n, err := s.db.Q.InterruptRunningScanJobs(ctx, new(s.timestamp()))
	if err != nil {
		return fmt.Errorf("recuperar escaneos: %w", err)
	}
	if n > 0 {
		s.log.Warn().Int64("escaneos", n).Msg("escaneos interrumpidos por un apagado anterior")
	}
	return nil
}

// Start inicia un escaneo en segundo plano. Si ya hay uno en curso para la
// biblioteca, devuelve ese.
func (s *Scanner) Start(libraryID string, trigger media.ScanTrigger) (sqlc.ScanJob, error) {
	r, err := s.start(libraryID, trigger)
	if err != nil {
		return sqlc.ScanJob{}, err
	}
	return r.job, nil
}

// Scan escanea y espera a que termine. Si ctx se cancela, también lo hace el
// escaneo.
func (s *Scanner) Scan(ctx context.Context, libraryID string, trigger media.ScanTrigger) (sqlc.ScanJob, error) {
	r, err := s.start(libraryID, trigger)
	if err != nil {
		return sqlc.ScanJob{}, err
	}
	select {
	case <-r.done:
	case <-ctx.Done():
		r.cancel()
		<-r.done
	}
	job, err := s.db.R.GetScanJob(context.WithoutCancel(ctx), r.job.ID)
	if err != nil {
		return sqlc.ScanJob{}, fmt.Errorf("leer escaneo: %w", err)
	}
	return job, nil
}

func (s *Scanner) start(libraryID string, trigger media.ScanTrigger) (*run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.base.Err() != nil || s.locked[libraryID] > 0 {
		return nil, core.ErrLibraryBusy()
	}
	if r, ok := s.runs[libraryID]; ok {
		return r, nil
	}
	if _, err := s.db.R.GetLibrary(s.base, libraryID); errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrNotFound()
	} else if err != nil {
		return nil, fmt.Errorf("leer biblioteca: %w", err)
	}

	id, err := database.NewID()
	if err != nil {
		return nil, err
	}
	err = s.db.Q.CreateScanJob(s.base, sqlc.CreateScanJobParams{
		ID: id, LibraryID: libraryID, Trigger: string(trigger), StartedAt: new(s.timestamp()),
	})
	if err != nil {
		// El índice único impide un segundo escaneo en curso aunque otro proceso
		// lo hubiera creado.
		if job, getErr := s.db.R.GetRunningScanJob(s.base, libraryID); getErr == nil {
			return &run{job: job, cancel: func() {}, done: closed()}, nil
		}
		return nil, fmt.Errorf("crear escaneo: %w", err)
	}
	job, err := s.db.Q.GetScanJob(s.base, id)
	if err != nil {
		return nil, fmt.Errorf("leer escaneo: %w", err)
	}

	ctx, cancel := context.WithCancel(s.base)
	r := &run{job: job, cancel: cancel, done: make(chan struct{})}
	s.runs[libraryID] = r
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(r.done)
		defer cancel()
		s.execute(ctx, job)
		s.mu.Lock()
		delete(s.runs, libraryID)
		s.mu.Unlock()
	}()
	return r, nil
}

func closed() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

// Exclusive detiene el escaneo en curso de la biblioteca, impide que empiece
// otro y ejecuta fn. Lo usan eliminar la biblioteca o quitar una ruta, para
// que ningún escaneo escriba sobre filas que desaparecen.
func (s *Scanner) Exclusive(libraryID string, fn func() error) error {
	s.mu.Lock()
	s.locked[libraryID]++
	r := s.runs[libraryID]
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.locked[libraryID]--; s.locked[libraryID] == 0 {
			delete(s.locked, libraryID)
		}
		s.mu.Unlock()
	}()
	if r != nil {
		r.cancel()
		<-r.done
	}
	return fn()
}

// Running devuelve el escaneo en curso de la biblioteca, si lo hay.
func (s *Scanner) Running(libraryID string) (sqlc.ScanJob, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[libraryID]
	if !ok {
		return sqlc.ScanJob{}, false
	}
	return r.job, true
}

// Close cancela los escaneos en curso y espera a que terminen. Quedan como
// interrumpidos.
func (s *Scanner) Close() {
	s.mu.Lock()
	s.stop()
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Scanner) execute(ctx context.Context, job sqlc.ScanJob) {
	log := s.log.With().Str("library_id", job.LibraryID).Str("job_id", job.ID).Logger()
	log.Info().Str("trigger", job.Trigger).Msg("escaneo iniciado")
	s.emit(Event{Name: EventScanStarted, LibraryID: job.LibraryID, JobID: job.ID})
	started := s.now()

	st := &stats{}
	err := s.scanLibrary(ctx, job, st, log)

	status, message := media.ScanCompleted, (*string)(nil)
	switch {
	case ctx.Err() != nil:
		status = media.ScanInterrupted
	case errors.Is(err, probe.ErrUnavailable):
		status, message = media.ScanFailed, new("FFprobe no está disponible")
	case err != nil:
		status, message = media.ScanFailed, new("El escaneo falló; consulta el log del servidor")
	}
	if err != nil && ctx.Err() == nil {
		log.Error().Err(err).Msg("escaneo fallido")
	}

	// El escaneo puede haberse cancelado, pero su cierre debe quedar escrito.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	ferr := s.db.Q.FinishScanJob(finishCtx, sqlc.FinishScanJobParams{
		ID: job.ID, Status: string(status), FinishedAt: new(s.timestamp()),
		FilesDiscovered: st.discovered, FilesProcessed: st.processed, ErrorMessage: message,
	})
	if ferr != nil {
		log.Error().Err(ferr).Msg("no se pudo cerrar el escaneo")
	}
	log.Info().Str("status", string(status)).Int64("descubiertos", st.discovered).
		Int64("procesados", st.processed).Dur("duracion", s.now().Sub(started)).Msg("escaneo terminado")
	s.emit(Event{Name: EventScanCompleted, LibraryID: job.LibraryID, JobID: job.ID})
	if st.changed {
		s.emit(Event{Name: EventLibraryUpdate, LibraryID: job.LibraryID, JobID: job.ID})
	}
}

func (s *Scanner) emit(e Event) {
	if s.OnEvent != nil {
		s.OnEvent(e)
	}
}

// Purge elimina los archivos ausentes desde hace más que la retención y,
// después, los elementos que se quedan sin archivos y las temporadas y series
// vacías. Solo entonces se pierde el progreso de esos elementos (§7.3).
func (s *Scanner) Purge(ctx context.Context) (files, items int64, err error) {
	cutoff := database.FormatTime(s.now().Add(-s.opts.Retention))
	err = s.db.InTx(ctx, func(q *sqlc.Queries) error {
		n, err := q.DeleteExpiredMissingFiles(ctx, &cutoff)
		if err != nil {
			return fmt.Errorf("purgar archivos: %w", err)
		}
		files = n
		items, err = DeleteOrphansTx(ctx, q)
		return err
	})
	return files, items, err
}

// DeleteOrphansTx elimina los elementos sin archivos y los padres vacíos.
func DeleteOrphansTx(ctx context.Context, q *sqlc.Queries) (int64, error) {
	total, err := q.DeleteOrphanLeafItems(ctx)
	if err != nil {
		return 0, fmt.Errorf("purgar elementos: %w", err)
	}
	for _, t := range []media.ItemType{media.ItemSeason, media.ItemSeries} {
		n, err := q.DeleteEmptyParents(ctx, string(t))
		if err != nil {
			return 0, fmt.Errorf("purgar %s: %w", t, err)
		}
		total += n
	}
	return total, nil
}

func (s *Scanner) timestamp() string {
	return database.FormatTime(s.now())
}
