// Package lifecycle ordena el arranque y el apagado de los componentes.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog"
)

// ErrStopTimeout indica que un componente no terminó dentro de su plazo.
var ErrStopTimeout = errors.New("el componente no se detuvo a tiempo")

// Component es una pieza del servidor con ciclo de vida. Start no debe
// bloquear: el trabajo de fondo corre en sus propias goroutines.
type Component interface {
	Name() string
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// Manager arranca los componentes en orden y los detiene en orden inverso.
type Manager struct {
	components []Component
	timeout    time.Duration
	log        zerolog.Logger
	fatal      chan error
}

// New crea un Manager. timeout es el plazo total de apagado.
func New(timeout time.Duration, log zerolog.Logger) *Manager {
	return &Manager{timeout: timeout, log: log, fatal: make(chan error, 1)}
}

// Register añade un componente. Debe llamarse antes de Run.
func (m *Manager) Register(c Component) {
	m.components = append(m.components, c)
}

// Fatal pide el apagado por un fallo de un componente en ejecución. Solo se
// conserva el primer error.
func (m *Manager) Fatal(err error) {
	select {
	case m.fatal <- err:
	default:
	}
}

// Run arranca todo y espera a que se cancele ctx o llegue un fallo fatal. La
// cancelación de ctx es un apagado normal y no se devuelve como error.
func (m *Manager) Run(ctx context.Context) error {
	for i, c := range m.components {
		m.log.Info().Str("component", c.Name()).Msg("iniciando componente")
		if err := c.Start(ctx); err != nil {
			startErr := fmt.Errorf("iniciar %s: %w", c.Name(), err)
			return errors.Join(startErr, m.stop(m.components[:i]))
		}
	}

	var runErr error
	select {
	case <-ctx.Done():
		m.log.Info().Msg("apagado solicitado")
	case runErr = <-m.fatal:
		m.log.Error().Err(runErr).Msg("fallo fatal, apagando")
	}
	return errors.Join(runErr, m.stop(m.components))
}

// stop reparte el tiempo restante entre los componentes pendientes, de modo
// que uno bloqueado no deje a los demás sin plazo para terminar.
func (m *Manager) stop(components []Component) error {
	var errs []error
	deadline := time.Now().Add(m.timeout)

	for i := len(components) - 1; i >= 0; i-- {
		c := components[i]
		share := time.Until(deadline) / time.Duration(i+1)
		ctx, cancel := context.WithTimeout(context.Background(), share)

		done := make(chan error, 1)
		go func() { done <- c.Stop(ctx) }()

		select {
		case err := <-done:
			if err != nil {
				errs = append(errs, fmt.Errorf("detener %s: %w", c.Name(), err))
			}
		case <-ctx.Done():
			m.log.Error().Str("component", c.Name()).Msg("el componente no se detuvo a tiempo")
			errs = append(errs, fmt.Errorf("detener %s: %w", c.Name(), ErrStopTimeout))
		}
		cancel()
	}
	return errors.Join(errs...)
}
