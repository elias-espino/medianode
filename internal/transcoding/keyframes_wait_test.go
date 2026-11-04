package transcoding

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func writeIndex(t *testing.T, x *Indexer, id string, idx Index) {
	t.Helper()
	data, err := json.Marshal(idx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(x.file(id), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestIndexerWaitDevuelveElIndiceCuandoLlega(t *testing.T) {
	x := NewIndexer(t.TempDir(), "", zerolog.Nop())
	idx := Index{Size: 10, ModifiedAt: "m", DurationUS: 5_000_000, KeyframesUS: []int64{0, 2_000_000}}
	go func() {
		time.Sleep(250 * time.Millisecond)
		writeIndex(t, x, "f", idx)
	}()
	start := time.Now()
	got := x.Wait(context.Background(), "f", "/x.mkv", 10, "m", 5*time.Second)
	if got == nil || len(got.KeyframesUS) != 2 {
		t.Fatalf("índice = %+v", got)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("esperó %s: debía volver en cuanto llegó", d)
	}
}

func TestIndexerWaitSinIndiceVuelveAlPlazo(t *testing.T) {
	x := NewIndexer(t.TempDir(), "", zerolog.Nop())
	start := time.Now()
	if got := x.Wait(context.Background(), "f", "/x.mkv", 10, "m", 300*time.Millisecond); got != nil {
		t.Fatalf("índice = %+v", got)
	}
	if d := time.Since(start); d < 250*time.Millisecond || d > 3*time.Second {
		t.Fatalf("esperó %s", d)
	}
	// La petición quedó en la cola para la siguiente sesión.
	if len(x.queue) != 1 {
		t.Fatalf("cola = %d", len(x.queue))
	}
}

func TestIndexerWaitIgnoraUnIndiceDeOtroArchivo(t *testing.T) {
	x := NewIndexer(t.TempDir(), "", zerolog.Nop())
	writeIndex(t, x, "f", Index{Size: 99, ModifiedAt: "viejo", KeyframesUS: []int64{0}})
	if got := x.Wait(context.Background(), "f", "/x.mkv", 10, "m", 200*time.Millisecond); got != nil {
		t.Fatalf("usó un índice que no corresponde: %+v", got)
	}
}

// El índice solo vale si coinciden el tamaño y la fecha: cada uno por separado.
func TestIndexerGetExigeTamañoYFecha(t *testing.T) {
	x := NewIndexer(t.TempDir(), "", zerolog.Nop())
	writeIndex(t, x, "f", Index{Size: 10, ModifiedAt: "m", KeyframesUS: []int64{0}})
	if x.Get("f", 10, "m") == nil {
		t.Fatal("el índice vigente debía valer")
	}
	if x.Get("f", 11, "m") != nil {
		t.Fatal("otro tamaño: no vale")
	}
	if x.Get("f", 10, "otra") != nil {
		t.Fatal("otra fecha: no vale")
	}
	writeIndex(t, x, "vacio", Index{Size: 10, ModifiedAt: "m"})
	if x.Get("vacio", 10, "m") != nil {
		t.Fatal("un índice sin keyframes no vale")
	}
}

func TestIndexerWaitRespetaElContexto(t *testing.T) {
	x := NewIndexer(t.TempDir(), "", zerolog.Nop())
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	start := time.Now()
	if got := x.Wait(ctx, "f", "/x.mkv", 10, "m", 10*time.Second); got != nil {
		t.Fatal("no debía haber índice")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("no respetó la cancelación")
	}
}
