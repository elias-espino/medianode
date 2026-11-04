package transcoding

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"medianode/internal/database"
	"medianode/internal/events"
	"medianode/internal/ffmpeg"
	"medianode/internal/testutil"
)

type tools struct{ ffmpeg, ffprobe string }

func needTools(t *testing.T) tools {
	t.Helper()
	if testing.Short() {
		t.Skip("necesita FFmpeg y tarda")
	}
	f, err := ffmpeg.Locate()
	if err != nil {
		t.Skip("FFmpeg no está instalado")
	}
	p, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("FFprobe no está instalado")
	}
	return tools{f, p}
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Skip("falta el fixture; genéralo con scripts/gen-fixtures")
	}
	return p
}

// irregular genera un vídeo de 60 s con keyframes a intervalos irregulares,
// el caso que obliga a cortar en keyframes al copiar.
func irregular(t *testing.T, tl tools) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "irregular (prueba) ñ.mkv")
	kf := "0,2.5,4,9,11.2,13,19.5,20,24,31,32,36.5,44,45,50.1,55,58"
	cmd := exec.Command(tl.ffmpeg, "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=25:duration=60",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=60", "-c:v", "libx264", "-preset", "ultrafast",
		"-x264-params", "scenecut=0:keyint=10000:min-keyint=1", "-force_key_frames", kf, "-c:a", "aac", "-shortest", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return out
}

type fixtureEnv struct {
	t   *testing.T
	m   *Manager
	tl  tools
	bus *events.Bus
	db  *database.DB
}

func newManager(t *testing.T, tl tools) *fixtureEnv {
	db := testutil.NewDB(t)
	bus := events.NewBus()
	m := NewManager(Options{FFmpeg: tl.ffmpeg, Dir: t.TempDir(), SegmentSeconds: 6, RestartSegments: 5, WaitTimeout: 30 * time.Second},
		db, bus, NewAdmission(2, 4), zerolog.Nop())
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })
	return &fixtureEnv{t: t, m: m, tl: tl, bus: bus, db: db}
}

func (f *fixtureEnv) job(session string) *job {
	st, err := f.m.get(session)
	if err != nil {
		f.t.Fatal(err)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.job
}

// firstPTS devuelve el primer timestamp de vídeo de un segmento, en segundos.
// Un segmento fMP4 necesita su init delante.
func (f *fixtureEnv) firstPTS(path, init string) float64 {
	f.t.Helper()
	target := path
	if init != "" {
		joined := filepath.Join(f.t.TempDir(), "joined.mp4")
		a, _ := os.ReadFile(init)
		b, _ := os.ReadFile(path)
		if err := os.WriteFile(joined, append(a, b...), 0o644); err != nil {
			f.t.Fatal(err)
		}
		target = joined
	}
	out, err := exec.Command(f.tl.ffprobe, "-v", "error", "-select_streams", "v:0", "-show_entries", "packet=pts_time",
		"-of", "csv=p=0", target).Output()
	if err != nil {
		f.t.Fatalf("ffprobe %s: %v", path, err)
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	v, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(line), ","), 64)
	if err != nil {
		f.t.Fatalf("pts %q: %v", line, err)
	}
	return v
}

func near(a, b, tol float64) bool { return a > b-tol && a < b+tol }

func TestRecodificarConSaltos(t *testing.T) {
	tl := needTools(t)
	f := newManager(t, tl)
	src := fixture(t, "seek_120s_h264.mkv")
	err := f.m.Prepare(context.Background(), Plan{
		SessionID: "s1", FileID: "f1", Path: src, DurationUS: 120_023_000, Kind: Heavy,
		VideoIndex: 0, AudioIndex: -1, Height: 360, VideoBitrate: 800_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	init, err := f.m.File(ctx, "s1", "init.mp4")
	if err != nil {
		t.Fatal(err)
	}
	seg0, err := f.m.File(ctx, "s1", "seg_00000.m4s")
	if err != nil {
		t.Fatal(err)
	}
	if pts := f.firstPTS(seg0, init); !near(pts, 0, 0.2) {
		t.Fatalf("segmento 0 en %.3f", pts)
	}
	first := f.job("s1")

	// Saltar lejos reinicia FFmpeg y responde en menos de 5 s.
	start := time.Now()
	seg15, err := f.m.File(ctx, "s1", "seg_00015.m4s")
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("el salto tardó %s", d)
	} else {
		t.Logf("salto a 90 s: %s", d)
	}
	second := f.job("s1")
	if second == first || second.startSeg != 15 || !isDone(first) {
		t.Fatal("el salto debía reiniciar FFmpeg y parar el anterior")
	}
	// La posición coincide con la del archivo: el segmento 15 empieza en 90 s.
	if pts := f.firstPTS(seg15, init); !near(pts, 90, 0.2) {
		t.Fatalf("segmento 15 en %.3f", pts)
	}
	// Una zona ya generada se sirve sin reiniciar.
	if _, err := f.m.File(ctx, "s1", "seg_00000.m4s"); err != nil {
		t.Fatal(err)
	}
	if f.job("s1") != second {
		t.Fatal("servir una zona generada no debe reiniciar")
	}
	// Un segmento cercano espera al trabajo activo en vez de reiniciar.
	if _, err := f.m.File(ctx, "s1", "seg_00017.m4s"); err != nil {
		t.Fatal(err)
	}
	if f.job("s1") != second {
		t.Fatal("un segmento cercano no debe reiniciar")
	}

	for _, bad := range []string{"../seg_00001.m4s", "seg_00001.ts", "seg_99999.m4s", "ffmpeg_0.m3u8", "seg_1.m4s", "list_0.csv"} {
		if _, err := f.m.File(ctx, "s1", bad); !errors.Is(err, ErrBadName) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := f.m.File(ctx, "otra", "seg_00000.m4s"); !errors.Is(err, ErrNoStream) {
		t.Fatalf("%v", err)
	}
	media, _ := f.m.Media("s1")
	if !strings.Contains(string(media), "seg_00020.m4s") || !strings.Contains(string(media), "#EXT-X-ENDLIST") {
		t.Fatal(string(media))
	}

	// Terminar la sesión para FFmpeg y borra el directorio.
	dir := filepath.Join(f.m.opts.Dir, "s1")
	f.m.OnSessionEnded("s1")
	if !isDone(second) {
		t.Fatal("FFmpeg sigue vivo")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("el directorio sigue: %v", err)
	}
}

// Copiando el vídeo, un reinicio produce exactamente los segmentos de la
// playlist sintética, también con keyframes irregulares.
func TestCopiarConIndiceYSaltos(t *testing.T) {
	tl := needTools(t)
	f := newManager(t, tl)
	src := irregular(t, tl)
	kf, dur, err := BuildIndex(context.Background(), tl.ffprobe, src)
	if err != nil {
		t.Fatal(err)
	}
	idx := &Index{DurationUS: dur, KeyframesUS: kf}
	segs := CopySegments(kf, dur, 6)
	err = f.m.Prepare(context.Background(), Plan{
		SessionID: "s2", FileID: "f2", Path: src, DurationUS: dur, StartUS: 37_000_000, Kind: Light,
		VideoIndex: 0, AudioIndex: 1, AudioCopy: true, Keyframes: idx,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// Empieza en el segmento que contiene la posición inicial.
	if j := f.job("s2"); segs[j.startSeg].StartUS != 36_520_000 {
		t.Fatalf("inicio en %d", segs[j.startSeg].StartUS)
	}
	for _, n := range []int{6, 7, 1, 2, 9, 4} {
		path, err := f.m.File(ctx, "s2", SegmentName(n, "ts"))
		if err != nil {
			t.Fatalf("segmento %d: %v", n, err)
		}
		want := float64(segs[n].StartUS) / 1e6
		if pts := f.firstPTS(path, ""); !near(pts, want, 0.05) {
			t.Fatalf("segmento %d empieza en %.3f, la playlist dice %.3f", n, pts, want)
		}
	}
	media, _ := f.m.Media("s2")
	if !strings.Contains(string(media), "#EXTINF:7.480000,\nseg_00006.ts") {
		t.Fatal(string(media))
	}
}

// Sin índice: playlist EVENT con lo ya generado, sin reinicios.
func TestCopiarSinIndice(t *testing.T) {
	tl := needTools(t)
	f := newManager(t, tl)
	src := fixture(t, "seek_120s_h264.mkv")
	err := f.m.Prepare(context.Background(), Plan{
		SessionID: "s3", FileID: "f3", Path: src, DurationUS: 120_023_000, Kind: Light, VideoIndex: 0, AudioIndex: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.File(context.Background(), "s3", "seg_00001.ts"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for !isDone(f.job("s3")) {
		if time.Now().After(deadline) {
			t.Fatal("la copia no terminó")
		}
		time.Sleep(100 * time.Millisecond)
	}
	media, _ := f.m.Media("s3")
	if !strings.Contains(string(media), "PLAYLIST-TYPE:EVENT") || !strings.Contains(string(media), "#EXT-X-ENDLIST") {
		t.Fatal(string(media))
	}
	if _, err := f.m.File(context.Background(), "s3", "seg_00500.ts"); !errors.Is(err, ErrBadName) {
		t.Fatalf("un segmento que no va a existir: %v", err)
	}
}

// Sin índice, la primera petición de la playlist no puede recibir una EVENT
// vacía: los reproductores la rechazan. Media espera al primer segmento.
func TestMediaSinIndiceNoDevuelveListaVacia(t *testing.T) {
	tl := needTools(t)
	f := newManager(t, tl)
	src := fixture(t, "seek_120s_h264.mkv")
	err := f.m.Prepare(context.Background(), Plan{
		SessionID: "s4", FileID: "f4", Path: src, DurationUS: 120_023_000, Kind: Light, VideoIndex: 0, AudioIndex: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	media, err := f.m.Media("s4")
	if err != nil {
		t.Fatal(err)
	}
	// Vuelve en cuanto hay un segmento, no al agotar el plazo de espera.
	if d := time.Since(start); d > 8*time.Second {
		t.Fatalf("Media tardó %s", d)
	}
	if !strings.Contains(string(media), "#EXTINF") || !strings.Contains(string(media), "seg_00000.ts") {
		t.Fatalf("playlist sin segmentos:\n%s", media)
	}
}

// Si FFmpeg falla, la playlist EVENT no se cierra con ENDLIST: el cliente
// creería que el vídeo termina ahí.
func TestMediaDeTrabajoFallidoNoLlevaEndlist(t *testing.T) {
	tl := needTools(t)
	f := newManager(t, tl)
	err := f.m.Prepare(context.Background(), Plan{
		SessionID: "s7", FileID: "f7", Path: filepath.Join(t.TempDir(), "no-existe.mkv"), DurationUS: 60_000_000,
		Kind: Light, VideoIndex: 0, AudioIndex: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for !isDone(f.job("s7")) {
		if time.Now().After(deadline) {
			t.Fatal("el trabajo no terminó")
		}
		time.Sleep(50 * time.Millisecond)
	}
	media, err := f.m.Media("s7")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(media), "#EXT-X-ENDLIST") {
		t.Fatalf("un trabajo fallido no cierra la playlist:\n%s", media)
	}
}

// Restream sustituye el trabajo por otro plan: el FFmpeg anterior termina, el
// directorio se renueva y el flujo viejo ya no puede reiniciar nada.
func TestRestreamSustituyeElTrabajo(t *testing.T) {
	tl := needTools(t)
	f := newManager(t, tl)
	src := fixture(t, "mkv_multi_audio.mkv")
	ctx := context.Background()
	plan := Plan{SessionID: "s8", FileID: "f8", Path: src, DurationUS: 4_000_000, Kind: Heavy,
		VideoIndex: 0, AudioIndex: 1, Height: 360, VideoBitrate: 800_000}
	if err := f.m.Prepare(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.File(ctx, "s8", "seg_00000.m4s"); err != nil {
		t.Fatal(err)
	}
	oldStream, err := f.m.get("s8")
	if err != nil {
		t.Fatal(err)
	}
	oldJob := f.job("s8")

	plan.AudioIndex = 2
	if err := f.m.Restream(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if !isDone(oldJob) {
		t.Fatal("el FFmpeg anterior debía haber terminado")
	}
	cur, err := f.m.get("s8")
	if err != nil || cur == oldStream || cur.plan.AudioIndex != 2 {
		t.Fatalf("el flujo no se sustituyó: %v", err)
	}
	// El cupo es de la sesión: Restream no lo toca.
	if _, err := f.m.File(ctx, "s8", "seg_00000.m4s"); err != nil {
		t.Fatalf("el flujo nuevo debía servir: %v", err)
	}
	// Una petición en vuelo del flujo anterior no puede arrancar FFmpeg.
	oldStream.mu.Lock()
	err = f.m.restart(oldStream, 0)
	oldStream.mu.Unlock()
	if !errors.Is(err, ErrNoStream) {
		t.Fatalf("restart sobre un flujo cerrado = %v", err)
	}
	if _, err := os.Stat(oldStream.dir); err != nil {
		// El directorio es el mismo (misma sesión): lo recrea el flujo nuevo.
		t.Fatalf("falta el directorio del flujo nuevo: %v", err)
	}
	// Restream de una sesión que no existe falla sin crear nada.
	if err := f.m.Restream(ctx, Plan{SessionID: "no-existe"}); !errors.Is(err, ErrNoStream) {
		t.Fatalf("err = %v", err)
	}
}

// 20 saltos seguidos no dejan procesos huérfanos.
func TestVeinteSaltosSinHuerfanos(t *testing.T) {
	tl := needTools(t)
	f := newManager(t, tl)
	src := fixture(t, "seek_120s_h264.mkv")
	err := f.m.Prepare(context.Background(), Plan{
		SessionID: "s4", FileID: "f4", Path: src, DurationUS: 120_023_000, Kind: Heavy,
		VideoIndex: 0, AudioIndex: -1, Height: 180, VideoBitrate: 300_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	var jobs []*job
	for i := range 20 {
		n := []int{18, 2, 12, 0, 19}[i%5] // Siempre lejos del anterior.
		st, _ := f.m.get("s4")
		st.mu.Lock()
		if err := f.m.restart(st, n); err != nil {
			st.mu.Unlock()
			t.Fatal(err)
		}
		jobs = append(jobs, st.job)
		st.mu.Unlock()
	}
	f.m.OnSessionEnded("s4")
	for i, j := range jobs {
		if !isDone(j) || j.cmd.ProcessState == nil {
			t.Fatalf("el trabajo %d sigue vivo", i)
		}
	}
}

// Matar FFmpeg termina en transcode.failed, con diagnóstico en la base.
func TestFFmpegMuerto(t *testing.T) {
	tl := needTools(t)
	f := newManager(t, tl)
	sub := f.bus.Subscribe()
	src := fixture(t, "seek_120s_h264.mkv")
	err := f.m.Prepare(context.Background(), Plan{
		SessionID: "s5", FileID: "f5", Path: src, DurationUS: 120_023_000, Kind: Heavy,
		VideoIndex: 0, AudioIndex: -1, Height: 360, VideoBitrate: 800_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	j := f.job("s5")
	if err := j.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(10 * time.Second)
	for {
		select {
		case e := <-sub.C:
			if e.Name == "transcode.failed" {
				if e.Data["job_id"] != j.id || !e.AdminOnly {
					t.Fatalf("%+v", e)
				}
				return
			}
		case <-deadline:
			t.Fatal("no llegó transcode.failed")
		}
	}
}

// Un archivo que no existe falla al arrancar y queda registrado.
func TestEntradaInexistente(t *testing.T) {
	tl := needTools(t)
	f := newManager(t, tl)
	err := f.m.Prepare(context.Background(), Plan{
		SessionID: "s6", FileID: "f6", Path: filepath.Join(t.TempDir(), "no-existe.mkv"), DurationUS: 60_000_000,
		Kind: Heavy, VideoIndex: 0, AudioIndex: -1, Height: 360, VideoBitrate: 800_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.m.opts.WaitTimeout = 5 * time.Second
	if _, err := f.m.File(context.Background(), "s6", "seg_00000.m4s"); err == nil {
		t.Fatal("debía fallar")
	}
	if j := f.job("s6"); !strings.Contains(j.stderr.String(), "no-existe") && !strings.Contains(j.stderr.String(), "No such file") {
		t.Fatalf("el diagnóstico debía conservarse: %q", j.stderr.String())
	}
}
