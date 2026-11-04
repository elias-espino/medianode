// Package routes monta el enrutador HTTP.
package routes

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"

	"medianode/internal/admin"
	"medianode/internal/api"
	"medianode/internal/api/handlers"
	"medianode/internal/api/middleware"
	"medianode/internal/api/websocket"
	"medianode/internal/auth"
	"medianode/internal/authorization"
	"medianode/internal/core"
	"medianode/internal/database"
	"medianode/internal/devices"
	"medianode/internal/events"
	"medianode/internal/ffmpeg"
	"medianode/internal/images"
	"medianode/internal/libraries"
	"medianode/internal/media"
	"medianode/internal/metadata"
	"medianode/internal/monitoring"
	"medianode/internal/playback"
	"medianode/internal/readmodel"
	"medianode/internal/remote"
	"medianode/internal/sessions"
	"medianode/internal/transcoding"
	"medianode/internal/users"
)

// Intentos por minuto y dirección. El de login cubre a un hogar con varios
// clientes; el límite estricto es el de cada cuenta, en auth.
const (
	loginPerMinute   = 20
	setupPerMinute   = 10
	pairingPerMinute = 10
	probePerMinute   = 20
)

// Deps agrupa lo que necesitan las rutas.
type Deps struct {
	Log            zerolog.Logger
	Name           string
	Version        string
	Auth           *auth.Service
	Users          *users.Service
	Devices        *devices.Service
	Libraries      *libraries.Service
	Catalog        *readmodel.Catalog
	Metadata       *metadata.Service
	Enricher       *metadata.Enricher
	Images         *images.Manager
	Sessions       *sessions.Manager
	Progress       *sessions.Progress
	Playback       *playback.Service
	Transcoder     *transcoding.Manager
	FFmpeg         *ffmpeg.Capabilities
	Admin          *admin.Service
	Metrics        *monitoring.Metrics // Opcional: mide la latencia de los endpoints.
	DB             *database.DB
	Bus            *events.Bus
	Authorizer     *authorization.Authorizer
	PathsChanged   func()
	Started        time.Time
	FFprobe        bool
	Web            http.Handler // Panel web; nil sin compilar.
	TrustedProxies []string
	CORSOrigins    []string
	// Remote aplica la política de acceso remoto (§14). Nil: todo se trata como
	// red local, que es lo que necesitan las pruebas de módulos sueltos.
	Remote *remote.Remote
	// DLNA atiende /dlna/*; nil lo desactiva (discovery.dlna_enabled). Se
	// publica sin sesión: un renderer no se autentica, y el propio manejador solo
	// responde a la red local.
	DLNA http.Handler
}

// UPnP añade dos métodos HTTP a los estándar: el enrutador debe conocerlos antes
// de construir ninguna ruta.
func init() {
	chi.RegisterMethod("SUBSCRIBE")
	chi.RegisterMethod("UNSUBSCRIBE")
}

// NewRouter construye el enrutador con sus middlewares.
func NewRouter(deps Deps) http.Handler {
	network := middleware.NewNetwork(deps.TrustedProxies)
	// localAdmin cierra la administración a las peticiones de fuera de la red
	// local; sin módulo remoto no hace nada.
	localAdmin := func(next http.Handler) http.Handler { return next }
	if deps.Remote != nil {
		network = deps.Remote.Net
		localAdmin = deps.Remote.RequireLocalAdmin
	}

	r := chi.NewRouter()

	r.Use(monitoring.WithRequestID)
	if deps.Metrics != nil {
		r.Use(deps.Metrics.Middleware)
	}
	r.Use(middleware.AccessLog(deps.Log))
	r.Use(middleware.Recover(deps.Log))
	if deps.Remote != nil {
		r.Use(deps.Remote.Gate)
	}
	r.Use(middleware.CORS(deps.CORSOrigins))
	r.Use(middleware.LimitBody(middleware.MaxBodyBytes))

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		api.WriteError(w, r, core.ErrNotFound())
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		api.WriteError(w, r, core.ErrMethodNotAllowed())
	})

	server := &handlers.Server{
		Name: deps.Name, Version: deps.Version, Log: deps.Log, SetupRequired: deps.Auth.SetupRequired,
		Started: deps.Started, FFprobeAvailable: deps.FFprobe, Transcoder: deps.Transcoder, Catalog: deps.Catalog, Enricher: deps.Enricher,
		FFmpeg: deps.FFmpeg,
	}
	if deps.Remote != nil {
		server.AdminAvailable = deps.Remote.AdminAllowed
	}
	mediaH := &handlers.Media{Catalog: deps.Catalog, Metadata: deps.Metadata, Images: deps.Images, Log: deps.Log}
	playbackH := &handlers.Playback{Sessions: deps.Sessions, Progress: deps.Progress, Playback: deps.Playback, Network: network, Log: deps.Log}
	adminH := &handlers.Admin{Transcoder: deps.Transcoder, Sessions: deps.Sessions, Service: deps.Admin, Remote: deps.Remote, Log: deps.Log}
	streamH := &handlers.Stream{DB: deps.DB, Sessions: deps.Sessions, Playback: deps.Playback, Transcoder: deps.Transcoder, Log: deps.Log}
	eventsH := &websocket.Handler{
		Bus: deps.Bus, Libs: deps.Authorizer, Authenticate: deps.Auth.Authenticate,
		Origins: deps.CORSOrigins, Log: deps.Log,
	}
	authH := &handlers.Auth{Service: deps.Auth, Network: network, Log: deps.Log}
	usersH := &handlers.Users{Service: deps.Users, Log: deps.Log}
	devicesH := &handlers.Devices{Service: deps.Devices, Log: deps.Log}
	librariesH := &handlers.Libraries{Service: deps.Libraries, Log: deps.Log, PathsChanged: deps.PathsChanged}

	r.Get("/health", server.Health)

	// La credencial es el token de la URL (§16.4); no pasan por Authenticate.
	r.Route("/stream/session/{id}/{token}", func(r chi.Router) {
		r.Get("/file", streamH.File)
		r.Head("/file", streamH.File)
		r.Get("/subtitles/{name}", streamH.Subtitle)
		// HLS: las rutas fijas van antes que el comodín de segmentos.
		r.Get("/master.m3u8", streamH.Master)
		r.Get("/media.m3u8", streamH.Media)
		r.Get("/subs.m3u8", streamH.SubtitlePlaylist)
		r.Get("/subs.vtt", streamH.SubtitleTrack)
		r.Get("/{name}", streamH.Segment)
	})
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/server/info", server.Info)
		r.Get("/server/version", server.ServerVersion)
		r.Get("/server/capabilities", server.Capabilities)

		// Únicas rutas sin sesión: cada una lleva su propia credencial.
		r.With(middleware.RateLimit(auth.NewLimiter(setupPerMinute), network)).Post("/auth/setup", authH.Setup)
		r.With(middleware.RateLimit(auth.NewLimiter(loginPerMinute), network)).Post("/auth/login", authH.Login)
		r.With(middleware.RateLimit(auth.NewLimiter(pairingPerMinute), network)).Post("/auth/pairing", authH.CreatePairing)
		r.Post("/auth/refresh", authH.Refresh)
		r.Post("/auth/pairing/token", authH.PairingToken)
		if deps.Remote != nil {
			// La abre quien está fuera de la red local, sin sesión: la protege un
			// código de un solo uso de 256 bits que caduca.
			r.With(middleware.RateLimit(auth.NewLimiter(probePerMinute), network)).
				Get("/remote/probe/{nonce}", func(w http.ResponseWriter, req *http.Request) {
					deps.Remote.HandleProbe(w, req, chi.URLParam(req, "nonce"))
				})
		}

		r.Group(func(r chi.Router) {
			r.Use(middleware.Authenticate(deps.Auth, deps.Log))
			r.Use(middleware.RequireCSRF)

			r.Post("/auth/logout", authH.Logout)
			r.Get("/auth/me", authH.Me)
			r.Post("/auth/pairing/claim", authH.ClaimPairing)

			r.Get("/users/me/preferences", usersH.GetPreferences)
			r.Put("/users/me/preferences", usersH.PutPreferences)
			r.Put("/users/me/password", authH.ChangePassword)

			r.Get("/devices", devicesH.List)
			r.Get("/devices/{id}", devicesH.Get)
			r.Patch("/devices/{id}", devicesH.Update)
			r.Delete("/devices/{id}", devicesH.Revoke)

			// Cada operación comprueba su permiso sobre la biblioteca: manage
			// permite editarla a quien no es administrador.
			r.Get("/libraries", librariesH.List)
			// Crear, editar y borrar bibliotecas, y sus rutas, decide qué carpetas del
			// servidor se exponen: es administración y, como ella, solo desde la red local.
			r.With(localAdmin).Post("/libraries", librariesH.Create)
			r.Get("/libraries/{id}", librariesH.Get)
			r.With(localAdmin).Patch("/libraries/{id}", librariesH.Update)
			r.With(localAdmin).Delete("/libraries/{id}", librariesH.Delete)
			r.With(localAdmin).Post("/libraries/{id}/paths", librariesH.AddPath)
			r.With(localAdmin).Delete("/libraries/{id}/paths/{pathId}", librariesH.RemovePath)
			r.Post("/libraries/{id}/scan", librariesH.StartScan)
			r.Get("/libraries/{id}/scan", librariesH.ScanStatus)
			r.Get("/libraries/{id}/items", mediaH.ListItems)

			r.Get("/server/status", server.Status)
			r.Get("/search", mediaH.Search)

			// Leer exige ver el elemento; identificar y editar, manage sobre su
			// biblioteca.
			r.Get("/media/{id}", mediaH.Get)
			r.Get("/media/{id}/children", mediaH.Children)
			r.Get("/media/{id}/files", mediaH.Files)
			r.Get("/media/{id}/streams", mediaH.Streams)
			r.Get("/media/{id}/subtitles", mediaH.Subtitles)
			r.Get("/media/{id}/images/{type}", mediaH.Image)
			r.Get("/media/{id}/matches", mediaH.Matches)
			r.Post("/media/{id}/identify", mediaH.Identify)
			r.Post("/media/{id}/refresh", mediaH.Refresh)
			r.Patch("/media/{id}/metadata", mediaH.EditMetadata)

			// Cada sesión solo la ve y la controla su usuario.
			r.Post("/playback/sessions", playbackH.CreateSession)
			r.Get("/playback/sessions/{id}", playbackH.GetSession)
			r.Patch("/playback/sessions/{id}", playbackH.Heartbeat)
			r.Post("/playback/sessions/{id}/tracks", playbackH.ChangeTracks)
			r.Delete("/playback/sessions/{id}", playbackH.StopSession)
			r.Put("/playback/progress", playbackH.PutProgress)
			r.Get("/playback/continue-watching", playbackH.ContinueWatching)
			r.Get("/playback/history", playbackH.History)

			// Valida Origin: solo el propio servidor y cors_origins.
			r.Get("/events", eventsH.ServeHTTP)

			r.Group(func(r chi.Router) {
				r.Use(localAdmin)
				r.Use(middleware.RequireRole(media.RoleAdmin))

				r.Get("/admin/system", adminH.System)
				r.Get("/admin/metrics", adminH.Metrics)
				r.Get("/admin/sessions", adminH.ActiveSessions)
				r.Get("/admin/network", adminH.Network)
				r.Get("/admin/remote", adminH.RemoteReport)
				r.Post("/admin/remote/probe", adminH.StartRemoteProbe)
				r.Get("/admin/storage", adminH.Storage)
				r.Get("/admin/logs", adminH.Logs)
				r.Get("/admin/settings", adminH.Settings)
				r.Get("/admin/system-check", adminH.SystemCheck)
				r.Get("/admin/transcodes", adminH.Transcodes)
				r.Post("/admin/transcodes/{id}/cancel", adminH.CancelTranscode)
				r.Get("/admin/users", usersH.List)
				r.Post("/admin/users", usersH.Create)
				r.Get("/admin/users/{id}", usersH.Get)
				r.Patch("/admin/users/{id}", usersH.Update)
				r.Delete("/admin/users/{id}", usersH.Delete)
				r.Get("/admin/users/{id}/permissions", librariesH.GetPermissions)
				r.Put("/admin/users/{id}/permissions", librariesH.PutPermissions)
			})
		})
	})

	if deps.DLNA != nil {
		r.Handle("/dlna/*", deps.DLNA)
	}

	// El panel ocupa el resto de rutas; las de /api/v1 sin coincidencia ya
	// respondieron 404 en su propio subenrutador.
	if deps.Web != nil {
		panel := deps.Web
		if deps.Remote != nil {
			panel = deps.Remote.GuardPanel(panel)
		}
		r.Handle("/*", panel)
	}

	return r
}
