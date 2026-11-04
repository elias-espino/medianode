// Package admin reúne lo que el panel de administración muestra del servidor:
// sistema, métricas, sesiones, almacenamiento, registros, ajustes y el
// diagnóstico (§19.1, §22.2). Solo lee; nada de aquí cambia el estado.
package admin

import (
	"context"
	"database/sql"
	"net"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"medianode/internal/config"
	"medianode/internal/database"
	"medianode/internal/ffmpeg"
	"medianode/internal/monitoring"
	"medianode/internal/sessions"
	"medianode/internal/transcoding"
)

// Service responde a las consultas del panel.
type Service struct {
	Version     string
	Started     time.Time
	ConfigPath  string
	Config      config.Config
	DB          *database.DB
	FFmpegPath  string
	FFprobePath string
	FFmpeg      *ffmpeg.Capabilities
	Sessions    *sessions.Manager
	Transcoder  *transcoding.Manager
	Metrics     *monitoring.Metrics
	CPU         *monitoring.ProcessSampler

	probeOnce sync.Once
	probeVer  string
}

// Tool describe un programa externo.
type Tool struct {
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
}

// System es el resumen del servidor y del equipo donde corre.
type System struct {
	Version       string         `json:"version"`
	OS            string         `json:"os"`
	Arch          string         `json:"arch"`
	GoVersion     string         `json:"go_version"`
	CPUs          int            `json:"cpus"`
	UptimeSeconds int64          `json:"uptime_seconds"`
	Memory        Memory         `json:"memory"`
	FFmpeg        Tool           `json:"ffmpeg"`
	FFprobe       Tool           `json:"ffprobe"`
	Hardware      Hardware       `json:"hardware"`
	Database      DatabaseInfo   `json:"database"`
	Counts        map[string]int `json:"counts"`
	ConfigPath    string         `json:"config_path"`
}

// Memory es la memoria del proceso del servidor.
type Memory struct {
	AllocBytes uint64 `json:"alloc_bytes"`
	SysBytes   uint64 `json:"sys_bytes"`
}

// Hardware es lo que FFmpeg puede usar para codificar y filtrar.
type Hardware struct {
	Encoder          string   `json:"encoder"`           // El que usan los trabajos nuevos.
	Compiled         []string `json:"encoders_compiled"` // Los que FFmpeg trae.
	Working          []string `json:"encoders_working"`  // Los que además inician en este equipo.
	SubtitleBurnIn   bool     `json:"subtitle_burn_in"`
	ToneMapping      bool     `json:"tone_mapping"`
	ToneMappingSpeed *float64 `json:"tone_mapping_speed"`
}

// DatabaseInfo describe la base de datos.
type DatabaseInfo struct {
	SizeBytes   int64  `json:"size_bytes"`
	JournalMode string `json:"journal_mode"`
}

// System devuelve el resumen del servidor.
func (s *Service) System(ctx context.Context) System {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	out := System{
		Version: s.Version, OS: runtime.GOOS, Arch: runtime.GOARCH, GoVersion: runtime.Version(), CPUs: runtime.NumCPU(),
		UptimeSeconds: int64(time.Since(s.Started).Seconds()), ConfigPath: s.ConfigPath,
		Memory:   Memory{AllocBytes: mem.Alloc, SysBytes: mem.Sys},
		Hardware: s.hardware(),
		Database: DatabaseInfo{SizeBytes: s.dbSize(), JournalMode: s.Config.Database.JournalMode},
		Counts:   map[string]int{},
	}
	if s.FFmpeg != nil {
		out.FFmpeg = Tool{Available: true, Version: s.FFmpeg.Version}
	}
	if s.FFprobePath != "" {
		out.FFprobe = Tool{Available: true, Version: s.ffprobeVersion(ctx)}
	}
	for key, query := range map[string]string{
		"libraries": "SELECT COUNT(*) FROM libraries",
		"files":     "SELECT COUNT(*) FROM media_files WHERE missing_since IS NULL",
		"items":     "SELECT COUNT(*) FROM media_items",
		"users":     "SELECT COUNT(*) FROM users",
	} {
		out.Counts[key] = s.count(ctx, query)
	}
	return out
}

func (s *Service) count(ctx context.Context, query string) int {
	var n int
	if err := s.DB.Read.QueryRowContext(ctx, query).Scan(&n); err != nil {
		return 0
	}
	return n
}

func (s *Service) hardware() Hardware {
	h := Hardware{Compiled: []string{}, Working: []string{}}
	if s.Transcoder != nil {
		h.Encoder = string(s.Transcoder.Encoder())
	}
	if c := s.FFmpeg; c != nil {
		for e := range c.Encoders {
			h.Compiled = append(h.Compiled, string(e))
		}
		sort.Strings(h.Compiled)
		for _, e := range c.Working {
			h.Working = append(h.Working, string(e))
		}
		h.SubtitleBurnIn = c.BurnInText && c.BurnInImage
		h.ToneMapping = c.ToneMapping
		if c.ToneMapSpeed > 0 {
			speed := c.ToneMapSpeed
			h.ToneMappingSpeed = &speed
		}
	}
	return h
}

// dbSize suma la base y sus archivos auxiliares del modo WAL.
func (s *Service) dbSize() int64 {
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if info, err := os.Stat(s.Config.Database.Path + suffix); err == nil {
			total += info.Size()
		}
	}
	return total
}

// ffprobeVersion lee la versión una sola vez: no cambia mientras el servidor
// corre y arrancar un proceso en cada consulta del panel sobra.
func (s *Service) ffprobeVersion(ctx context.Context) string {
	s.probeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, s.FFprobePath, "-version").Output()
		if err != nil {
			return
		}
		line, _, _ := strings.Cut(string(out), "\n")
		line = strings.TrimSpace(strings.TrimPrefix(line, "ffprobe version "))
		s.probeVer, _, _ = strings.Cut(line, " ")
	})
	return s.probeVer
}

// IsLocal indica si una IP es de la propia máquina o de la red local.
func IsLocal(ip string) bool {
	addr := net.ParseIP(strings.TrimSpace(ip))
	if addr == nil {
		return false
	}
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast()
}

// SessionInfo es una sesión activa vista por el administrador.
type SessionInfo struct {
	ID           string `json:"id"`
	User         string `json:"user"`
	Title        string `json:"title"`
	Mode         string `json:"mode"`
	Status       string `json:"status"`
	PositionMS   int64  `json:"position_ms"`
	DurationMS   *int64 `json:"duration_ms"`
	StartedAt    string `json:"started_at"`
	ClientIP     string `json:"client_ip"`
	Local        bool   `json:"local"`
	BitrateBPS   int64  `json:"bitrate_bps"` // Aproximado: el del archivo o el de la salida.
	Job          string `json:"job"`         // heavy, light o vacío en Direct Play.
	Encoder      string `json:"encoder"`
	ClientKind   string `json:"client_kind"`
	DeviceID     string `json:"device_id"`
	LastActivity string `json:"last_activity"`
}

// SessionList devuelve las sesiones activas, de la más antigua a la más
// reciente.
func (s *Service) SessionList(ctx context.Context) []SessionInfo {
	out := []SessionInfo{}
	jobs := map[string]transcoding.JobInfo{}
	if s.Transcoder != nil {
		for _, j := range s.Transcoder.Jobs() {
			jobs[j.SessionID] = j
		}
	}
	names := map[string]string{}
	for _, sess := range s.Sessions.Snapshots() {
		user, ok := names[sess.UserID]
		if !ok {
			user = s.username(ctx, sess.UserID)
			names[sess.UserID] = user
		}
		info := SessionInfo{
			ID: sess.ID, User: user, Title: sess.Title, Mode: sess.Mode, Status: string(sess.Status),
			PositionMS: sess.PositionMS, DurationMS: sess.DurationMS, StartedAt: database.FormatTime(sess.StartedAt),
			ClientIP: sess.ClientIP, Local: IsLocal(sess.ClientIP), ClientKind: sess.ClientKind, DeviceID: sess.DeviceID,
			LastActivity: database.FormatTime(sess.LastActivity),
		}
		if j, ok := jobs[sess.ID]; ok {
			info.Job, info.Encoder, info.BitrateBPS = string(j.Kind), j.Encoder, int64(j.Bandwidth)
		} else {
			info.BitrateBPS = s.fileBitrate(ctx, sess.FileID)
		}
		out = append(out, info)
	}
	return out
}

func (s *Service) username(ctx context.Context, id string) string {
	if id == "" {
		return ""
	}
	var name string
	if err := s.DB.Read.QueryRowContext(ctx, "SELECT username FROM users WHERE id = ?", id).Scan(&name); err != nil {
		return ""
	}
	return name
}

func (s *Service) fileBitrate(ctx context.Context, fileID string) int64 {
	var n sql.NullInt64
	if err := s.DB.Read.QueryRowContext(ctx, "SELECT bitrate FROM media_files WHERE id = ?", fileID).Scan(&n); err != nil {
		return 0
	}
	return n.Int64
}

// Connections cuenta las sesiones activas que vienen de la red local y las que
// vienen de fuera.
func (s *Service) Connections() (local, remote int) {
	for _, sess := range s.Sessions.Snapshots() {
		if IsLocal(sess.ClientIP) {
			local++
		} else {
			remote++
		}
	}
	return local, remote
}

// NetworkInfo es la configuración de red y las direcciones donde se llega.
type NetworkInfo struct {
	BindAddress    string   `json:"bind_address"`
	HTTPPort       int      `json:"http_port"`
	HTTPSEnabled   bool     `json:"https_enabled"`
	TrustedProxies []string `json:"trusted_proxies"`
	CORSOrigins    []string `json:"cors_origins"`
	Addresses      []string `json:"addresses"` // URL por las que se llega desde la red local.
	Local          int      `json:"connections_local"`
	Remote         int      `json:"connections_remote"`
}

// Network devuelve la configuración de red.
func (s *Service) Network() NetworkInfo {
	c := s.Config.Server
	n := NetworkInfo{BindAddress: c.BindAddress, HTTPPort: c.HTTPPort, HTTPSEnabled: c.HTTPSEnabled,
		TrustedProxies: nonNil(c.TrustedProxies), CORSOrigins: nonNil(c.CORSOrigins), Addresses: []string{}}
	n.Local, n.Remote = s.Connections()
	for _, ip := range localAddresses(c.BindAddress) {
		n.Addresses = append(n.Addresses, "http://"+net.JoinHostPort(ip, strconv.Itoa(c.HTTPPort)))
	}
	return n
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// interfaceAddrs es net.InterfaceAddrs; las pruebas lo sustituyen.
var interfaceAddrs = net.InterfaceAddrs

// localAddresses son las IPv4 por las que se llega al servidor: la propia
// dirección de enlace si es concreta, o las de las interfaces si escucha en
// todas.
func localAddresses(bind string) []string {
	ip := net.ParseIP(bind)
	if ip != nil && !ip.IsUnspecified() {
		return []string{bind}
	}
	var out []string
	addrs, err := interfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.To4() == nil || ipn.IP.IsLoopback() || ipn.IP.IsLinkLocalUnicast() {
			continue
		}
		out = append(out, ipn.IP.String())
	}
	sort.Strings(out)
	return out
}
