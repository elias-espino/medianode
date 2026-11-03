package scanner

import (
	"context"
	"sync"
)

// Component integra el escáner y el vigilante en el ciclo de vida del
// servidor.
type Component struct {
	Scanner *Scanner
	Watcher *Watcher

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Name identifica el componente en los logs.
func (c *Component) Name() string { return "scanner" }

// Start marca como interrumpidos los escaneos de una ejecución anterior y
// lanza el vigilante.
func (c *Component) Start(ctx context.Context) error {
	if err := c.Scanner.Recover(ctx); err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	c.cancel = cancel
	c.wg.Go(func() {
		if err := c.Watcher.Run(runCtx); err != nil {
			c.Watcher.log.Error().Err(err).Msg("el vigilante no pudo arrancar; solo habrá reconciliación manual")
		}
	})
	return nil
}

// Stop detiene el vigilante y cancela los escaneos en curso, que quedan como
// interrumpidos.
func (c *Component) Stop(ctx context.Context) error {
	if c.cancel != nil {
		c.cancel()
	}
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		c.Scanner.Close()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
