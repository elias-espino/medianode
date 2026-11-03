// Package media declara las enumeraciones del dominio. La base guarda el texto.
package media

// Role es el rol de un usuario.
type Role string

// Roles de §15.1.
const (
	RoleOwner      Role = "owner"
	RoleAdmin      Role = "admin"
	RoleUser       Role = "user"
	RoleRestricted Role = "restricted"
)

// Valid indica si r es un rol conocido.
func (r Role) Valid() bool {
	switch r {
	case RoleOwner, RoleAdmin, RoleUser, RoleRestricted:
		return true
	}
	return false
}

// SubtitleMode es la preferencia de subtítulos de un usuario.
type SubtitleMode string

// Modos de subtítulos.
const (
	SubtitleDefault SubtitleMode = "default" // Pista marcada por defecto.
	SubtitleForced  SubtitleMode = "forced"  // Solo pistas forzadas.
	SubtitleAlways  SubtitleMode = "always"  // Siempre en el idioma preferido.
	SubtitleOff     SubtitleMode = "off"     // Nunca.
)

// Valid indica si m es un modo conocido.
func (m SubtitleMode) Valid() bool {
	switch m {
	case SubtitleDefault, SubtitleForced, SubtitleAlways, SubtitleOff:
		return true
	}
	return false
}

// Permission es un permiso sobre una biblioteca. Cada uno incluye a los
// anteriores: manage permite escanear y escanear permite ver.
type Permission string

// Permisos por biblioteca.
const (
	PermissionView   Permission = "view"
	PermissionScan   Permission = "scan"
	PermissionManage Permission = "manage"
)

// Valid indica si p es un permiso conocido.
func (p Permission) Valid() bool {
	switch p {
	case PermissionView, PermissionScan, PermissionManage:
		return true
	}
	return false
}

// LibraryType es el tipo de una biblioteca.
type LibraryType string

// Tipos de la primera versión (§7.1).
const (
	LibraryMovies LibraryType = "movies"
	LibraryTV     LibraryType = "tv"
)

// Valid indica si t es un tipo conocido.
func (t LibraryType) Valid() bool {
	return t == LibraryMovies || t == LibraryTV
}

// ItemType es el tipo de un elemento del catálogo.
type ItemType string

// Tipos de elemento.
const (
	ItemMovie   ItemType = "movie"
	ItemSeries  ItemType = "series"
	ItemSeason  ItemType = "season"
	ItemEpisode ItemType = "episode"
)

// MetadataStatus es el estado del enriquecimiento de un elemento.
type MetadataStatus string

// Estados de metadatos. pending forma la cola del Enricher; unmatched y failed
// solo se reintentan al forzar una actualización.
const (
	MetadataPending   MetadataStatus = "pending"
	MetadataDone      MetadataStatus = "done"
	MetadataUnmatched MetadataStatus = "unmatched" // El proveedor no encontró una coincidencia segura.
	MetadataFailed    MetadataStatus = "failed"    // El proveedor respondió con un error permanente.
)

// ImageType es el tipo de una imagen de §8.2.
type ImageType string

// Tipos de imagen.
const (
	ImagePoster    ImageType = "poster"
	ImageBackdrop  ImageType = "backdrop"
	ImageThumbnail ImageType = "thumbnail"
	ImageLogo      ImageType = "logo"
)

// Valid indica si t es un tipo conocido.
func (t ImageType) Valid() bool {
	switch t {
	case ImagePoster, ImageBackdrop, ImageThumbnail, ImageLogo:
		return true
	}
	return false
}

// ProbeStatus es el estado del análisis de un archivo.
type ProbeStatus string

// Estados de análisis.
const (
	ProbePending ProbeStatus = "pending"
	ProbeDone    ProbeStatus = "done"
	ProbeFailed  ProbeStatus = "failed"
)

// StreamType es el tipo de una pista.
type StreamType string

// Tipos de pista.
const (
	StreamVideo      StreamType = "video"
	StreamAudio      StreamType = "audio"
	StreamSubtitle   StreamType = "subtitle"
	StreamAttachment StreamType = "attachment"
	StreamData       StreamType = "data"
)

// ScanStatus es el estado de un escaneo.
type ScanStatus string

// Estados de escaneo.
const (
	ScanRunning     ScanStatus = "running"
	ScanCompleted   ScanStatus = "completed"
	ScanFailed      ScanStatus = "failed"
	ScanInterrupted ScanStatus = "interrupted"
)

// ScanTrigger indica qué inició un escaneo.
type ScanTrigger string

// Orígenes de un escaneo.
const (
	TriggerManual    ScanTrigger = "manual"
	TriggerScheduled ScanTrigger = "scheduled"
	TriggerWatcher   ScanTrigger = "watcher"
)
