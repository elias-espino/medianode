package dto

// ItemSummary es un elemento en un listado.
type ItemSummary struct {
	ID             string  `json:"id"`
	Type           string  `json:"type"`
	Title          string  `json:"title"`
	Year           *int64  `json:"year"`
	MetadataStatus string  `json:"metadata_status"`
	PosterURL      *string `json:"poster_url"`
}

// ItemPage es una página de un listado. next_cursor es null en la última.
type ItemPage struct {
	Items      []ItemSummary `json:"items"`
	NextCursor *string       `json:"next_cursor"`
}

// Person es una persona del reparto o del equipo.
type Person struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Role      string  `json:"role"`
	Character *string `json:"character"`
}

// Item es el detalle de un elemento. images asocia cada tipo disponible con
// su URL.
type Item struct {
	ID               string            `json:"id"`
	LibraryID        string            `json:"library_id"`
	ParentID         *string           `json:"parent_id"`
	Type             string            `json:"type"`
	Title            string            `json:"title"`
	OriginalTitle    *string           `json:"original_title"`
	Year             *int64            `json:"year"`
	ReleaseDate      *string           `json:"release_date"`
	Overview         *string           `json:"overview"`
	AgeRating        *string           `json:"age_rating"`
	MinAge           *int64            `json:"min_age"`
	OriginalLanguage *string           `json:"original_language"`
	DurationMS       *int64            `json:"duration_ms"`
	SeasonNumber     *int64            `json:"season_number"`
	EpisodeNumber    *int64            `json:"episode_number"`
	MetadataStatus   string            `json:"metadata_status"`
	MetadataLocked   bool              `json:"metadata_locked"`
	Genres           []string          `json:"genres"`
	People           []Person          `json:"people"`
	ExternalIDs      map[string]string `json:"external_ids"`
	Images           map[string]string `json:"images"`
	CreatedAt        string            `json:"created_at"`
	UpdatedAt        string            `json:"updated_at"`
}

// Child es una temporada o un episodio.
type Child struct {
	ID            string  `json:"id"`
	Type          string  `json:"type"`
	Title         string  `json:"title"`
	Year          *int64  `json:"year"`
	SeasonNumber  *int64  `json:"season_number"`
	EpisodeNumber *int64  `json:"episode_number"`
	Overview      *string `json:"overview"`
	ReleaseDate   *string `json:"release_date"`
	DurationMS    *int64  `json:"duration_ms"`
	ImageURL      *string `json:"image_url"`
}

// MediaFile es una versión de un elemento. relative_path solo llega a los
// administradores.
type MediaFile struct {
	ID           string  `json:"id"`
	RelativePath *string `json:"relative_path,omitempty"`
	Container    *string `json:"container"`
	FileSize     *int64  `json:"file_size"`
	DurationMS   *int64  `json:"duration_ms"`
	Bitrate      *int64  `json:"bitrate"`
	ProbeStatus  string  `json:"probe_status"`
	MissingSince *string `json:"missing_since"`
	PartOrder    int64   `json:"part_order"`
}

// Stream es una pista de un archivo.
type Stream struct {
	ID             string  `json:"id"`
	FileID         string  `json:"file_id"`
	Index          int64   `json:"index"`
	Type           string  `json:"type"`
	Codec          *string `json:"codec"`
	Profile        *string `json:"profile"`
	Level          *string `json:"level"`
	Language       *string `json:"language"`
	Channels       *int64  `json:"channels"`
	Width          *int64  `json:"width"`
	Height         *int64  `json:"height"`
	FrameRate      *string `json:"frame_rate"`
	Bitrate        *int64  `json:"bitrate"`
	BitDepth       *int64  `json:"bit_depth"`
	ColorTransfer  *string `json:"color_transfer"`
	ColorPrimaries *string `json:"color_primaries"`
	HDRFormat      *string `json:"hdr_format"`
	Default        bool    `json:"default"`
	Forced         bool    `json:"forced"`
	Title          *string `json:"title"`
}

// Subtitle es un subtítulo externo.
type Subtitle struct {
	ID           string  `json:"id"`
	FileID       string  `json:"file_id"`
	RelativePath *string `json:"relative_path,omitempty"`
	Language     string  `json:"language"`
	Format       *string `json:"format"`
	Default      bool    `json:"default"`
	Forced       bool    `json:"forced"`
}

// SearchResult es un elemento encontrado. parent_title es la serie de un
// episodio.
type SearchResult struct {
	ID          string  `json:"id"`
	Type        string  `json:"type"`
	Title       string  `json:"title"`
	Year        *int64  `json:"year"`
	LibraryID   string  `json:"library_id"`
	ParentID    *string `json:"parent_id"`
	ParentTitle *string `json:"parent_title"`
}

// Match es una coincidencia del proveedor.
type Match struct {
	Provider      string  `json:"provider"`
	ExternalID    string  `json:"external_id"`
	Title         string  `json:"title"`
	OriginalTitle string  `json:"original_title"`
	Year          *int64  `json:"year"`
	Overview      string  `json:"overview"`
	PosterURL     *string `json:"poster_url"`
}

// IdentifyRequest elige una coincidencia.
type IdentifyRequest struct {
	Provider   string `json:"provider"`
	ExternalID string `json:"external_id"`
}

// EditMetadataRequest cambia los campos presentes. Si no incluye locked, editar
// bloquea el elemento.
type EditMetadataRequest struct {
	Title         *string          `json:"title"`
	OriginalTitle Optional[string] `json:"original_title"`
	Overview      Optional[string] `json:"overview"`
	Year          Optional[int64]  `json:"year"`
	ReleaseDate   Optional[string] `json:"release_date"`
	AgeRating     Optional[string] `json:"age_rating"`
	MinAge        Optional[int64]  `json:"min_age"`
	Locked        *bool            `json:"locked"`
}
