package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/rs/zerolog"
)

// No hay WriteTimeout: el streaming mantiene respuestas abiertas durante horas.
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 60 * time.Second
	idleTimeout       = 120 * time.Second
)

// Server es el servidor HTTP como componente del ciclo de vida.
type Server struct {
	addr    string
	srv     *http.Server
	log     zerolog.Logger
	onFatal func(error)
	ln      net.Listener
}

// NewServer crea el servidor. onFatal recibe los fallos posteriores al arranque.
func NewServer(addr string, handler http.Handler, log zerolog.Logger, onFatal func(error)) *Server {
	return &Server{
		addr:    addr,
		log:     log,
		onFatal: onFatal,
		srv: &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: readHeaderTimeout,
			ReadTimeout:       readTimeout,
			IdleTimeout:       idleTimeout,
		},
	}
}

// Name implementa lifecycle.Component.
func (s *Server) Name() string { return "http" }

// Start reserva el puerto de forma síncrona para que un puerto ocupado falle
// en el arranque y no después.
func (s *Server) Start(ctx context.Context) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", s.addr)
	if err != nil {
		return fmt.Errorf("escuchar en %s: %w", s.addr, err)
	}
	s.ln = ln
	s.log.Info().Str("addr", ln.Addr().String()).Msg("servidor HTTP escuchando")

	go func() {
		if err := s.srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			s.onFatal(fmt.Errorf("servidor HTTP: %w", err))
		}
	}()
	return nil
}

// Stop espera a las peticiones en curso y corta las que superen el plazo.
func (s *Server) Stop(ctx context.Context) error {
	if err := s.srv.Shutdown(ctx); err != nil {
		_ = s.srv.Close()
		return err
	}
	return nil
}

// Addr devuelve la dirección real de escucha.
func (s *Server) Addr() string {
	return s.ln.Addr().String()
}
