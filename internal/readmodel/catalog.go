// Package readmodel atiende las consultas de lectura del catálogo. Cada
// operación comprueba antes el permiso sobre la biblioteca o el elemento.
package readmodel

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"medianode/internal/authorization"
	"medianode/internal/core"
	"medianode/internal/database"
	"medianode/internal/database/sqlc"
	"medianode/internal/media"
)

// Tamaños de página de los listados.
const (
	DefaultPageSize = 50
	MaxPageSize     = 200
)

// Catalog lee el catálogo con las comprobaciones de autorización.
type Catalog struct {
	db    *database.DB
	authz *authorization.Authorizer
}

// New crea el lector.
func New(db *database.DB, authz *authorization.Authorizer) *Catalog {
	return &Catalog{db: db, authz: authz}
}

// ItemSummary es un elemento en un listado.
type ItemSummary struct {
	ID             string
	Type           string
	Title          string
	Year           *int64
	MetadataStatus string
	HasPoster      bool
}

// Page es una página de un listado. NextCursor está vacío en la última.
type Page struct {
	Items      []ItemSummary
	NextCursor string
}

// ItemDetail es un elemento con sus datos relacionados.
type ItemDetail struct {
	sqlc.MediaItem
	Genres      []string
	People      []sqlc.ListItemPeopleRow
	ExternalIDs []sqlc.ListItemExternalIDsRow
	Images      []media.ImageType // Solo las ya descargadas.
}

// Child es una temporada o un episodio.
type Child struct {
	sqlc.ListChildrenRow
}

// cursor es la posición tras el último elemento de una página.
type cursor struct {
	Sort string `json:"s"`
	ID   string `json:"i"`
}

func encodeCursor(c cursor) string {
	data, _ := json.Marshal(c) // Dos cadenas siempre se serializan.
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeCursor(s string) (cursor, error) {
	var c cursor
	if s == "" {
		return c, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || json.Unmarshal(data, &c) != nil || c.ID == "" {
		return c, core.ErrValidation("El cursor no es válido")
	}
	return c, nil
}

// PageSize normaliza el tamaño de página pedido.
func PageSize(n int) int {
	if n <= 0 {
		return DefaultPageSize
	}
	return min(n, MaxPageSize)
}

// ageFilter traduce el actor a los parámetros de las consultas: un usuario
// restringido solo ve edades conocidas hasta su límite (§15.1).
func ageFilter(actor authorization.Actor) (restricted, maxAge int64) {
	if actor.Role != media.RoleRestricted {
		return 0, 0
	}
	if actor.MaxAgeRating != nil {
		maxAge = *actor.MaxAgeRating
	}
	return 1, maxAge
}

// ListParams filtra y pagina un listado. Status vacío no filtra.
type ListParams struct {
	After  string
	Size   int
	Status media.MetadataStatus
}

// ListLibraryItems devuelve los elementos principales de una biblioteca por
// título ordenable. El cursor no degrada con bibliotecas grandes.
func (c *Catalog) ListLibraryItems(ctx context.Context, actor authorization.Actor, libraryID string, p ListParams) (*Page, error) {
	if err := c.authz.CanLibrary(ctx, actor, libraryID, media.PermissionView); err != nil {
		return nil, err
	}
	cur, err := decodeCursor(p.After)
	if err != nil {
		return nil, err
	}
	switch p.Status {
	case "", media.MetadataPending, media.MetadataDone, media.MetadataUnmatched, media.MetadataFailed:
	default:
		return nil, core.ErrValidation("Estado de metadatos desconocido")
	}
	size := PageSize(p.Size)
	restricted, maxAge := ageFilter(actor)
	// Se pide uno más para saber si hay otra página.
	rows, err := c.db.R.ListLibraryItems(ctx, sqlc.ListLibraryItemsParams{
		LibraryID: libraryID, AfterSort: &cur.Sort, AfterID: &cur.ID,
		Restricted: restricted, MaxAge: &maxAge, Status: string(p.Status), PageSize: int64(size + 1),
	})
	if err != nil {
		return nil, fmt.Errorf("listar elementos: %w", err)
	}
	page := &Page{Items: make([]ItemSummary, 0, min(len(rows), size))}
	for i, r := range rows {
		if i == size {
			last := rows[size-1]
			page.NextCursor = encodeCursor(cursor{Sort: deref(last.SortTitle), ID: last.ID})
			break
		}
		page.Items = append(page.Items, ItemSummary{
			ID: r.ID, Type: r.Type, Title: r.Title, Year: r.Year,
			MetadataStatus: r.MetadataStatus, HasPoster: r.HasPoster != 0,
		})
	}
	return page, nil
}

// Item devuelve el detalle de un elemento visible.
func (c *Catalog) Item(ctx context.Context, actor authorization.Actor, id string) (*ItemDetail, error) {
	if err := c.authz.CanItem(ctx, actor, id); err != nil {
		return nil, err
	}
	item, err := c.db.R.GetMediaItem(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrNotFound()
	}
	if err != nil {
		return nil, fmt.Errorf("leer elemento: %w", err)
	}
	out := &ItemDetail{MediaItem: item}
	if out.Genres, err = c.db.R.ListItemGenres(ctx, id); err != nil {
		return nil, fmt.Errorf("leer géneros: %w", err)
	}
	if out.People, err = c.db.R.ListItemPeople(ctx, id); err != nil {
		return nil, fmt.Errorf("leer personas: %w", err)
	}
	if out.ExternalIDs, err = c.db.R.ListItemExternalIDs(ctx, id); err != nil {
		return nil, fmt.Errorf("leer identificadores: %w", err)
	}
	images, err := c.db.R.ListItemImages(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("leer imágenes: %w", err)
	}
	for _, im := range images {
		if im.LocalPath != nil {
			out.Images = append(out.Images, media.ImageType(im.Type))
		}
	}
	return out, nil
}

// Children devuelve las temporadas de una serie o los episodios de una
// temporada, sin los que la clasificación oculta al actor.
func (c *Catalog) Children(ctx context.Context, actor authorization.Actor, id string) ([]Child, error) {
	if err := c.authz.CanItem(ctx, actor, id); err != nil {
		return nil, err
	}
	rows, err := c.db.R.ListChildren(ctx, &id)
	if err != nil {
		return nil, fmt.Errorf("listar hijos: %w", err)
	}
	out := make([]Child, 0, len(rows))
	for _, r := range rows {
		if authorization.AgeAllowed(actor, r.EffectiveMinAge) {
			out = append(out, Child{r})
		}
	}
	return out, nil
}

// Files devuelve los archivos del elemento. La ruta relativa solo llega a los
// administradores: describe la estructura de carpetas del servidor.
func (c *Catalog) Files(ctx context.Context, actor authorization.Actor, id string) ([]sqlc.ListItemFilesRow, error) {
	if err := c.authz.CanItem(ctx, actor, id); err != nil {
		return nil, err
	}
	rows, err := c.db.R.ListItemFiles(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("listar archivos: %w", err)
	}
	if !authorization.IsAdmin(actor) {
		for i := range rows {
			rows[i].RelativePath = ""
		}
	}
	return rows, nil
}

// Streams devuelve las pistas de todos los archivos del elemento.
func (c *Catalog) Streams(ctx context.Context, actor authorization.Actor, id string) ([]sqlc.MediaStream, error) {
	if err := c.authz.CanItem(ctx, actor, id); err != nil {
		return nil, err
	}
	rows, err := c.db.R.ListItemStreams(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("listar pistas: %w", err)
	}
	return rows, nil
}

// Subtitles devuelve los subtítulos externos de los archivos del elemento. Los
// internos son pistas y llegan en Streams.
func (c *Catalog) Subtitles(ctx context.Context, actor authorization.Actor, id string) ([]sqlc.Subtitle, error) {
	if err := c.authz.CanItem(ctx, actor, id); err != nil {
		return nil, err
	}
	rows, err := c.db.R.ListItemSubtitles(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("listar subtítulos: %w", err)
	}
	if !authorization.IsAdmin(actor) {
		for i := range rows {
			rows[i].RelativePath = ""
		}
	}
	return rows, nil
}

// Image devuelve la ruta, relativa al directorio de imágenes, de una imagen ya
// descargada. Nunca descarga: eso lo hace el Enricher.
func (c *Catalog) Image(ctx context.Context, actor authorization.Actor, id string, t media.ImageType) (string, error) {
	if !t.Valid() {
		return "", core.ErrNotFound()
	}
	if err := c.authz.CanItem(ctx, actor, id); err != nil {
		return "", err
	}
	path, err := c.db.R.GetItemImage(ctx, sqlc.GetItemImageParams{MediaItemID: id, Type: string(t)})
	if errors.Is(err, sql.ErrNoRows) {
		return "", core.ErrNotFound()
	}
	if err != nil {
		return "", fmt.Errorf("leer imagen: %w", err)
	}
	return deref(path), nil
}

// Counts son los contadores del estado del servidor.
type Counts struct {
	Libraries       int64
	ScansRunning    int64
	PendingMetadata int64
}

// Counts devuelve los contadores. Solo para administradores: lo comprueba
// quien llama.
func (c *Catalog) Counts(ctx context.Context) (Counts, error) {
	var out Counts
	var err error
	if out.Libraries, err = c.db.R.CountLibraries(ctx); err != nil {
		return out, fmt.Errorf("contar bibliotecas: %w", err)
	}
	if out.ScansRunning, err = c.db.R.CountRunningScans(ctx); err != nil {
		return out, fmt.Errorf("contar escaneos: %w", err)
	}
	if out.PendingMetadata, err = c.db.R.CountPendingMetadata(ctx); err != nil {
		return out, fmt.Errorf("contar pendientes: %w", err)
	}
	return out, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
