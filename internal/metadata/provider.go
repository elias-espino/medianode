// Package metadata enriquece el catálogo con un proveedor externo (§8). La
// indexación local nunca depende de él: un elemento sin metadatos se muestra
// con los datos del archivo.
package metadata

import (
	"context"
	"errors"

	"medianode/internal/media"
)

// Errores que todo proveedor debe distinguir, porque deciden qué hace la cola.
var (
	// ErrUnavailable es un fallo pasajero: sin red, error 5xx o límite de
	// peticiones. El elemento sigue pendiente y la cola espera.
	ErrUnavailable = errors.New("proveedor de metadatos no disponible")
	// ErrUnauthorized indica que la credencial del proveedor no vale. Es un
	// problema de configuración: la cola espera sin marcar nada como fallido.
	ErrUnauthorized = errors.New("credencial del proveedor rechazada")
	// ErrNotFound indica que el identificador no existe en el proveedor.
	ErrNotFound = errors.New("el proveedor no conoce ese identificador")
)

// Query es una búsqueda de coincidencias.
type Query struct {
	Kind  media.ItemType // ItemMovie o ItemSeries.
	Title string
	Year  *int64
}

// Match es un resultado de búsqueda.
type Match struct {
	ExternalID    string
	Title         string
	OriginalTitle string
	Year          *int64
	Overview      string
	PosterURL     string
}

// Roles de una persona en un elemento.
const (
	RoleActor    = "actor"
	RoleDirector = "director"
	RoleWriter   = "writer"
	RoleCreator  = "creator"
)

// Person es una persona del reparto o del equipo.
type Person struct {
	ExternalID string
	Name       string
	Role       string
	Character  string
	Order      int64
}

// Details son los metadatos de una película o una serie.
type Details struct {
	ExternalID       string
	Title            string
	OriginalTitle    string
	Overview         string
	OriginalLanguage string
	Year             *int64
	ReleaseDate      *string // YYYY-MM-DD.
	AgeRating        *string // Clasificación original con su país, para mostrarla.
	MinAge           *int64  // Clasificación normalizada de §15.1; nil si no se conoce.
	Genres           []string
	People           []Person
	ExternalIDs      map[string]string // Otros proveedores, como imdb.
	Images           map[media.ImageType]string
}

// Episode son los metadatos de un episodio.
type Episode struct {
	Number   int64
	Title    string
	Overview string
	AirDate  *string
	StillURL string
}

// Season son los metadatos de una temporada y sus episodios.
type Season struct {
	Number    int64
	Title     string
	Overview  string
	AirDate   *string
	PosterURL string
	Episodes  []Episode
}

// Provider es un proveedor de metadatos. Las implementaciones limitan su
// propio ritmo de peticiones y traducen sus errores a los de este paquete.
type Provider interface {
	Name() string
	Search(ctx context.Context, q Query) ([]Match, error)
	Details(ctx context.Context, externalID string, kind media.ItemType) (*Details, error)
	Season(ctx context.Context, seriesID string, number int64) (*Season, error)
}
