package api

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// certReloadEvery es cada cuánto se mira si el certificado cambió en disco: una
// renovación (por ejemplo con certbot) no obliga a reiniciar el servidor.
const certReloadEvery = time.Minute

// certReloader sirve el certificado del usuario y lo recarga cuando cambia.
type certReloader struct {
	certFile, keyFile string
	log               zerolog.Logger
	now               func() time.Time

	mu       sync.Mutex
	cert     *tls.Certificate
	loadedAt time.Time
	certMod  time.Time
	keyMod   time.Time
}

func newCertReloader(certFile, keyFile string, log zerolog.Logger) (*certReloader, error) {
	c := &certReloader{certFile: certFile, keyFile: keyFile, log: log, now: time.Now}
	if err := c.load(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *certReloader) load() error {
	cert, err := tls.LoadX509KeyPair(c.certFile, c.keyFile)
	if err != nil {
		return err
	}
	cm, ck := modTime(c.certFile), modTime(c.keyFile)
	c.mu.Lock()
	c.cert, c.loadedAt, c.certMod, c.keyMod = &cert, c.now(), cm, ck
	c.mu.Unlock()
	return nil
}

func modTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// get devuelve el certificado vigente. Si cambió en disco y el nuevo par es
// válido lo sustituye; si no, sigue con el anterior y lo avisa: un
// certificado a medio escribir no debe tumbar el servicio.
func (c *certReloader) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	stale := c.now().Sub(c.loadedAt) >= certReloadEvery
	c.mu.Unlock()
	if stale {
		c.refresh()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cert, nil
}

func (c *certReloader) refresh() {
	cm, ck := modTime(c.certFile), modTime(c.keyFile)
	c.mu.Lock()
	changed := !cm.Equal(c.certMod) || !ck.Equal(c.keyMod)
	c.loadedAt = c.now()
	c.mu.Unlock()
	if !changed {
		return
	}
	if err := c.load(); err != nil {
		c.log.Error().Err(err).Msg("el certificado TLS cambió pero no se pudo cargar: se sigue con el anterior")
		return
	}
	c.log.Info().Msg("certificado TLS recargado")
}

// TLSServer sirve la misma API por HTTPS con el certificado del usuario.
type TLSServer struct {
	addr    string
	srv     *http.Server
	certs   *certReloader
	log     zerolog.Logger
	onFatal func(error)
	ln      net.Listener
}

// NewTLSServer crea el servidor HTTPS. El certificado se carga al crearlo, así
// que un par inválido falla en el arranque.
func NewTLSServer(addr string, handler http.Handler, certFile, keyFile string, log zerolog.Logger, onFatal func(error)) (*TLSServer, error) {
	certs, err := newCertReloader(certFile, keyFile, log)
	if err != nil {
		return nil, fmt.Errorf("cargar el certificado TLS: %w", err)
	}
	return &TLSServer{
		addr: addr, certs: certs, log: log, onFatal: onFatal,
		srv: &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: readHeaderTimeout,
			ReadTimeout:       readTimeout,
			IdleTimeout:       idleTimeout,
			TLSConfig: &tls.Config{
				MinVersion:     tls.VersionTLS12,
				GetCertificate: certs.get,
				NextProtos:     []string{"h2", "http/1.1"},
			},
		},
	}, nil
}

// Name implementa lifecycle.Component.
func (s *TLSServer) Name() string { return "https" }

// Start reserva el puerto de forma síncrona.
func (s *TLSServer) Start(ctx context.Context) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", s.addr)
	if err != nil {
		return fmt.Errorf("escuchar en %s: %w", s.addr, err)
	}
	s.ln = ln
	s.log.Info().Str("addr", ln.Addr().String()).Msg("servidor HTTPS escuchando")
	go func() {
		if err := s.srv.ServeTLS(ln, "", ""); !errors.Is(err, http.ErrServerClosed) {
			s.onFatal(fmt.Errorf("servidor HTTPS: %w", err))
		}
	}()
	return nil
}

// Stop espera a las peticiones en curso y corta las que superen el plazo.
func (s *TLSServer) Stop(ctx context.Context) error {
	if err := s.srv.Shutdown(ctx); err != nil {
		_ = s.srv.Close()
		return err
	}
	return nil
}

// Addr devuelve la dirección real de escucha.
func (s *TLSServer) Addr() string { return s.ln.Addr().String() }
