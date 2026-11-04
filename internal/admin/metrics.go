package admin

import (
	"context"
	"runtime"

	"medianode/internal/monitoring"
)

// MetricsSnapshot son las métricas de §22.2 en un instante.
type MetricsSnapshot struct {
	CPUPercent        float64    `json:"cpu_percent"` // De toda la máquina, del proceso del servidor.
	MemoryBytes       uint64     `json:"memory_bytes"`
	Goroutines        int        `json:"goroutines"`
	DiskFreeBytes     uint64     `json:"disk_free_bytes"`
	DiskTotalBytes    uint64     `json:"disk_total_bytes"`
	FilesIndexed      int        `json:"files_indexed"`
	LastScans         []ScanInfo `json:"last_scans"`
	ActiveSessions    int        `json:"active_sessions"`
	ActiveJobs        int        `json:"active_jobs"`
	TranscodeFailures int64      `json:"transcode_failures"`
	BitrateBPS        int64      `json:"bitrate_bps"` // Suma aproximada de las sesiones activas.
	ConnectionsLocal  int        `json:"connections_local"`
	ConnectionsRemote int        `json:"connections_remote"`
	Endpoints         []Endpoint `json:"endpoints"`
}

// ScanInfo es un escaneo terminado.
type ScanInfo struct {
	Library         string  `json:"library"`
	Status          string  `json:"status"`
	FinishedAt      string  `json:"finished_at"`
	DurationSeconds float64 `json:"duration_seconds"`
	FilesProcessed  int     `json:"files_processed"`
}

// Endpoint es la latencia de una ruta.
type Endpoint struct {
	Route   string  `json:"route"`
	Count   int64   `json:"count"`
	Errors  int64   `json:"errors"`
	P50MS   float64 `json:"p50_ms"`
	P95MS   float64 `json:"p95_ms"`
	MaxMS   float64 `json:"max_ms"`
	Samples int     `json:"samples"`
}

// topEndpoints es cuántas rutas se devuelven: las más usadas.
const topEndpoints = 15

// recentScans es cuántos escaneos terminados se devuelven.
const recentScans = 5

// MetricsNow reúne las métricas.
func (s *Service) MetricsNow(ctx context.Context) MetricsSnapshot {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	m := MetricsSnapshot{
		MemoryBytes: mem.Sys, Goroutines: runtime.NumGoroutine(), LastScans: []ScanInfo{}, Endpoints: []Endpoint{},
		FilesIndexed:   s.count(ctx, "SELECT COUNT(*) FROM media_files WHERE missing_since IS NULL"),
		ActiveSessions: s.Sessions.Active(),
	}
	if s.CPU != nil {
		m.CPUPercent = s.CPU.CPUPercent()
	}
	if free, total, err := monitoring.DiskSpace(s.dataDir()); err == nil {
		m.DiskFreeBytes, m.DiskTotalBytes = free, total
	}
	if s.Transcoder != nil {
		m.ActiveJobs = len(s.Transcoder.Jobs())
		m.TranscodeFailures = s.Transcoder.Failures()
	}
	for _, sess := range s.SessionList(ctx) {
		m.BitrateBPS += sess.BitrateBPS
	}
	m.ConnectionsLocal, m.ConnectionsRemote = s.Connections()
	m.LastScans = s.lastScans(ctx)
	if s.Metrics != nil {
		for i, l := range s.Metrics.Latency() {
			if i == topEndpoints {
				break
			}
			m.Endpoints = append(m.Endpoints, Endpoint{Route: l.Route, Count: l.Count, Errors: l.Errors,
				P50MS: l.P50MS, P95MS: l.P95MS, MaxMS: l.MaxMS, Samples: l.Samples})
		}
	}
	return m
}

func (s *Service) lastScans(ctx context.Context) []ScanInfo {
	out := []ScanInfo{}
	rows, err := s.DB.Read.QueryContext(ctx, `
		SELECT l.name, j.status, j.finished_at, j.started_at, j.files_processed
		FROM scan_jobs j JOIN libraries l ON l.id = j.library_id
		WHERE j.finished_at IS NOT NULL AND j.started_at IS NOT NULL
		ORDER BY j.finished_at DESC LIMIT ?`, recentScans)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var name, status, finished, started string
		var processed int
		if rows.Scan(&name, &status, &finished, &started, &processed) != nil {
			continue
		}
		info := ScanInfo{Library: name, Status: status, FinishedAt: finished, FilesProcessed: processed}
		info.DurationSeconds = duration(started, finished)
		out = append(out, info)
	}
	_ = rows.Err() // Una lectura a medias deja la lista como esté: es informativa.
	return out
}
