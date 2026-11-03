// Package config carga y valida la configuración del servidor.
//
// La precedencia es: flag --config (elige el archivo), variables MEDIANODE_*,
// archivo YAML y valores por defecto.
package config

import (
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	envConfigPath     = "MEDIANODE_CONFIG"
	envPrefix         = "MEDIANODE"
	defaultConfigPath = "config/server.yaml"
)

var reLanguage = regexp.MustCompile(`^[a-z]{2}(-[A-Z]{2})?$`)

// Config refleja la sección 20.2 del documento maestro.
type Config struct {
	Server      Server      `yaml:"server"`
	Database    Database    `yaml:"database"`
	Paths       Paths       `yaml:"paths"`
	Logging     Logging     `yaml:"logging"`
	Scanner     Scanner     `yaml:"scanner"`
	Metadata    Metadata    `yaml:"metadata"`
	Transcoding Transcoding `yaml:"transcoding"`
	Sessions    Sessions    `yaml:"sessions"`
	Discovery   Discovery   `yaml:"discovery"`
	Remote      Remote      `yaml:"remote"`
}

// Server agrupa los parámetros de red.
type Server struct {
	Name                   string   `yaml:"name"`
	BindAddress            string   `yaml:"bind_address"`
	HTTPPort               int      `yaml:"http_port"`
	HTTPSEnabled           bool     `yaml:"https_enabled"`
	HTTPSPort              int      `yaml:"https_port"`
	TrustedProxies         []string `yaml:"trusted_proxies"`
	CORSOrigins            []string `yaml:"cors_origins"`
	ShutdownTimeoutSeconds int      `yaml:"shutdown_timeout_seconds"`
}

// Database agrupa los parámetros de SQLite.
type Database struct {
	Path          string `yaml:"path"`
	JournalMode   string `yaml:"journal_mode"`
	BusyTimeoutMS int    `yaml:"busy_timeout_ms"`
}

// Paths agrupa los directorios de datos.
type Paths struct {
	Metadata   string `yaml:"metadata"`
	Images     string `yaml:"images"`
	Cache      string `yaml:"cache"`
	Transcodes string `yaml:"transcodes"`
	Logs       string `yaml:"logs"`
	Backups    string `yaml:"backups"`
}

// Logging agrupa los parámetros de registro.
type Logging struct {
	Level string `yaml:"level"`
}

// Scanner agrupa los parámetros de indexación.
type Scanner struct {
	Workers                  int `yaml:"workers"`
	ReconcileIntervalMinutes int `yaml:"reconcile_interval_minutes"`
	MissingFileRetentionDays int `yaml:"missing_file_retention_days"`
}

// Metadata agrupa el proveedor de metadatos (§8). Sin clave, el catálogo usa
// los datos de los archivos. La clave puede llegar por
// MEDIANODE_METADATA_TMDB_API_KEY para no escribirla en el archivo.
type Metadata struct {
	Provider   string `yaml:"provider"`
	Language   string `yaml:"language"`
	TMDBAPIKey string `yaml:"tmdb_api_key"`
}

// Transcoding agrupa los límites de FFmpeg.
type Transcoding struct {
	MaxConcurrentJobs      int    `yaml:"max_concurrent_jobs"`
	MaxConcurrentLightJobs int    `yaml:"max_concurrent_light_jobs"`
	SegmentSeconds         int    `yaml:"segment_seconds"`
	SeekRestartSegments    int    `yaml:"seek_restart_segments"`
	HardwareAcceleration   string `yaml:"hardware_acceleration"`
	CleanupAfterMinutes    int    `yaml:"cleanup_after_minutes"`
	MaxDiskGB              int    `yaml:"max_disk_gb"`
}

// Sessions agrupa los tiempos de sesión.
type Sessions struct {
	HeartbeatTimeoutSeconds        int `yaml:"heartbeat_timeout_seconds"`
	ProgressPersistIntervalSeconds int `yaml:"progress_persist_interval_seconds"`
	CompletedThresholdPercent      int `yaml:"completed_threshold_percent"`
}

// Discovery agrupa el descubrimiento en la red local.
type Discovery struct {
	MDNSEnabled bool `yaml:"mdns_enabled"`
	DLNAEnabled bool `yaml:"dlna_enabled"`
}

// Remote agrupa el acceso remoto.
type Remote struct {
	Enabled       bool `yaml:"enabled"`
	DirectEnabled bool `yaml:"direct_enabled"`
	RelayEnabled  bool `yaml:"relay_enabled"`
	AdminEnabled  bool `yaml:"admin_enabled"`
}

// Default devuelve la configuración base. HTTPS queda desactivado hasta que
// exista el certificado propio de la Fase 11.
func Default() *Config {
	return &Config{
		Server: Server{
			Name:                   "MediaNode",
			BindAddress:            "0.0.0.0",
			HTTPPort:               8420,
			HTTPSPort:              8421,
			ShutdownTimeoutSeconds: 30,
		},
		Database: Database{Path: "./data/medianode.db", JournalMode: "WAL", BusyTimeoutMS: 5000},
		Paths: Paths{
			Metadata:   "./data/metadata",
			Images:     "./data/images",
			Cache:      "./data/cache",
			Transcodes: "./data/transcodes",
			Logs:       "./logs",
			Backups:    "./backups",
		},
		Logging:  Logging{Level: "info"},
		Scanner:  Scanner{Workers: 2, ReconcileIntervalMinutes: 60, MissingFileRetentionDays: 30},
		Metadata: Metadata{Provider: "tmdb", Language: "es-ES"},
		Transcoding: Transcoding{
			MaxConcurrentJobs:      1,
			MaxConcurrentLightJobs: 4,
			SegmentSeconds:         6,
			SeekRestartSegments:    5,
			HardwareAcceleration:   "auto",
			CleanupAfterMinutes:    30,
			MaxDiskGB:              20,
		},
		Sessions:  Sessions{HeartbeatTimeoutSeconds: 90, ProgressPersistIntervalSeconds: 30, CompletedThresholdPercent: 90},
		Discovery: Discovery{MDNSEnabled: true},
	}
}

// Load construye la configuración efectiva y la valida. Si path está vacío usa
// MEDIANODE_CONFIG y, por último, config/server.yaml si existe. Un archivo
// indicado de forma explícita debe existir.
func Load(path string) (*Config, error) {
	explicit := path != ""
	if !explicit {
		if v, ok := os.LookupEnv(envConfigPath); ok && v != "" {
			path, explicit = v, true
		} else {
			path = defaultConfigPath
		}
	}

	cfg := Default()
	loaded, err := readFile(path, cfg)
	if err != nil {
		if explicit || !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}

	if err := applyEnv(reflect.ValueOf(cfg).Elem(), envPrefix, os.LookupEnv); err != nil {
		return nil, err
	}

	base := ""
	if loaded {
		base = path
	}
	if err := cfg.resolvePaths(base); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func readFile(path string, cfg *Config) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("abrir configuración: %w", err)
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("leer configuración %s: %w", path, err)
	}
	return true, nil
}

// applyEnv recorre la estructura y aplica MEDIANODE_<SECCION>_<CLAVE>.
func applyEnv(v reflect.Value, prefix string, lookup func(string) (string, bool)) error {
	t := v.Type()
	for i := range t.NumField() {
		field := v.Field(i)
		name := prefix + "_" + strings.ToUpper(t.Field(i).Tag.Get("yaml"))

		if field.Kind() == reflect.Struct {
			if err := applyEnv(field, name, lookup); err != nil {
				return err
			}
			continue
		}

		raw, ok := lookup(name)
		if !ok {
			continue
		}
		if err := setField(field, raw); err != nil {
			return fmt.Errorf("variable %s: %w", name, err)
		}
	}
	return nil
}

func setField(field reflect.Value, raw string) error {
	switch field.Kind() {
	case reflect.String:
		field.SetString(raw)
	case reflect.Int:
		n, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("se esperaba un entero: %q", raw)
		}
		field.SetInt(int64(n))
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("se esperaba un booleano: %q", raw)
		}
		field.SetBool(b)
	case reflect.Slice:
		var items []string
		for _, s := range strings.Split(raw, ",") {
			if s = strings.TrimSpace(s); s != "" {
				items = append(items, s)
			}
		}
		field.Set(reflect.ValueOf(items))
	default:
		return fmt.Errorf("tipo no admitido: %s", field.Kind())
	}
	return nil
}

// resolvePaths convierte en absolutas las rutas relativas, tomando como base
// la raíz de instalación de §20.1. Un servicio no arranca necesariamente desde
// esa carpeta, por lo que el directorio actual solo se usa sin archivo.
func (c *Config) resolvePaths(configPath string) error {
	base, err := baseDir(configPath)
	if err != nil {
		return err
	}
	for _, p := range []*string{
		&c.Database.Path, &c.Paths.Metadata, &c.Paths.Images, &c.Paths.Cache,
		&c.Paths.Transcodes, &c.Paths.Logs, &c.Paths.Backups,
	} {
		if *p != "" && !filepath.IsAbs(*p) {
			*p = filepath.Join(base, *p)
		}
	}
	return nil
}

func baseDir(configPath string) (string, error) {
	if configPath == "" {
		return os.Getwd()
	}
	abs, err := filepath.Abs(configPath)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(abs)
	if filepath.Base(dir) == "config" {
		return filepath.Dir(dir), nil
	}
	return dir, nil
}

// Validate comprueba la coherencia de la configuración y devuelve todos los
// problemas a la vez, para no obligar a corregirlos de uno en uno.
func (c *Config) Validate() error {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if strings.TrimSpace(c.Server.Name) == "" {
		fail("server.name no puede estar vacío")
	}
	if _, err := netip.ParseAddr(c.Server.BindAddress); err != nil {
		fail("server.bind_address no es una dirección IP válida: %q", c.Server.BindAddress)
	}
	checkPort := func(name string, port int) {
		if port < 1 || port > 65535 {
			fail("%s fuera de rango: %d", name, port)
		}
	}
	checkPort("server.http_port", c.Server.HTTPPort)
	checkPort("server.https_port", c.Server.HTTPSPort)
	if c.Server.HTTPPort == c.Server.HTTPSPort {
		fail("server.http_port y server.https_port no pueden coincidir")
	}
	if c.Server.HTTPSEnabled {
		fail("server.https_enabled aún no está disponible: el certificado propio llega en la Fase 11")
	}
	for _, p := range c.Server.TrustedProxies {
		if !validProxy(p) {
			fail("server.trusted_proxies contiene un valor inválido: %q", p)
		}
	}
	for _, o := range c.Server.CORSOrigins {
		if !validOrigin(o) {
			fail("server.cors_origins contiene un origen inválido: %q", o)
		}
	}

	if c.Database.JournalMode != "WAL" {
		fail("database.journal_mode debe ser WAL: %q", c.Database.JournalMode)
	}

	for name, path := range map[string]string{
		"database.path":    c.Database.Path,
		"paths.metadata":   c.Paths.Metadata,
		"paths.images":     c.Paths.Images,
		"paths.cache":      c.Paths.Cache,
		"paths.transcodes": c.Paths.Transcodes,
		"paths.logs":       c.Paths.Logs,
		"paths.backups":    c.Paths.Backups,
	} {
		if !filepath.IsAbs(path) {
			fail("%s debe ser una ruta absoluta: %q", name, path)
		}
	}

	if !slices.Contains([]string{"debug", "info", "warn", "error"}, c.Logging.Level) {
		fail("logging.level inválido: %q", c.Logging.Level)
	}

	for name, limit := range map[string]struct{ value, min int }{
		"server.shutdown_timeout_seconds":            {c.Server.ShutdownTimeoutSeconds, 1},
		"database.busy_timeout_ms":                   {c.Database.BusyTimeoutMS, 0},
		"scanner.workers":                            {c.Scanner.Workers, 1},
		"scanner.reconcile_interval_minutes":         {c.Scanner.ReconcileIntervalMinutes, 1},
		"scanner.missing_file_retention_days":        {c.Scanner.MissingFileRetentionDays, 0},
		"transcoding.max_concurrent_jobs":            {c.Transcoding.MaxConcurrentJobs, 1},
		"transcoding.max_concurrent_light_jobs":      {c.Transcoding.MaxConcurrentLightJobs, 1},
		"transcoding.segment_seconds":                {c.Transcoding.SegmentSeconds, 1},
		"transcoding.seek_restart_segments":          {c.Transcoding.SeekRestartSegments, 1},
		"transcoding.cleanup_after_minutes":          {c.Transcoding.CleanupAfterMinutes, 1},
		"transcoding.max_disk_gb":                    {c.Transcoding.MaxDiskGB, 1},
		"sessions.heartbeat_timeout_seconds":         {c.Sessions.HeartbeatTimeoutSeconds, 1},
		"sessions.progress_persist_interval_seconds": {c.Sessions.ProgressPersistIntervalSeconds, 1},
	} {
		if limit.value < limit.min {
			fail("%s debe ser al menos %d: %d", name, limit.min, limit.value)
		}
	}

	if !slices.Contains([]string{"tmdb", "none"}, c.Metadata.Provider) {
		fail("metadata.provider debe ser tmdb o none: %q", c.Metadata.Provider)
	}
	if !reLanguage.MatchString(c.Metadata.Language) {
		fail("metadata.language debe tener la forma «es» o «es-ES»: %q", c.Metadata.Language)
	}

	if p := c.Sessions.CompletedThresholdPercent; p < 50 || p > 100 {
		fail("sessions.completed_threshold_percent debe estar entre 50 y 100: %d", p)
	}

	accel := []string{"auto", "none", "nvenc", "qsv", "vaapi", "amf", "videotoolbox"}
	if !slices.Contains(accel, c.Transcoding.HardwareAcceleration) {
		fail("transcoding.hardware_acceleration inválido: %q", c.Transcoding.HardwareAcceleration)
	}

	if (c.Remote.DirectEnabled || c.Remote.RelayEnabled || c.Remote.AdminEnabled) && !c.Remote.Enabled {
		fail("remote.enabled debe estar activo para usar remote.direct_enabled, relay_enabled o admin_enabled")
	}

	return errors.Join(errs...)
}

// validOrigin exige la forma exacta que envía un navegador: esquema y host, sin
// ruta ni barra final. Otra forma nunca coincidiría con la cabecera Origin.
func validOrigin(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	return u.Scheme+"://"+u.Host == s
}

func validProxy(s string) bool {
	if _, err := netip.ParseAddr(s); err == nil {
		return true
	}
	_, err := netip.ParsePrefix(s)
	return err == nil
}
