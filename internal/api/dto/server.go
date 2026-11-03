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
	HLS              bool     `json:"hls"`
	Events           bool     `json:"events"`
}

// ServerVersion identifica la versión instalada.
type ServerVersion struct {
	Version         string `json:"version"`
	ProtocolVersion int    `json:"protocol_version"`
}
