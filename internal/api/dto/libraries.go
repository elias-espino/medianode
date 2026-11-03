package dto

// Library es una biblioteca. Paths solo aparece para los administradores,
// porque son rutas físicas del servidor.
type Library struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Type        string        `json:"type"`
	Enabled     bool          `json:"enabled"`
	DLNAEnabled bool          `json:"dlna_enabled"`
	CreatedAt   string        `json:"created_at"`
	UpdatedAt   string        `json:"updated_at"`
	Paths       []LibraryPath `json:"paths,omitempty"`
}

// LibraryPath es una raíz de biblioteca.
type LibraryPath struct {
	ID          string  `json:"id"`
	Path        string  `json:"path"`
	IsAvailable bool    `json:"is_available"`
	LastScanAt  *string `json:"last_scan_at"`
}

// CreateLibraryRequest crea una biblioteca.
type CreateLibraryRequest struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	DLNAEnabled bool     `json:"dlna_enabled"`
	Paths       []string `json:"paths"`
}

// UpdateLibraryRequest cambia los campos presentes.
type UpdateLibraryRequest struct {
	Name        *string `json:"name"`
	Enabled     *bool   `json:"enabled"`
	DLNAEnabled *bool   `json:"dlna_enabled"`
}

// AddPathRequest añade una raíz.
type AddPathRequest struct {
	Path string `json:"path"`
}

// ScanJob es el estado de un escaneo.
type ScanJob struct {
	ID              string  `json:"id"`
	LibraryID       string  `json:"library_id"`
	Trigger         string  `json:"trigger"`
	Status          string  `json:"status"`
	StartedAt       *string `json:"started_at"`
	FinishedAt      *string `json:"finished_at"`
	FilesDiscovered int64   `json:"files_discovered"`
	FilesProcessed  int64   `json:"files_processed"`
	ErrorMessage    *string `json:"error_message"`
}

// LibraryGrant son los permisos de un usuario sobre una biblioteca.
type LibraryGrant struct {
	LibraryID   string   `json:"library_id"`
	Permissions []string `json:"permissions"`
}

// PermissionsRequest sustituye todos los permisos de un usuario.
type PermissionsRequest struct {
	Libraries []LibraryGrant `json:"libraries"`
}

// Permissions son los permisos por biblioteca de un usuario.
type Permissions struct {
	Libraries []LibraryGrant `json:"libraries"`
}
