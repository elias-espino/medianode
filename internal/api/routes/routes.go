// Package routes monta el enrutador HTTP.
package routes

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"

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
	"medianode/internal/images"
	"medianode/internal/libraries"
	"medianode/internal/media"
	"medianode/internal/metadata"
	"medianode/internal/monitoring"
	"medianode/internal/playback"
	"medianode/internal/readmodel"
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
	DB             *database.DB
	Bus            *events.Bus
	Authorizer     *authorization.Authorizer
	PathsChanged   func()
	Started        time.Time
	FFprobe        bool
	Web            http.Handler // Panel web; nil sin compilar.
	TrustedProxies []string
	CORSOrigins    []string
}

// NewRouter construye el enrutador con sus middlewares.
func NewRouter(deps Deps) http.Handler {
	network := middleware.NewNetwork(deps.TrustedProxies)

	r := chi.NewRouter()

	r.Use(monitoring.WithRequestID)
	r.Use(middleware.AccessLog(deps.Log))
	r.Use(middleware.Recover(deps.Log))
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
	}
	mediaH := &handlers.Media{Catalog: deps.Catalog, Metadata: deps.Metadata, Images: deps.Images, Log: deps.Log}
	playbackH := &handlers.Playback{Sessions: deps.Sessions, Progress: deps.Progress, Playback: deps.Playback, Log: deps.Log}
	adminH := &handlers.Admin{Transcoder: deps.Transcoder, Sessions: deps.Sessions, Log: deps.Log}
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
			r.Post("/libraries", librariesH.Create)
			r.Get("/libraries/{id}", librariesH.Get)
			r.Patch("/libraries/{id}", librariesH.Update)
			r.Delete("/libraries/{id}", librariesH.Delete)
			r.Post("/libraries/{id}/paths", librariesH.AddPath)
			r.Delete("/libraries/{id}/paths/{pathId}", librariesH.RemovePath)
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
			r.Delete("/playback/sessions/{id}", playbackH.StopSession)
			r.Put("/playback/progress", playbackH.PutProgress)
			r.Get("/playback/continue-watching", playbackH.ContinueWatching)
			r.Get("/playback/history", playbackH.History)

			// Valida Origin: solo el propio servidor y cors_origins.
			r.Get("/events", eventsH.ServeHTTP)

			r.Group(func(r chi.Router) {
				r.Use(middleware.RequireRole(media.RoleAdmin))

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

	// El panel ocupa el resto de rutas; las de /api/v1 sin coincidencia ya
	// respondieron 404 en su propio subenrutador.
	if deps.Web != nil {
		r.Handle("/*", deps.Web)
	}

	return r
}
