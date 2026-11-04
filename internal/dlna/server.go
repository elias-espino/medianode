// Package dlna publica las bibliotecas con dlna_enabled como un servidor
// UPnP/DLNA (§13.2): descubrimiento por SSDP, descripción del dispositivo,
// ContentDirectory, ConnectionManager y entrega HTTP de los medios.
//
// Un renderer no se autentica, así que todo lo que se publica lo decide
// libraries.dlna_enabled, y todo responde solo a direcciones de la red local.
package dlna

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"medianode/internal/database"
	"medianode/internal/database/sqlc"
	"medianode/internal/images"
	"medianode/internal/playback"
	"medianode/internal/remote"
	"medianode/internal/sessions"
	"medianode/internal/transcoding"
)

const settingUUID = "dlna.device_uuid"

// Options configura el servidor DLNA.
type Options struct {
	Name          string // Nombre que ve el televisor.
	Version       string
	HTTPPort      int
	BindAddress   string
	Mode          string // playback.DLNAAuto, DLNADirect o DLNATranscode.
	BurnSubtitles bool
	Language      string
	// SSDPPort es el puerto UDP de descubrimiento; 1900 salvo en las pruebas.
	SSDPPort int
}

// Server es el servidor DLNA como componente del ciclo de vida.
type Server struct {
	opts       Options
	db         *database.DB
	playback   *playback.Service
	sessions   *sessions.Manager
	transcoder *transcoding.Manager // nil sin FFmpeg: solo se entregan originales.
	// Images sirve las carátulas; nil las desactiva.
	Images *images.Manager
	net    *remote.Network
	log    zerolog.Logger

	uuid string
	ssdp *ssdp
	// newIO abre el transporte SSDP; se sustituye en las pruebas.
	newIO func(ctx context.Context, port int) (packetIO, error)

	mu    sync.Mutex
	flows map[string]string // IP|archivo → sesión.
}

// New crea el servidor. transcoder puede ser nil.
func New(opts Options, db *database.DB, pb *playback.Service, sm *sessions.Manager, tm *transcoding.Manager, netw *remote.Network, log zerolog.Logger) *Server {
	if opts.SSDPPort == 0 {
		opts.SSDPPort = ssdpPort
	}
	if opts.Mode == "" {
		opts.Mode = playback.DLNAAuto
	}
	return &Server{
		opts: opts, db: db, playback: pb, sessions: sm, transcoder: tm, net: netw, log: log,
		flows: map[string]string{},
		newIO: func(ctx context.Context, port int) (packetIO, error) { return newUDPIO(ctx, port) },
	}
}

// Name implementa lifecycle.Component.
func (s *Server) Name() string { return "dlna" }

// Start empieza a anunciar el servidor. Un fallo de
// SSDP (puerto ocupado, sin red) no impide el resto: el servidor sigue
// alcanzable escribiendo su dirección en el televisor.
// Prepare carga la identidad del dispositivo. Start la llama; se puede usar
// sola para atender las peticiones HTTP sin anunciar nada por SSDP.
func (s *Server) Prepare(ctx context.Context) error {
	id, err := s.deviceUUID(ctx)
	if err != nil {
		return fmt.Errorf("DLNA: identidad del dispositivo: %w", err)
	}
	s.uuid = id
	return nil
}

func (s *Server) Start(ctx context.Context) error {
	if err := s.Prepare(ctx); err != nil {
		return err
	}
	id := s.uuid
	pio, err := s.newIO(ctx, s.opts.SSDPPort)
	if err != nil {
		s.log.Warn().Err(err).Int("puerto", s.opts.SSDPPort).
			Msg("DLNA: no se pudo abrir el puerto SSDP; el servidor no se anunciará en la red")
		return nil
	}
	s.ssdp = newSSDP(pio, id, s.opts.HTTPPort, s.opts.Version, s.net,
		func() []iface { return localInterfaces(s.opts.BindAddress, s.net) }, s.log)
	s.ssdp.start(context.WithoutCancel(ctx))
	s.log.Info().Str("uuid", id).Str("nombre", s.opts.Name).Msg("DLNA: servidor anunciado")
	return nil
}

// Stop avisa de que el servidor se va.
func (s *Server) Stop(context.Context) error {
	if s.ssdp != nil {
		s.ssdp.stop()
	}
	return nil
}

// UUID devuelve la identidad persistente del dispositivo.
func (s *Server) UUID() string { return s.uuid }

// deviceUUID devuelve el UUID del servidor, creándolo la primera vez. Debe
// ser estable: un televisor recuerda los servidores por él.
func (s *Server) deviceUUID(ctx context.Context) (string, error) {
	v, err := s.db.R.GetSetting(ctx, settingUUID)
	if err == nil {
		if _, perr := uuid.Parse(v); perr == nil {
			return v, nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id := uuid.NewString()
	if err := s.db.Q.SetSetting(ctx, sqlc.SetSettingParams{Key: settingUUID, Value: id}); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Server) planOptions(decideOnly bool) playback.DLNAOptions {
	return playback.DLNAOptions{
		Mode: s.opts.Mode, BurnSubtitles: s.opts.BurnSubtitles, Language: s.opts.Language, DecideOnly: decideOnly,
	}
}

// reHost admite lo que puede llevar una cabecera Host de una red local: un
// nombre, una IP (también IPv6 entre corchetes) y un puerto.
var reHost = regexp.MustCompile(`^[A-Za-z0-9.\-]+(:\d{1,5})?$|^\[[0-9A-Fa-f:.]+\](:\d{1,5})?$`)

// hostOf devuelve la dirección por la que el renderer llegó: es la que ha de
// usar en las URL que le damos. Una cabecera rara se sustituye por la de la
// interfaz de la red del cliente, para no devolver nunca texto ajeno.
func (s *Server) hostOf(r *http.Request) string {
	if reHost.MatchString(r.Host) {
		return r.Host
	}
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil && s.ssdp != nil {
		if via, ok := s.ssdp.interfaceFor(ap.Addr().Unmap()); ok {
			return fmt.Sprintf("%s:%d", via.IP, s.opts.HTTPPort)
		}
	}
	return ":" + strconv.Itoa(s.opts.HTTPPort)
}

// Handler atiende /dlna/*. Solo responde a la red local: lo demás es un 404,
// como si el servicio no existiera, aunque el acceso remoto esté activo.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /dlna/device.xml", s.device)
	mux.HandleFunc("GET /dlna/cds.xml", xmlHandler(cdsSCPD))
	mux.HandleFunc("GET /dlna/cms.xml", xmlHandler(cmsSCPD))
	mux.HandleFunc("POST /dlna/cds/control", s.control(cdsTypeURN, s.cdsActions()))
	mux.HandleFunc("POST /dlna/cms/control", s.control(cmsTypeURN, s.cmsActions()))
	for _, svc := range []string{"cds", "cms"} {
		mux.HandleFunc("SUBSCRIBE /dlna/"+svc+"/event", s.subscribe)
		mux.HandleFunc("UNSUBSCRIBE /dlna/"+svc+"/event", s.unsubscribe)
	}
	mux.HandleFunc("GET /dlna/media/{file}/{name}", s.serveDirect)
	mux.HandleFunc("HEAD /dlna/media/{file}/{name}", s.serveDirect)
	mux.HandleFunc("GET /dlna/ts/{file}/{name}", s.serveTS)
	mux.HandleFunc("HEAD /dlna/ts/{file}/{name}", s.serveTS)
	mux.HandleFunc("GET /dlna/art/{name}", s.serveArt)
	mux.HandleFunc("HEAD /dlna/art/{name}", s.serveArt)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.net.Classify(r) != remote.OriginLocal {
			http.NotFound(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func xmlHandler(body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { writeXML(w, http.StatusOK, body) }
}

func (s *Server) device(w http.ResponseWriter, _ *http.Request) {
	writeXML(w, http.StatusOK, deviceDescription(s.opts.Name, s.opts.Version, s.uuid))
}

// --- Control SOAP ---

type actionFunc func(ctx context.Context, r *http.Request, args map[string]string) ([]pair, *upnpError)

func (s *Server) control(service string, actions map[string]actionFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := parseSOAP(http.MaxBytesReader(w, r.Body, maxSOAPBody))
		if err != nil {
			writeXML(w, http.StatusBadRequest, soapFault(&upnpError{errInvalidArgs, "Invalid Args"}))
			return
		}
		// El servicio de la cabecera, si viene, ha de ser el del endpoint.
		if hs, ha := soapActionName(r.Header.Get("SOAPACTION")); hs != "" && (hs != service || (ha != "" && ha != c.action)) {
			writeXML(w, http.StatusInternalServerError, soapFault(&upnpError{errInvalidAction, "Invalid Action"}))
			return
		}
		fn, ok := actions[c.action]
		if c.service != service || !ok {
			writeXML(w, http.StatusInternalServerError, soapFault(&upnpError{errInvalidAction, "Invalid Action"}))
			return
		}
		out, uerr := fn(r.Context(), r, c.args)
		if uerr != nil {
			writeXML(w, http.StatusInternalServerError, soapFault(uerr))
			return
		}
		writeXML(w, http.StatusOK, soapResponse(service, c.action, out))
	}
}

func (s *Server) cdsActions() map[string]actionFunc {
	return map[string]actionFunc{
		"GetSearchCapabilities": func(context.Context, *http.Request, map[string]string) ([]pair, *upnpError) {
			return []pair{{"SearchCaps", ""}}, nil
		},
		"GetSortCapabilities": func(context.Context, *http.Request, map[string]string) ([]pair, *upnpError) {
			return []pair{{"SortCaps", "dc:title"}}, nil
		},
		"GetSystemUpdateID": func(ctx context.Context, _ *http.Request, _ map[string]string) ([]pair, *upnpError) {
			return []pair{{"Id", strconv.FormatUint(uint64(s.updateID(ctx)), 10)}}, nil
		},
		"Browse": s.actionBrowse,
	}
}

func (s *Server) actionBrowse(ctx context.Context, r *http.Request, a map[string]string) ([]pair, *upnpError) {
	bad := &upnpError{errInvalidArgs, "Invalid Args"}
	req := browseRequest{objectID: a["ObjectID"], host: s.hostOf(r), desc: parseSort(a["SortCriteria"])}
	switch a["BrowseFlag"] {
	case "BrowseMetadata":
		req.metadata = true
	case "BrowseDirectChildren":
	default:
		return nil, bad
	}
	var ok1, ok2 bool
	if req.start, ok1 = parseIndex(a["StartingIndex"], 0); !ok1 {
		return nil, bad
	}
	if req.count, ok2 = parseIndex(a["RequestedCount"], 0); !ok2 {
		return nil, bad
	}
	res, uerr := s.browse(ctx, req)
	if uerr != nil {
		return nil, uerr
	}
	return []pair{
		{"Result", res.didl}, {"NumberReturned", strconv.Itoa(res.returned)},
		{"TotalMatches", strconv.FormatInt(res.total, 10)}, {"UpdateID", strconv.FormatUint(uint64(res.updateID), 10)},
	}, nil
}

// sourceProtocols son los formatos que se pueden ofrecer.
var sourceProtocols = []string{
	"http-get:*:video/mp4:*", "http-get:*:video/mpeg:*", "http-get:*:video/x-matroska:*",
	"http-get:*:video/x-msvideo:*", "http-get:*:video/quicktime:*", "http-get:*:video/webm:*", "http-get:*:image/jpeg:*",
}

func (s *Server) cmsActions() map[string]actionFunc {
	return map[string]actionFunc{
		"GetProtocolInfo": func(context.Context, *http.Request, map[string]string) ([]pair, *upnpError) {
			return []pair{{"Source", strings.Join(sourceProtocols, ",")}, {"Sink", ""}}, nil
		},
		"GetCurrentConnectionIDs": func(context.Context, *http.Request, map[string]string) ([]pair, *upnpError) {
			return []pair{{"ConnectionIDs", "0"}}, nil
		},
		"GetCurrentConnectionInfo": func(_ context.Context, _ *http.Request, a map[string]string) ([]pair, *upnpError) {
			if strings.TrimSpace(a["ConnectionID"]) != "0" {
				return nil, &upnpError{706, "Invalid connection reference"}
			}
			return []pair{
				{"RcsID", "-1"}, {"AVTransportID", "-1"}, {"ProtocolInfo", ""}, {"PeerConnectionManager", ""},
				{"PeerConnectionID", "-1"}, {"Direction", "Output"}, {"Status", "OK"},
			}, nil
		},
	}
}

// --- Eventos ---

// subscribe acepta la suscripción (UDA 1.0 §4.1.1) pero no envía eventos: la
// lista de bibliotecas cambia poco, y el cliente que quiera novedades vuelve a
// navegar. No se llama a ninguna URL de CALLBACK: no hay nada que enviar, y
// así un cliente no puede hacer que el servidor conecte a donde quiera.
func (s *Server) subscribe(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("SID") == "" && r.Header.Get("CALLBACK") == "" {
		http.Error(w, "falta CALLBACK", http.StatusPreconditionFailed)
		return
	}
	sid := r.Header.Get("SID")
	if sid == "" {
		sid = "uuid:" + uuid.NewString()
	}
	w.Header().Set("SID", sid)
	w.Header().Set("TIMEOUT", "Second-"+strconv.Itoa(ssdpMaxAge))
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusOK)
}

func (s *Server) unsubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("SID") == "" {
		http.Error(w, "falta SID", http.StatusPreconditionFailed)
		return
	}
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusOK)
}

// touchEvery es cada cuánto se renueva la actividad de una sesión mientras
// hay una respuesta abierta: un televisor en pausa deja de leer, y la sesión no
// debe caducar por eso.
const touchEvery = 10 * time.Second
