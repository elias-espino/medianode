package subtitles

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"medianode/internal/ffmpeg"
)

// stub escribe el archivo de salida y cuenta las llamadas.
type stub struct {
	calls atomic.Int32
	delay time.Duration
	mu    sync.Mutex
	fail  error
}

func (s *stub) setFail(err error) {
	s.mu.Lock()
	s.fail = err
	s.mu.Unlock()
}

func (s *stub) extract(_ context.Context, _, _ string, _ int, _, out string) error {
	s.calls.Add(1)
	time.Sleep(s.delay)
	s.mu.Lock()
	fail := s.fail
	s.mu.Unlock()
	if fail != nil {
		return fail
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
		return err
	}
	return os.WriteFile(out, []byte("WEBVTT\n"), 0o600)
}

func newTestManager(t *testing.T, s *stub) *Manager {
	t.Helper()
	m := NewManager("ffmpeg", t.TempDir(), zerolog.Nop())
	m.extract = s.extract
	return m
}

func track() Track {
	return Track{FileID: "f1", TrackID: "t1", Input: "/v.mkv", Stream: 2, Size: 10, ModifiedAt: "m1"}
}

func TestGetExtraeUnaVezYLuegoUsaLaCache(t *testing.T) {
	s := &stub{}
	m := newTestManager(t, s)
	p, err := m.Get(context.Background(), track(), ffmpeg.FormatWebVTT)
	if err != nil || !strings.HasSuffix(p, filepath.Join("f1", "t1.vtt")) {
		t.Fatalf("%q %v", p, err)
	}
	if _, err := m.Get(context.Background(), track(), ffmpeg.FormatWebVTT); err != nil {
		t.Fatal(err)
	}
	if s.calls.Load() != 1 {
		t.Fatalf("extracciones = %d, debía ser 1", s.calls.Load())
	}
	// ASS es otra copia, con su propia extensión.
	p, err = m.Get(context.Background(), track(), ffmpeg.FormatASS)
	if err != nil || !strings.HasSuffix(p, "t1.ass") || s.calls.Load() != 2 {
		t.Fatalf("%q %v %d", p, err, s.calls.Load())
	}
}

func TestGetPeticionesConcurrentesComparteUnaExtraccion(t *testing.T) {
	s := &stub{delay: 300 * time.Millisecond}
	m := newTestManager(t, s)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := m.Get(context.Background(), track(), ffmpeg.FormatWebVTT); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if s.calls.Load() != 1 {
		t.Fatalf("extracciones = %d, debía ser 1", s.calls.Load())
	}
}

func TestCambioDeTamañoOFechaInvalidaLaCache(t *testing.T) {
	s := &stub{}
	m := newTestManager(t, s)
	if _, err := m.Get(context.Background(), track(), ffmpeg.FormatWebVTT); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Track){
		"tamaño": func(x *Track) { x.Size = 11 },
		"fecha":  func(x *Track) { x.ModifiedAt = "m2" },
	} {
		tr := track()
		mut(&tr)
		if _, ok := m.Cached(tr, ffmpeg.FormatWebVTT); ok {
			t.Fatalf("%s: la copia no debía valer", name)
		}
	}
	tr := track()
	tr.Size = 11
	before := s.calls.Load()
	if _, err := m.Get(context.Background(), tr, ffmpeg.FormatWebVTT); err != nil || s.calls.Load() != before+1 {
		t.Fatalf("debía extraer otra vez: %v %d", err, s.calls.Load())
	}
	if _, ok := m.Cached(tr, ffmpeg.FormatWebVTT); !ok {
		t.Fatal("la nueva copia debía valer")
	}
}

func TestUnFalloNoSeGuardaYSePuedeReintentar(t *testing.T) {
	s := &stub{}
	s.setFail(errors.New("boom"))
	m := newTestManager(t, s)
	if _, err := m.Get(context.Background(), track(), ffmpeg.FormatWebVTT); err == nil {
		t.Fatal("debía fallar")
	}
	if _, ok := m.Cached(track(), ffmpeg.FormatWebVTT); ok {
		t.Fatal("un fallo no se guarda")
	}
	s.setFail(nil)
	if _, err := m.Get(context.Background(), track(), ffmpeg.FormatWebVTT); err != nil {
		t.Fatalf("el reintento debía funcionar: %v", err)
	}
	if s.calls.Load() != 2 {
		t.Fatalf("extracciones = %d", s.calls.Load())
	}
}

// Si el cliente se va, la extracción sigue y la siguiente petición la encuentra.
func TestCancelarLaPeticionNoCancelaLaExtraccion(t *testing.T) {
	s := &stub{delay: 300 * time.Millisecond}
	m := newTestManager(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := m.Get(ctx, track(), ffmpeg.FormatWebVTT); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	waitCached(t, m)
	if s.calls.Load() != 1 {
		t.Fatalf("extracciones = %d", s.calls.Load())
	}
}

func waitCached(t *testing.T, m *Manager) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := m.Cached(track(), ffmpeg.FormatWebVTT); ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("la extracción no terminó")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestIdentificadoresYFormatosNoValidos(t *testing.T) {
	m := newTestManager(t, &stub{})
	for name, tr := range map[string]Track{
		"archivo con ruta": {FileID: "../x", TrackID: "t"},
		"pista con ruta":   {FileID: "f", TrackID: "a/b"},
		"pista vacía":      {FileID: "f", TrackID: ""},
		"pista es ..":      {FileID: "f", TrackID: ".."},
	} {
		if _, err := m.Get(context.Background(), tr, ffmpeg.FormatWebVTT); err == nil {
			t.Errorf("%s: debía fallar", name)
		}
	}
	if _, err := m.Get(context.Background(), track(), "srt"); err == nil {
		t.Error("formato no admitido")
	}
}

func TestSinFFmpegDevuelveErrNoExtractor(t *testing.T) {
	m := NewManager("", t.TempDir(), zerolog.Nop())
	if _, err := m.Get(context.Background(), track(), ffmpeg.FormatWebVTT); !errors.Is(err, ErrNoExtractor) {
		t.Fatalf("err = %v", err)
	}
	m.Prefetch(track(), ffmpeg.FormatWebVTT) // No debe entrar en pánico ni colgarse.
}

func TestPrefetchDejaLaCopiaLista(t *testing.T) {
	s := &stub{}
	m := newTestManager(t, s)
	m.Prefetch(track(), ffmpeg.FormatWebVTT)
	waitCached(t, m)
}

// Con FFmpeg de verdad y un MKV con SRT interno.
func TestManagerConFFmpegReal(t *testing.T) {
	if testing.Short() {
		t.Skip("necesita FFmpeg")
	}
	ff, err := ffmpeg.Locate()
	if err != nil {
		t.Skip("FFmpeg no está instalado")
	}
	src, _ := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "mkv_srt_internal.mkv"))
	info, err := os.Stat(src)
	if err != nil {
		t.Skip("falta el fixture")
	}
	m := NewManager(ff, t.TempDir(), zerolog.Nop())
	tr := Track{FileID: "f", TrackID: "t", Input: src, Stream: 2, Size: info.Size(), ModifiedAt: info.ModTime().String()}
	p, err := m.Get(context.Background(), tr, ffmpeg.FormatWebVTT)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(data), "WEBVTT") {
		t.Fatalf("%s", data)
	}
}
