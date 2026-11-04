package dto

import "encoding/json"

// PlaybackSession es el estado de una sesión. Una terminada sigue
// consultable: su posición permite retomar tras SESSION_EXPIRED.
type PlaybackSession struct {
	ID                 string  `json:"session_id"`
	Status             string  `json:"status"`
	MediaItemID        string  `json:"media_item_id"`
	MediaFileID        string  `json:"media_file_id"`
	PlaybackMode       string  `json:"playback_mode"`
	StartPositionMS    int64   `json:"start_position_ms"`
	PositionMS         int64   `json:"position_ms"`
	DurationMS         *int64  `json:"duration_ms"`
	StartedAt          string  `json:"started_at"`
	LastActivityAt     *string `json:"last_activity_at"`
	EndedAt            *string `json:"ended_at"`
	IdleTimeoutSeconds int     `json:"idle_timeout_seconds"`
}

// CreateSessionRequest pide reproducir un elemento (§16.3). client_profile se
// decodifica aparte: un campo omitido toma el valor de la plataforma y uno
// enviado, aunque vacío, manda.
type CreateSessionRequest struct {
	MediaItemID           string          `json:"media_item_id"`
	MediaFileID           string          `json:"media_file_id"`
	DeviceID              string          `json:"device_id"`
	ClientProfile         json.RawMessage `json:"client_profile"`
	SelectedAudioStreamID *string         `json:"selected_audio_stream_id"`
	SelectedSubtitleID    *string         `json:"selected_subtitle_id"`
	MaxBitrate            *int64          `json:"max_bitrate"`
	MaxResolution         string          `json:"max_resolution"`
	PreferredProtocol     string          `json:"preferred_protocol"`
	StartPositionMS       int64           `json:"start_position_ms"`
}

// PlaybackReason explica por qué no es Direct Play.
type PlaybackReason struct {
	Code   string `json:"code"`
	Stream string `json:"stream"`
}

// CreateSessionResponse es la respuesta de §9.5. Las URL son opacas: el
// cliente no las construye.
type CreateSessionResponse struct {
	SessionID             string           `json:"session_id"`
	Status                string           `json:"status"`
	MediaFileID           string           `json:"media_file_id"`
	PlaybackMode          string           `json:"playback_mode"`
	Protocol              string           `json:"protocol"`
	Container             string           `json:"container"`
	VideoAction           string           `json:"video_action"`
	AudioAction           string           `json:"audio_action"`
	VideoCodec            *string          `json:"video_codec"`
	AudioCodec            *string          `json:"audio_codec"`
	SubtitleMode          string           `json:"subtitle_mode"`
	SelectedAudioStreamID *string          `json:"selected_audio_stream_id"`
	SelectedSubtitleID    *string          `json:"selected_subtitle_id"`
	StreamURL             string           `json:"stream_url"`
	SubtitleURL           *string          `json:"subtitle_url"`
	StartPositionMS       int64            `json:"start_position_ms"`
	DurationMS            *int64           `json:"duration_ms"`
	IdleTimeoutSeconds    int              `json:"idle_timeout_seconds"`
	Reasons               []PlaybackReason `json:"reasons"`
}

// ChangeTracksRequest cambia las pistas de una sesión viva. Un campo omitido
// no cambia; selected_subtitle_id "none" desactiva los subtítulos.
type ChangeTracksRequest struct {
	SelectedAudioStreamID *string `json:"selected_audio_stream_id"`
	SelectedSubtitleID    *string `json:"selected_subtitle_id"`
	PositionMS            *int64  `json:"position_ms"`
}

// HeartbeatRequest informa del estado del reproductor.
type HeartbeatRequest struct {
	State      string `json:"state"`
	PositionMS *int64 `json:"position_ms"`
}

// ProgressRequest guarda un progreso. client_time es la hora del dato en ISO
// 8601; si es futura se usa la del servidor.
type ProgressRequest struct {
	MediaItemID string  `json:"media_item_id"`
	PositionMS  *int64  `json:"position_ms"`
	DurationMS  *int64  `json:"duration_ms"`
	Completed   *bool   `json:"completed"`
	ClientTime  *string `json:"client_time"`
}

// Progress es el progreso vigente de un elemento.
type Progress struct {
	MediaItemID string `json:"media_item_id"`
	PositionMS  int64  `json:"position_ms"`
	DurationMS  *int64 `json:"duration_ms"`
	Completed   bool   `json:"completed"`
	UpdatedAt   string `json:"updated_at"`
}

// ContinueItem es un elemento empezado. series_* solo aparece en episodios.
type ContinueItem struct {
	ID            string  `json:"id"`
	Type          string  `json:"type"`
	Title         string  `json:"title"`
	Year          *int64  `json:"year"`
	SeasonNumber  *int64  `json:"season_number"`
	EpisodeNumber *int64  `json:"episode_number"`
	SeriesID      *string `json:"series_id"`
	SeriesTitle   *string `json:"series_title"`
	PositionMS    int64   `json:"position_ms"`
	DurationMS    *int64  `json:"duration_ms"`
	UpdatedAt     string  `json:"updated_at"`
	ImageURL      *string `json:"image_url"`
}

// HistoryEntry es una reproducción terminada. media_item_id es null si el
// elemento ya no existe; el título se conserva.
type HistoryEntry struct {
	ID          string  `json:"id"`
	MediaItemID *string `json:"media_item_id"`
	DeviceID    *string `json:"device_id"`
	Title       string  `json:"title"`
	PositionMS  int64   `json:"position_ms"`
	Completed   bool    `json:"completed"`
	WatchedAt   string  `json:"watched_at"`
}

// HistoryPage es una página del historial.
type HistoryPage struct {
	Items      []HistoryEntry `json:"items"`
	NextCursor *string        `json:"next_cursor"`
}
