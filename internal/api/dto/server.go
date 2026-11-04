// Package dto define los contratos JSON de la API.
package dto

// Health es la respuesta de supervisión.
type Health struct {
	Status string `json:"status"`
}

// ServerInfo es la información pública del servidor.
type ServerInfo struct {
	Name            string `json:"name"`
	Version         string `json:"version"`
	ProtocolVersion int    `json:"protocol_version"`
	SetupRequired   bool   `json:"setup_required"`
	AdminAvailable  bool   `json:"admin_available"`
}

// ServerStatus es el estado del servidor. Los contadores y la cola de
// metadatos solo llegan a los administradores.
type ServerStatus struct {
	Status        string       `json:"status"`
	Version       string       `json:"version"`
	UptimeSeconds int64        `json:"uptime_seconds"`
	Admin         *AdminStatus `json:"admin,omitempty"`
}

// AdminStatus son los detalles de estado para administradores.
type AdminStatus struct {
	FFprobeAvailable bool           `json:"ffprobe_available"`
	Libraries        int64          `json:"libraries"`
	ScansRunning     int64          `json:"scans_running"`
	Metadata         MetadataStatus `json:"metadata"`
}

// MetadataStatus es el estado de la cola de metadatos.
type MetadataStatus struct {
	Provider    *string `json:"provider"`
	Configured  bool    `json:"configured"`
	Pending     int64   `json:"pending"`
	PausedUntil *string `json:"paused_until"`
	LastError   *string `json:"last_error"`
}

// ServerCapabilities describe qué ofrece esta versión del servidor. Es
// pública: el cliente la consulta antes de iniciar sesión.
type ServerCapabilities struct {
	ProtocolVersion  int      `json:"protocol_version"`
	MetadataProvider *string  `json:"metadata_provider"`
	ImageTypes       []string `json:"image_types"`
	Search           bool     `json:"search"`
	DirectPlay       bool     `json:"direct_play"`
	Transcoding      bool     `json:"transcoding"`
	VideoEncoder     *string  `json:"video_encoder"`
	HLS              bool     `json:"hls"`
	// Subtítulos y HDR (Fase 9): lo que el FFmpeg instalado puede hacer.
	SubtitleConversion bool `json:"subtitle_conversion"`
	SubtitleBurnIn     bool `json:"subtitle_burn_in"`
	ToneMapping        bool `json:"tone_mapping"`
	// Veces el tiempo real a las que este equipo convierte un HDR de 720p a SDR
	// por software; null si no se midió.
	ToneMappingSpeed *float64 `json:"tone_mapping_speed"`
	Events           bool     `json:"events"`
}

// ServerVersion identifica la versión instalada.
type ServerVersion struct {
	Version         string `json:"version"`
	ProtocolVersion int    `json:"protocol_version"`
}

// TranscodeJob es un trabajo de FFmpeg activo.
type TranscodeJob struct {
	ID           string  `json:"id"`
	SessionID    string  `json:"session_id"`
	Kind         string  `json:"kind"` // heavy recodifica el vídeo; light copia o recodifica solo el audio.
	Encoder      *string `json:"encoder"`
	StartSegment int     `json:"start_segment"`
	Segment      int     `json:"segment"`
	PositionMS   int64   `json:"position_ms"`
	StartedAt    string  `json:"started_at"`
}

// Capacity es el uso de una clase de cupo.
type Capacity struct {
	Used  int `json:"used"`
	Limit int `json:"limit"`
}

// Transcodes son los trabajos activos y los cupos.
type Transcodes struct {
	Jobs     []TranscodeJob      `json:"jobs"`
	Capacity map[string]Capacity `json:"capacity"`
}
