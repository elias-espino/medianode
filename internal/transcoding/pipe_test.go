package transcoding

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"medianode/internal/events"
	"medianode/internal/ffmpeg"
	"medianode/internal/testutil"
)

// pipeLogger calla, salvo con PIPE_DEBUG: para ver qué dice FFmpeg al fallar.
func pipeLogger() zerolog.Logger {
	if os.Getenv("PIPE_DEBUG") != "" {
		return zerolog.New(os.Stderr)
	}
	return zerolog.Nop()
}

// pipeManager crea un gestor con los cupos indicados.
func pipeManager(t *testing.T, tl tools, heavy, light int, enc ffmpeg.Encoder) *Manager {
	t.Helper()
	m := NewManager(Options{FFmpeg: tl.ffmpeg, Dir: t.TempDir(), SegmentSeconds: 6, Encoder: enc},
		testutil.NewDB(t), events.NewBus(), NewAdmission(heavy, light), pipeLogger())
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })
	return m
}

// probeTS devuelve el códec de vídeo, el alto y la duración de un flujo TS.
func probeTS(t *testing.T, tl tools, data []byte) (codec string, height int, duration float64, audio string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "salida.ts")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		out, err := exec.Command(tl.ffprobe, append([]string{"-v", "error"}, args...)...).Output()
		if err != nil {
			t.Fatalf("ffprobe %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	first := func(s string) string { return strings.TrimSpace(strings.SplitN(s, "\n", 2)[0]) }
	v := strings.Split(first(run("-select_streams", "v:0", "-show_entries", "stream=codec_name,height", "-of", "csv=p=0", path)), ",")
	if len(v) != 2 {
		t.Fatalf("pista de vídeo: %v", v)
	}
	h, _ := strconv.Atoi(v[1])
	d, _ := strconv.ParseFloat(first(run("-show_entries", "format=duration", "-of", "csv=p=0", path)), 64)
	return v[0], h, d, first(run("-select_streams", "a:0", "-show_entries", "stream=codec_name", "-of", "csv=p=0", path))
}

func checkSync(t *testing.T, data []byte) {
	t.Helper()
	if len(data) == 0 || len(data)%188 != 0 {
		t.Fatalf("%d bytes no son paquetes TS completos", len(data))
	}
	for i := 0; i < len(data); i += 188 {
		if data[i] != 0x47 {
			t.Fatalf("el paquete %d no está sincronizado: %#x", i/188, data[i])
		}
	}
}

func basePlan(path, session string) PipePlan {
	return PipePlan{SessionID: session, FileID: "f", Path: path, DurationUS: 60_000_000, Kind: Light, VideoIndex: 0, AudioIndex: 1, AudioCopy: true}
}

func TestStreamTSCopiandoElVideo(t *testing.T) {
	tl := needTools(t)
	src := sharedIrregular(t, tl)
	m := pipeManager(t, tl, 1, 1, "")

	var out bytes.Buffer
	n, err := m.StreamTS(context.Background(), basePlan(src, "s1"), &out)
	if err != nil || n != int64(out.Len()) || n == 0 {
		t.Fatalf("n=%d len=%d err=%v", n, out.Len(), err)
	}
	checkSync(t, out.Bytes())
	codec, _, dur, audio := probeTS(t, tl, out.Bytes())
	if codec != "h264" || audio != "aac" || !near(dur, 60, 1.5) {
		t.Fatalf("codec=%s audio=%s duración=%.2f", codec, audio, dur)
	}
	if len(m.Jobs()) != 0 {
		t.Fatalf("quedan trabajos: %+v", m.Jobs())
	}
	if used, _ := m.Admission().Usage(); used[Light] != 0 || used[Heavy] != 0 {
		t.Fatalf("cupo sin liberar: %v", used)
	}
	if m.Failures() != 0 {
		t.Fatalf("fallos = %d", m.Failures())
	}
	// No quedan directorios de trabajo.
	entries, _ := os.ReadDir(m.Dir())
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), pipeDirPrefix) {
			t.Errorf("directorio sin borrar: %s", e.Name())
		}
	}
}

func TestStreamTSRecodificandoYEscalando(t *testing.T) {
	tl := needTools(t)
	src := sharedIrregular(t, tl)
	m := pipeManager(t, tl, 1, 1, "")
	p := basePlan(src, "s1")
	p.Kind, p.AudioCopy, p.Height, p.VideoBitrate = Heavy, false, 144, 300_000
	p.StartMS = 20_000 // Recodificar salta con precisión.
	var out bytes.Buffer
	if _, err := m.StreamTS(context.Background(), p, &out); err != nil {
		t.Fatal(err)
	}
	checkSync(t, out.Bytes())
	codec, h, dur, audio := probeTS(t, tl, out.Bytes())
	if codec != "h264" || h != 144 || audio != "aac" || !near(dur, 40, 1.5) {
		t.Fatalf("codec=%s alto=%d audio=%s duración=%.2f", codec, h, audio, dur)
	}
}

func TestStreamTSConSaltoCopiando(t *testing.T) {
	tl := needTools(t)
	src := sharedIrregular(t, tl)
	m := pipeManager(t, tl, 1, 1, "")
	p := basePlan(src, "s1")
	p.StartMS = 40_000
	var out bytes.Buffer
	if _, err := m.StreamTS(context.Background(), p, &out); err != nil {
		t.Fatal(err)
	}
	// Al copiar, el salto cae en el keyframe anterior (36,5 s): quedan entre 20 y 24 s.
	if _, _, dur, _ := probeTS(t, tl, out.Bytes()); dur < 19 || dur > 25 {
		t.Fatalf("duración = %.2f", dur)
	}
}

func TestStreamTSGrabandoUnSubtitulo(t *testing.T) {
	tl := needTools(t)
	src := sharedIrregular(t, tl)
	sub := filepath.Join(t.TempDir(), "sub.ass")
	if err := os.WriteFile(sub, []byte("[Script Info]\nScriptType: v4.00+\nPlayResX: 320\nPlayResY: 180\n\n[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\nStyle: Default,Arial,20,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,1,0,2,10,10,10,1\n\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\nDialogue: 0,0:00:01.00,0:00:05.00,Default,,0,0,0,,Hola\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := pipeManager(t, tl, 1, 1, "")
	p := basePlan(src, "s1")
	p.Kind, p.AudioCopy, p.VideoBitrate = Heavy, false, 300_000
	p.Burn, p.BurnFile = ffmpeg.BurnText, sub
	p.DurationUS = 5_000_000
	// Con una escritura que corta tras unos paquetes basta: FFmpeg ya aplicó el filtro.
	var out bytes.Buffer
	w := &limitWriter{w: &out, max: 188 * 400}
	if _, err := m.StreamTS(context.Background(), p, w); err != nil {
		t.Fatal(err)
	}
	checkSync(t, out.Bytes()[:out.Len()/188*188])
	if used, _ := m.Admission().Usage(); used[Heavy] != 0 {
		t.Fatalf("cupo sin liberar: %v", used)
	}
}

// limitWriter falla al llegar a max bytes, como un cliente que cierra.
type limitWriter struct {
	w   *bytes.Buffer
	max int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.w.Len()+len(p) > l.max {
		return 0, errors.New("conexión cerrada")
	}
	return l.w.Write(p)
}

func TestStreamTSSeParaCuandoElClienteSeVa(t *testing.T) {
	tl := needTools(t)
	src := sharedIrregular(t, tl)
	m := pipeManager(t, tl, 1, 1, "")
	p := basePlan(src, "s1")
	p.Kind, p.AudioCopy, p.VideoBitrate = Heavy, false, 300_000 // Más lento que copiar.

	var out bytes.Buffer
	start := time.Now()
	n, err := m.StreamTS(context.Background(), p, &limitWriter{w: &out, max: 188 * 50})
	if err != nil {
		t.Fatalf("que el cliente se vaya no es un fallo: %v", err)
	}
	if time.Since(start) > 10*time.Second || n == 0 {
		t.Fatalf("tardó %v, n=%d", time.Since(start), n)
	}
	if len(m.Jobs()) != 0 {
		t.Fatalf("quedan trabajos: %+v", m.Jobs())
	}
	if m.Failures() != 0 {
		t.Fatalf("un cliente que se va no cuenta como fallo: %d", m.Failures())
	}
}

func TestStreamTSSeParaConElContexto(t *testing.T) {
	tl := needTools(t)
	src := sharedIrregular(t, tl)
	m := pipeManager(t, tl, 1, 1, "")
	p := basePlan(src, "s1")
	p.Kind, p.AudioCopy, p.VideoBitrate = Heavy, false, 300_000
	ctx, cancel := context.WithCancel(context.Background())
	gw := &gateWriter{first: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() { _, err := m.StreamTS(ctx, p, gw); done <- err }()
	<-gw.first
	cancel()
	close(gw.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cancelar no es un fallo: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no se paró")
	}
}

// gateWriter avisa de la primera escritura y espera a que se la libere: un
// televisor en pausa que no lee.
type gateWriter struct {
	once    sync.Once
	first   chan struct{}
	release chan struct{}
}

func (g *gateWriter) Write(p []byte) (int, error) {
	g.once.Do(func() { close(g.first) })
	<-g.release
	return len(p), nil
}

func TestElCupoPerteneceALaSesionHastaElUltimoFlujo(t *testing.T) {
	tl := needTools(t)
	src := sharedIrregular(t, tl)
	m := pipeManager(t, tl, 1, 1, "")
	p := basePlan(src, "s1")
	p.Kind, p.AudioCopy, p.VideoBitrate = Heavy, false, 300_000

	a, b := &gateWriter{first: make(chan struct{}), release: make(chan struct{})}, &gateWriter{first: make(chan struct{}), release: make(chan struct{})}
	doneA, doneB := make(chan struct{}), make(chan struct{})
	ctxA, cancelA := context.WithCancel(context.Background())
	go func() { _, _ = m.StreamTS(ctxA, p, a); close(doneA) }()
	<-a.first
	// Otra sesión no cabe.
	if _, err := m.StreamTS(context.Background(), basePlanHeavy(src, "otra"), &bytes.Buffer{}); !errors.Is(err, ErrCapacity) {
		t.Fatalf("otra sesión: %v", err)
	}
	// La misma sesión abre otro flujo (un salto del televisor) sin gastar otro cupo.
	ctxB, cancelB := context.WithCancel(context.Background())
	go func() { _, _ = m.StreamTS(ctxB, p, b); close(doneB) }()
	<-b.first
	if used, _ := m.Admission().Usage(); used[Heavy] != 1 || len(m.Jobs()) != 2 {
		t.Fatalf("usado=%v trabajos=%d", used, len(m.Jobs()))
	}
	// Termina el primero: el cupo sigue porque el segundo corre.
	cancelA()
	close(a.release)
	<-doneA
	if used, _ := m.Admission().Usage(); used[Heavy] != 1 {
		t.Fatalf("el cupo se liberó con un flujo aún abierto: %v", used)
	}
	cancelB()
	close(b.release)
	<-doneB
	if used, _ := m.Admission().Usage(); used[Heavy] != 0 {
		t.Fatalf("cupo sin liberar: %v", used)
	}
}

func basePlanHeavy(path, session string) PipePlan {
	p := basePlan(path, session)
	p.Kind, p.AudioCopy, p.VideoBitrate = Heavy, false, 300_000
	return p
}

func TestCerrarLaSesionParaElFlujoYDesbloqueaLaEscritura(t *testing.T) {
	tl := needTools(t)
	src := sharedIrregular(t, tl)
	m := pipeManager(t, tl, 1, 1, "")
	p := basePlanHeavy(src, "s1")
	gw := &gateWriter{first: make(chan struct{}), release: make(chan struct{})}
	var aborted bool
	p.Abort = func() { aborted = true; close(gw.release) }
	done := make(chan error, 1)
	go func() { _, err := m.StreamTS(context.Background(), p, gw); done <- err }()
	<-gw.first
	if jobs := m.Jobs(); len(jobs) != 1 || jobs[0].SessionID != "s1" {
		t.Fatalf("trabajos: %+v", jobs)
	}
	if s, ok := m.SessionOfJob(m.Jobs()[0].ID); !ok || s != "s1" {
		t.Fatalf("SessionOfJob = %q %v", s, ok)
	}
	m.OnSessionEnded("s1") // El administrador cancela.
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cerrar la sesión no es un fallo: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("el flujo no terminó")
	}
	if !aborted {
		t.Fatal("no se llamó a Abort: la escritura bloqueada no se habría liberado")
	}
	if used, _ := m.Admission().Usage(); used[Heavy] != 0 {
		t.Fatalf("cupo sin liberar: %v", used)
	}
}

func TestSinCerrarLaSesionNoSeLlamaAAbort(t *testing.T) {
	tl := needTools(t)
	src := sharedIrregular(t, tl)
	m := pipeManager(t, tl, 1, 1, "")
	p := basePlan(src, "s1")
	var aborted bool
	p.Abort = func() { aborted = true }
	var out bytes.Buffer
	if _, err := m.StreamTS(context.Background(), p, &out); err != nil {
		t.Fatal(err)
	}
	// Dejar correr la goroutine que vigila la cancelación.
	time.Sleep(100 * time.Millisecond)
	if aborted {
		t.Fatal("un flujo que termina solo no debe abortar la escritura")
	}
}

func TestStreamTSFallaSiLaEntradaNoExiste(t *testing.T) {
	tl := needTools(t)
	m := pipeManager(t, tl, 1, 1, "")
	p := basePlan(filepath.Join(t.TempDir(), "no existe.mkv"), "s1")
	var out bytes.Buffer
	n, err := m.StreamTS(context.Background(), p, &out)
	if !errors.Is(err, ErrPipeFailed) || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if m.Failures() != 1 {
		t.Fatalf("fallos = %d", m.Failures())
	}
	if used, _ := m.Admission().Usage(); used[Light] != 0 {
		t.Fatalf("cupo sin liberar: %v", used)
	}
}

func TestStreamTSRechazaUnPlanInvalido(t *testing.T) {
	tl := needTools(t)
	m := pipeManager(t, tl, 1, 1, "")
	p := basePlan("relativa.mkv", "s1")
	if _, err := m.StreamTS(context.Background(), p, &bytes.Buffer{}); err == nil || errors.Is(err, ErrCapacity) {
		t.Fatalf("err = %v", err)
	}
	if used, _ := m.Admission().Usage(); used[Light] != 0 {
		t.Fatalf("cupo sin liberar tras un plan inválido: %v", used)
	}
	if entries, _ := os.ReadDir(m.Dir()); len(entries) != 0 {
		t.Fatalf("quedaron archivos: %v", entries)
	}
}

func TestSiElCodificadorDeHardwareFallaSeUsaX264(t *testing.T) {
	tl := needTools(t)
	src := sharedIrregular(t, tl)
	// NVENC casi nunca está en un equipo de pruebas: FFmpeg falla antes de
	// escribir nada y el flujo se reintenta con libx264. Si estuviera, funciona
	// a la primera, y ambas cosas son correctas.
	m := pipeManager(t, tl, 1, 1, ffmpeg.NVENC)
	p := basePlanHeavy(src, "s1")
	var out bytes.Buffer
	n, err := m.StreamTS(context.Background(), p, &out)
	if err != nil || n == 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	checkSync(t, out.Bytes())
	if m.Encoder() != ffmpeg.NVENC && m.Encoder() != ffmpeg.X264 {
		t.Fatalf("codificador = %s", m.Encoder())
	}
	if m.Encoder() == ffmpeg.X264 && m.Failures() != 1 {
		t.Fatalf("el intento fallido cuenta una vez: %d", m.Failures())
	}
}

func TestElRecolectorNoBorraLosFlujosActivos(t *testing.T) {
	tl := needTools(t)
	src := sharedIrregular(t, tl)
	m := pipeManager(t, tl, 1, 1, "")
	p := basePlanHeavy(src, "s1")
	gw := &gateWriter{first: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() { _, _ = m.StreamTS(context.Background(), p, gw); close(done) }()
	<-gw.first

	var dir string
	entries, _ := os.ReadDir(m.Dir())
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), pipeDirPrefix) {
			dir = filepath.Join(m.Dir(), e.Name())
		}
	}
	if dir == "" {
		t.Fatal("no hay directorio de trabajo del flujo")
	}
	// Un directorio viejo: si el recolector lo viera huérfano, lo borraría.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	// Uno huérfano de verdad, con el mismo prefijo: este sí.
	orphan := filepath.Join(m.Dir(), pipeDirPrefix+"huerfano")
	if err := os.Mkdir(orphan, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(orphan, old, old); err != nil {
		t.Fatal(err)
	}
	m.collect()
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("el recolector borró el directorio de un flujo activo: %v", err)
	}
	if _, err := os.Stat(orphan); err == nil {
		t.Fatal("el recolector no borró un directorio huérfano")
	}
	m.OnSessionEnded("s1")
	close(gw.release)
	<-done
}

func TestFlushWriter(t *testing.T) {
	var flushes int
	fw := newFlushWriter(&flushRecorder{n: &flushes})
	if n, err := fw.Write([]byte("abc")); n != 3 || err != nil || flushes != 1 {
		t.Fatalf("n=%d err=%v flushes=%d", n, err, flushes)
	}
	if _, err := fw.Write(nil); err != nil || flushes != 1 {
		t.Fatalf("una escritura vacía no vacía el buffer: %d", flushes)
	}
	var plain bytes.Buffer
	if got := newFlushWriter(&plain); got != &plain {
		t.Fatal("sin Flush se usa el escritor tal cual")
	}
}

type flushRecorder struct{ n *int }

func (f *flushRecorder) Write(p []byte) (int, error) { return len(p), nil }
func (f *flushRecorder) Flush()                      { *f.n++ }
