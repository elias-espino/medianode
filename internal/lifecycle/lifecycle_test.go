package lifecycle

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(e string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

type fake struct {
	name     string
	rec      *recorder
	startErr error
	block    bool
}

func (f *fake) Name() string { return f.name }

func (f *fake) Start(context.Context) error {
	f.rec.add("start " + f.name)
	return f.startErr
}

func (f *fake) Stop(ctx context.Context) error {
	f.rec.add("stop " + f.name)
	if f.block {
		select {}
	}
	return nil
}

func newManager(timeout time.Duration) *Manager {
	return New(timeout, zerolog.Nop())
}

func TestRunArrancaEnOrdenYParaEnInverso(t *testing.T) {
	rec := &recorder{}
	m := newManager(time.Second)
	m.Register(&fake{name: "a", rec: rec})
	m.Register(&fake{name: "b", rec: rec})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)
	cancel()

	if err := <-done; err != nil {
		t.Fatalf("un apagado normal no debe devolver error: %v", err)
	}
	want := []string{"start a", "start b", "stop b", "stop a"}
	if got := rec.snapshot(); !slices.Equal(got, want) {
		t.Fatalf("eventos = %v, se esperaba %v", got, want)
	}
}

func TestRunFalloAlArrancarDetieneLoIniciado(t *testing.T) {
	rec := &recorder{}
	boom := errors.New("boom")
	m := newManager(time.Second)
	m.Register(&fake{name: "a", rec: rec})
	m.Register(&fake{name: "b", rec: rec, startErr: boom})
	m.Register(&fake{name: "c", rec: rec})

	err := m.Run(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v", err)
	}
	want := []string{"start a", "start b", "stop a"}
	if got := rec.snapshot(); !slices.Equal(got, want) {
		t.Fatalf("eventos = %v, se esperaba %v", got, want)
	}
}

func TestComponenteBloqueadoNoImpideElApagado(t *testing.T) {
	rec := &recorder{}
	m := newManager(200 * time.Millisecond)
	m.Register(&fake{name: "a", rec: rec})
	m.Register(&fake{name: "lento", rec: rec, block: true})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := m.Run(ctx)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("el apagado tardó %v", elapsed)
	}
	if !errors.Is(err, ErrStopTimeout) {
		t.Fatalf("error = %v", err)
	}
	if !slices.Contains(rec.snapshot(), "stop a") {
		t.Fatal("el componente anterior debe detenerse aunque otro se bloquee")
	}
}

func TestFatalApagaYDevuelveElError(t *testing.T) {
	rec := &recorder{}
	boom := errors.New("boom")
	m := newManager(time.Second)
	m.Register(&fake{name: "a", rec: rec})
	m.Fatal(boom)
	m.Fatal(errors.New("segundo error ignorado"))

	if err := m.Run(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("error = %v", err)
	}
	if !slices.Contains(rec.snapshot(), "stop a") {
		t.Fatal("debe detener los componentes")
	}
}
