package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"medianode/internal/authorization"
	"medianode/internal/core"
	"medianode/internal/database"
	"medianode/internal/database/sqlc"
	"medianode/internal/media"
)

// identifyTimeout limita la identificación manual, que espera al proveedor
// dentro de la petición HTTP.
const identifyTimeout = 45 * time.Second

const (
	maxTitleLength    = 500
	maxOverviewLength = 10000
	maxRatingLength   = 50
	maxMinAge         = 21
)

// Field es un campo de una edición parcial: Set distingue «ausente» de «null».
type Field[T any] struct {
	Set   bool
	Value *T
}

// EditParams son los cambios manuales de §8.3. Editar bloquea el elemento,
// salvo que Locked diga otra cosa, para que el Enricher no deshaga el cambio.
type EditParams struct {
	Title         *string
	OriginalTitle Field[string]
	Overview      Field[string]
	Year          Field[int64]
	ReleaseDate   Field[string]
	AgeRating     Field[string]
	MinAge        Field[int64]
	Locked        *bool
}

// Service atiende la identificación y la edición manual. Todas las
// operaciones exigen el permiso manage sobre la biblioteca del elemento.
type Service struct {
	db       *database.DB
	authz    *authorization.Authorizer
	enricher *Enricher
}

// NewService crea el servicio.
func NewService(db *database.DB, authz *authorization.Authorizer, enricher *Enricher) *Service {
	return &Service{db: db, authz: authz, enricher: enricher}
}

// ProviderName devuelve el proveedor configurado, o vacío.
func (s *Service) ProviderName() string {
	if p := s.enricher.Provider(); p != nil {
		return p.Name()
	}
	return ""
}

// manageable comprueba que el actor ve el elemento y gestiona su biblioteca.
func (s *Service) manageable(ctx context.Context, actor authorization.Actor, id string) (*sqlc.MediaItem, error) {
	if err := s.authz.CanItem(ctx, actor, id); err != nil {
		return nil, err
	}
	item, err := s.db.R.GetMediaItem(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrNotFound()
	}
	if err != nil {
		return nil, fmt.Errorf("leer elemento: %w", err)
	}
	if err := s.authz.CanLibrary(ctx, actor, item.LibraryID, media.PermissionManage); err != nil {
		return nil, err
	}
	return &item, nil
}

// identifiable exige un elemento principal: las temporadas y episodios se
// identifican con su serie.
func (s *Service) identifiable(ctx context.Context, actor authorization.Actor, id string) (*sqlc.MediaItem, Provider, error) {
	item, err := s.manageable(ctx, actor, id)
	if err != nil {
		return nil, nil, err
	}
	if item.ParentID != nil {
		return nil, nil, core.ErrValidation("Las temporadas y los episodios se identifican con su serie")
	}
	p := s.enricher.Provider()
	if p == nil {
		return nil, nil, core.ErrMetadataUnavailable("No hay ningún proveedor de metadatos configurado")
	}
	return item, p, nil
}

// Matches busca coincidencias para un elemento. Sin query usa su título y su
// año.
func (s *Service) Matches(ctx context.Context, actor authorization.Actor, id, query string, year *int64) ([]Match, error) {
	item, p, err := s.identifiable(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	q := Query{Kind: media.ItemType(item.Type), Title: strings.TrimSpace(query), Year: year}
	if q.Title == "" {
		q.Title = item.Title
		if q.Year == nil {
			q.Year = item.Year
		}
	}
	if utf8.RuneCountInString(q.Title) > maxTitleLength {
		return nil, core.ErrValidation("La búsqueda es demasiado larga")
	}
	matches, err := p.Search(ctx, q)
	if err != nil {
		return nil, providerError(err)
	}
	return matches, nil
}

// Identify asocia el elemento al identificador elegido y aplica sus metadatos
// en el momento. Las imágenes se descargan después, desde la cola.
func (s *Service) Identify(ctx context.Context, actor authorization.Actor, id, provider, externalID string) error {
	item, p, err := s.identifiable(ctx, actor, id)
	if err != nil {
		return err
	}
	if provider != p.Name() {
		return core.ErrValidation("Proveedor desconocido: el configurado es " + p.Name())
	}
	if strings.TrimSpace(externalID) == "" {
		return core.ErrValidation("Falta el identificador externo")
	}
	if item.MetadataLocked != 0 {
		return core.ErrMetadataLocked()
	}
	ctx, cancel := context.WithTimeout(ctx, identifyTimeout)
	defer cancel()
	applied, err := s.enricher.fetchAndApply(ctx, id, media.ItemType(item.Type), strings.TrimSpace(externalID))
	if errors.Is(err, ErrNotFound) {
		return core.ErrValidation("El proveedor no tiene ese identificador")
	}
	if err != nil {
		return providerError(err)
	}
	if !applied {
		return core.ErrMetadataLocked() // Se bloqueó durante la petición.
	}
	if s.enricher.OnUpdated != nil {
		s.enricher.OnUpdated(item.LibraryID)
	}
	return nil
}

// Refresh devuelve el elemento a la cola. Si ya está identificado se piden
// los detalles por su identificador, sin volver a buscar.
func (s *Service) Refresh(ctx context.Context, actor authorization.Actor, id string) error {
	item, _, err := s.identifiable(ctx, actor, id)
	if err != nil {
		return err
	}
	if item.MetadataLocked != 0 {
		return core.ErrMetadataLocked()
	}
	err = s.db.Q.SetMetadataStatus(ctx, sqlc.SetMetadataStatusParams{
		ID: id, MetadataStatus: string(media.MetadataPending), UpdatedAt: database.Now(),
	})
	if err != nil {
		return fmt.Errorf("guardar estado: %w", err)
	}
	s.enricher.Notify()
	return nil
}

// Edit cambia los metadatos a mano. Vale para cualquier elemento, también
// temporadas y episodios, y no necesita proveedor.
func (s *Service) Edit(ctx context.Context, actor authorization.Actor, id string, p EditParams) error {
	if _, err := s.manageable(ctx, actor, id); err != nil {
		return err
	}
	if err := p.validate(); err != nil {
		return err
	}
	return s.db.InTx(ctx, func(q *sqlc.Queries) error {
		item, err := q.GetMediaItem(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return core.ErrNotFound()
		}
		if err != nil {
			return fmt.Errorf("leer elemento: %w", err)
		}
		edited := false
		if p.Title != nil {
			item.Title, edited = strings.TrimSpace(*p.Title), true
		}
		for _, f := range []struct {
			field Field[string]
			dst   **string
		}{
			{p.OriginalTitle, &item.OriginalTitle},
			{p.Overview, &item.Overview},
			{p.ReleaseDate, &item.ReleaseDate},
			{p.AgeRating, &item.AgeRating},
		} {
			if f.field.Set {
				*f.dst, edited = trimmed(f.field.Value), true
			}
		}
		if p.Year.Set {
			item.Year, edited = p.Year.Value, true
		}
		if p.MinAge.Set {
			item.MinAge, edited = p.MinAge.Value, true
		}
		locked := item.MetadataLocked
		switch {
		case p.Locked != nil:
			locked = boolInt(*p.Locked)
		case edited:
			locked = 1
		}
		sort := media.SortTitle(item.Title, s.enricher.lang)
		return q.EditItemMetadata(ctx, sqlc.EditItemMetadataParams{
			ID: id, Title: item.Title, OriginalTitle: item.OriginalTitle, SortTitle: &sort,
			Year: item.Year, ReleaseDate: item.ReleaseDate, Overview: item.Overview,
			AgeRating: item.AgeRating, MinAge: item.MinAge, MetadataLocked: locked, UpdatedAt: database.Now(),
		})
	})
}

func (p EditParams) validate() error {
	if p.Title != nil {
		t := strings.TrimSpace(*p.Title)
		if t == "" || utf8.RuneCountInString(t) > maxTitleLength {
			return core.ErrValidation("El título no es válido")
		}
	}
	if p.OriginalTitle.Value != nil && utf8.RuneCountInString(*p.OriginalTitle.Value) > maxTitleLength {
		return core.ErrValidation("El título original es demasiado largo")
	}
	if p.Overview.Value != nil && utf8.RuneCountInString(*p.Overview.Value) > maxOverviewLength {
		return core.ErrValidation("La sinopsis es demasiado larga")
	}
	if p.AgeRating.Value != nil && utf8.RuneCountInString(*p.AgeRating.Value) > maxRatingLength {
		return core.ErrValidation("La clasificación es demasiado larga")
	}
	if y := p.Year.Value; y != nil && (*y < 1800 || *y > 2200) {
		return core.ErrValidation("El año no es válido")
	}
	if a := p.MinAge.Value; a != nil && (*a < 0 || *a > maxMinAge) {
		return core.ErrValidation("La edad mínima debe estar entre 0 y 21")
	}
	if d := p.ReleaseDate.Value; d != nil && strings.TrimSpace(*d) != "" {
		if _, err := time.Parse(time.DateOnly, strings.TrimSpace(*d)); err != nil {
			return core.ErrValidation("La fecha debe tener el formato AAAA-MM-DD")
		}
	}
	return nil
}

// providerError traduce un fallo del proveedor para el cliente.
func providerError(err error) error {
	switch {
	case errors.Is(err, ErrUnauthorized):
		return core.ErrMetadataUnavailable("El proveedor de metadatos rechazó la clave de API")
	case errors.Is(err, ErrUnavailable), errors.Is(err, context.DeadlineExceeded):
		return core.ErrMetadataUnavailable("El proveedor de metadatos no responde")
	}
	return err
}

func trimmed(s *string) *string {
	if s == nil {
		return nil
	}
	t := strings.TrimSpace(*s)
	if t == "" {
		return nil
	}
	return &t
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
