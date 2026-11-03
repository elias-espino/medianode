// medianode-server es el punto de entrada del servidor.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"medianode/internal/api"
	"medianode/internal/api/routes"
	"medianode/internal/auth"
	"medianode/internal/authorization"
	"medianode/internal/config"
	"medianode/internal/database"
	"medianode/internal/devices"
	"medianode/internal/events"
	"medianode/internal/images"
	"medianode/internal/libraries"
	"medianode/internal/lifecycle"
	"medianode/internal/metadata"
	"medianode/internal/metadata/tmdb"
	"medianode/internal/monitoring"
	"medianode/internal/playback"
	"medianode/internal/probe"
	"medianode/internal/readmodel"
	"medianode/internal/scanner"
	"medianode/internal/sessions"
	"medianode/internal/storage"
	"medianode/internal/users"
	"medianode/web"
)

const (
	// probeTimeout limita cada análisis: un archivo dañado o una ruta de red
	// colgada no deben bloquear un trabajador.
	probeTimeout = 60 * time.Second
	// watchDebounce es el silencio que se espera tras un cambio antes de escanear.
	watchDebounce = 30 * time.Second
)

const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// version se inyecta en la compilación con -ldflags.
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop() // Una segunda señal termina el proceso sin esperar.
	}()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("medianode-server", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "ruta del archivo de configuración")
	showVersion := flags.Bool("version", false, "muestra la versión y termina")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}

	if *showVersion {
		fmt.Fprintln(stdout, "medianode-server", version)
		return exitOK
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, "configuración inválida:")
		fmt.Fprintln(stderr, err)
		return exitError
	}

	log, closer, err := monitoring.NewLogger(monitoring.LogConfig{
		Dir:     cfg.Paths.Logs,
		Level:   cfg.Logging.Level,
		Console: stdout,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}
	defer closer.Close()

	log.Info().Str("version", version).Msg("iniciando MediaNode")

	err = storage.EnsureDirs(cfg.Paths.Metadata, cfg.Paths.Images, cfg.Paths.Cache,
		cfg.Paths.Transcodes, cfg.Paths.Backups)
	if err != nil {
		log.Error().Err(err).Msg("no se pudieron crear los directorios de datos")
		return exitError
	}

	db, err := database.Open(ctx, cfg.Database)
	if err != nil {
		log.Error().Err(err).Msg("no se pudo abrir la base de datos")
		return exitError
	}
	if err := db.Migrate(ctx, cfg.Paths.Backups); err != nil {
		log.Error().Err(err).Msg("no se pudo migrar la base de datos")
		_ = db.Close()
		return exitError
	}
	log.Info().Msg("base de datos lista")

	timeout := time.Duration(cfg.Server.ShutdownTimeoutSeconds) * time.Second
	manager := lifecycle.New(timeout, monitoring.Module(log, "lifecycle"))

	// La base va primero para detenerse la última.
	manager.Register(db)

	// Las sesiones van justo después: reconcilian antes de que nada más
	// arranque y, al apagar, vuelcan el progreso cuando ya no llegan
	// peticiones.
	bus := events.NewBus()
	sessionManager := sessions.New(db, bus, sessions.Options{
		IdleTimeout:        time.Duration(cfg.Sessions.HeartbeatTimeoutSeconds) * time.Second,
		FlushInterval:      time.Duration(cfg.Sessions.ProgressPersistIntervalSeconds) * time.Second,
		CompletedThreshold: float64(cfg.Sessions.CompletedThresholdPercent) / 100,
		TranscodeDir:       cfg.Paths.Transcodes,
	}, monitoring.Module(log, "sessions"))
	manager.Register(sessionManager)

	hasher := auth.NewHasher(auth.DefaultParams)
	authService := auth.New(db, hasher, monitoring.Module(log, "auth"))
	setupCode, err := authService.Bootstrap(ctx)
	if err != nil {
		log.Error().Err(err).Msg("no se pudo preparar la autenticación")
		_ = db.Close()
		return exitError
	}
	if setupCode != "" {
		// Único valor secreto que va al log: un servicio no tiene consola, y sin el
		// código no se puede crear el Owner. Deja de valer al usarlo.
		log.Warn().Str("codigo", setupCode).Msg("servidor sin configurar: usa este código en /auth/setup")
	}

	ffprobe, err := probe.Locate()
	if err != nil {
		// El servidor arranca igual: los escaneos fallarán con SCAN_FAILED hasta
		// que FFprobe esté disponible.
		log.Error().Err(err).Msg("FFprobe no encontrado: no se podrán escanear bibliotecas")
	}
	var provider metadata.Provider
	switch {
	case cfg.Metadata.Provider == "tmdb" && cfg.Metadata.TMDBAPIKey != "":
		provider = tmdb.New(cfg.Metadata.TMDBAPIKey, cfg.Metadata.Language)
	case cfg.Metadata.Provider == "tmdb":
		log.Warn().Msg("sin clave de TMDB: el catálogo usará los datos de los archivos")
	}
	imageManager := images.New(cfg.Paths.Images, db, nil)
	enricher := metadata.NewEnricher(db, provider, imageManager, cfg.Metadata.Language,
		metadata.Options{}, monitoring.Module(log, "metadata"))
	enricher.OnUpdated = func(libraryID string) {
		bus.Publish(events.Event{Name: events.LibraryUpdated, LibraryID: libraryID,
			Data: map[string]any{"library_id": libraryID}})
	}
	// La cola arranca antes que el escáner: rellena los títulos ordenables
	// antes de que nadie liste.
	manager.Register(enricher)

	sc := scanner.New(db, &probe.Runner{Path: ffprobe, Timeout: probeTimeout}, scanner.Options{
		Workers:   cfg.Scanner.Workers,
		Retention: time.Duration(cfg.Scanner.MissingFileRetentionDays) * 24 * time.Hour,
	}, log)
	sc.OnEvent = func(e scanner.Event) {
		data := map[string]any{"library_id": e.LibraryID}
		if e.JobID != "" {
			data["job_id"] = e.JobID
		}
		bus.Publish(events.Event{Name: e.Name, LibraryID: e.LibraryID, Data: data})
		if e.Name == scanner.EventScanCompleted {
			enricher.Notify()
		}
	}
	watcher := scanner.NewWatcher(sc, watchDebounce,
		time.Duration(cfg.Scanner.ReconcileIntervalMinutes)*time.Minute, log)
	manager.Register(&scanner.Component{Scanner: sc, Watcher: watcher})

	authz := authorization.NewAuthorizer(db)
	reserved := []string{filepath.Dir(cfg.Database.Path), cfg.Paths.Metadata, cfg.Paths.Images,
		cfg.Paths.Cache, cfg.Paths.Transcodes, cfg.Paths.Logs, cfg.Paths.Backups}
	librariesService := libraries.New(db, authz, sc, reserved)
	librariesService.Sessions = sessionManager
	usersService := users.New(db, hasher)
	usersService.Sessions = sessionManager
	devicesService := devices.New(db)
	devicesService.Sessions, devicesService.Bus = sessionManager, bus

	playbackService := playback.NewService(db, authz, sessionManager, monitoring.Module(log, "playback"))
	sessionManager.AddListener(playbackService)

	router := routes.NewRouter(routes.Deps{
		Log:       monitoring.Module(log, "http"),
		Name:      cfg.Server.Name,
		Version:   version,
		Auth:      authService,
		Users:     usersService,
		Devices:   devicesService,
		Libraries: librariesService,
		Catalog:   readmodel.New(db, authz),
		Metadata:  metadata.NewService(db, authz, enricher),
		Enricher:  enricher,
		Images:    imageManager,
		Sessions:  sessionManager,
		Playback:  playbackService,
		DB:        db,
		Progress: sessions.NewProgress(db, authz, bus,
			float64(cfg.Sessions.CompletedThresholdPercent)/100),
		Bus:            bus,
		Authorizer:     authz,
		PathsChanged:   watcher.Refresh,
		Started:        time.Now(),
		FFprobe:        ffprobe != "",
		Web:            web.Handler(),
		TrustedProxies: cfg.Server.TrustedProxies,
		CORSOrigins:    cfg.Server.CORSOrigins,
	})
	addr := net.JoinHostPort(cfg.Server.BindAddress, strconv.Itoa(cfg.Server.HTTPPort))
	manager.Register(api.NewServer(addr, router, monitoring.Module(log, "http"), manager.Fatal))

	if err := manager.Run(ctx); err != nil {
		log.Error().Err(err).Msg("el servidor terminó con errores")
		return exitError
	}
	log.Info().Msg("servidor detenido")
	return exitOK
}
