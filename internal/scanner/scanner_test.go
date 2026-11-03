package scanner

import (
	"context"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"medianode/internal/database"
	"medianode/internal/database/sqlc"
	"medianode/internal/media"
	"medianode/internal/probe"
	"medianode/internal/testutil"
)

// fakeProber simula FFprobe. Un nombre con «corrupt» falla como archivo
// dañado; gate, si existe, bloquea cada análisis hasta recibir un valor.
type fakeProber struct {
	calls       atomic.Int64
	unavailable bool
	gate        chan struct{}
}

func (p *fakeProber) Run(ctx context.Context, path string) (*probe.Result, error) {
	p.calls.Add(1)
	if p.gate != nil {
		select {
		case <-p.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if p.unavailable {
		return nil, probe.ErrUnavailable
	}
	if strings.Contains(filepath.Base(path), "corrupt") {
		return nil, probe.ErrInvalidMedia
	}
	dur, rate := int64(5400000), int64(8000000)
	format := "matroska,webm"
	if strings.EqualFold(filepath.Ext(path), ".mp4") {
		format = "mov,mp4,m4a,3gp,3g2,mj2"
	}
	return &probe.Result{
		FormatName: format, DurationMS: &dur, Bitrate: &rate,
		Streams: []probe.Stream{
			{Index: 0, Type: media.StreamVideo, Codec: new("h264"), Width: new(int64(1920))},
			{Index: 1, Type: media.StreamAudio, Codec: new("aac"), Language: new("spa"), Default: true},
		},
	}, nil
}

type fixture struct {
	t      *testing.T
	ctx    context.Context
	db     *database.DB
	sc     *Scanner
	prober *fakeProber
	clock  time.Time
	mu     sync.Mutex
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, ctx: context.Background(), db: testutil.NewDB(t), prober: &fakeProber{},
		clock: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)}
	f.sc = New(f.db, f.prober, Options{Workers: 2, Retention: 30 * 24 * time.Hour, BatchSize: 3}, zerolog.Nop())
	f.sc.now = f.now
	t.Cleanup(f.sc.Close)
	return f
}

func (f *fixture) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clock
}

func (f *fixture) advance(d time.Duration) {
	f.mu.Lock()
	f.clock = f.clock.Add(d)
	f.mu.Unlock()
}

func (f *fixture) library(t media.LibraryType, roots ...string) (string, []string) {
	f.t.Helper()
	id := mustID(f.t)
	err := f.db.Q.CreateLibrary(f.ctx, sqlc.CreateLibraryParams{ID: id, Name: "lib", Type: string(t), Enabled: 1, CreatedAt: database.Now()})
	if err != nil {
		f.t.Fatal(err)
	}
	var pathIDs []string
	for _, r := range roots {
		pid := mustID(f.t)
		if err := f.db.Q.CreateLibraryPath(f.ctx, sqlc.CreateLibraryPathParams{ID: pid, LibraryID: id, Path: r}); err != nil {
			f.t.Fatal(err)
		}
		pathIDs = append(pathIDs, pid)
	}
	return id, pathIDs
}

func (f *fixture) scan(libraryID string) sqlc.ScanJob {
	f.t.Helper()
	job, err := f.sc.Scan(f.ctx, libraryID, media.TriggerManual)
	if err != nil {
		f.t.Fatal(err)
	}
	return job
}

func (f *fixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.db.Read.QueryRowContext(f.ctx, query, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *fixture) file(pathID, rel string) sqlc.MediaFile {
	f.t.Helper()
	var id string
	err := f.db.Read.QueryRowContext(f.ctx,
		"SELECT id FROM media_files WHERE library_path_id = ? AND relative_path = ?", pathID, rel).Scan(&id)
	if err != nil {
		f.t.Fatalf("%s: %v", rel, err)
	}
	row, err := f.db.R.GetMediaFile(f.ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return row
}

func mustID(t *testing.T) string {
	t.Helper()
	id, err := database.NewID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// write crea un archivo con contenido aleatorio, para que cada uno tenga su
// propia huella.
func write(t *testing.T, root, rel string, size int) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, size)
	_, _ = rand.Read(data)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPrimerEscaneoIndexaEIdentifica(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	write(t, root, "The Matrix (1999)/The.Matrix.1999.1080p.mkv", 1000)
	write(t, root, "Inception (2010).mp4", 1000)
	write(t, root, "Inception (2010).es.srt", 10)
	write(t, root, "Inception (2010).en.forced.srt", 10)
	write(t, root, "notas.txt", 10)
	write(t, root, ".oculto/Hidden (2000).mkv", 10)
	write(t, root, "Inception (2010)-sample.mkv", 10)
	lib, paths := f.library(media.LibraryMovies, root)

	job := f.scan(lib)
	if job.Status != string(media.ScanCompleted) || job.FilesDiscovered != 2 || job.FilesProcessed != 2 {
		t.Fatalf("escaneo: %+v", job)
	}
	if f.count("SELECT COUNT(*) FROM media_items WHERE type = 'movie'") != 2 {
		t.Fatal("deben crearse dos películas")
	}
	inception := f.file(paths[0], "Inception (2010).mp4")
	if inception.ProbeStatus != "done" || *inception.Container != "mp4" || *inception.DurationMs != 5400000 || inception.FileHash == nil {
		t.Fatalf("archivo: %+v", inception)
	}
	if f.count("SELECT COUNT(*) FROM media_streams WHERE media_file_id = ?", inception.ID) != 2 {
		t.Fatal("faltan las pistas")
	}
	if f.count("SELECT COUNT(*) FROM subtitles WHERE media_file_id = ?", inception.ID) != 2 ||
		f.count("SELECT COUNT(*) FROM subtitles WHERE language = 'en' AND is_forced = 1") != 1 {
		t.Fatal("subtítulos externos")
	}
	var title string
	var year, duration int64
	err := f.db.Read.QueryRow(`SELECT i.title, i.year, i.duration_ms FROM media_items i
		JOIN media_file_items l ON l.media_item_id = i.id WHERE l.media_file_id = ?`, inception.ID).Scan(&title, &year, &duration)
	if err != nil || title != "Inception" || year != 2010 || duration != 5400000 {
		t.Fatalf("%q %d %d %v", title, year, duration, err)
	}
	var lastScan *string
	_ = f.db.Read.QueryRow("SELECT last_scan_at FROM library_paths WHERE id = ?", paths[0]).Scan(&lastScan)
	if lastScan == nil {
		t.Fatal("last_scan_at debe quedar escrito")
	}
}

func TestReescanearSinCambiosNoLanzaFFprobe(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	write(t, root, "A (2001).mkv", 100)
	write(t, root, "B (2002).mkv", 100)
	write(t, root, "corrupt (2003).mkv", 100)
	lib, _ := f.library(media.LibraryMovies, root)
	f.scan(lib)
	first := f.prober.calls.Load()

	job := f.scan(lib)
	if f.prober.calls.Load() != first {
		t.Fatalf("FFprobe se lanzó %d veces sin cambios", f.prober.calls.Load()-first)
	}
	if job.FilesDiscovered != 3 || job.FilesProcessed != 0 {
		t.Fatalf("%+v", job)
	}
}

func TestDiscoDesconectadoNoMarcaAusentes(t *testing.T) {
	f := newFixture(t)
	parent := t.TempDir()
	root := filepath.Join(parent, "disco")
	write(t, root, "A (2001).mkv", 100)
	lib, paths := f.library(media.LibraryMovies, root)
	f.scan(lib)

	if err := os.Rename(root, root+"-fuera"); err != nil {
		t.Fatal(err)
	}
	job := f.scan(lib)
	if job.Status != string(media.ScanCompleted) {
		t.Fatalf("%+v", job)
	}
	if f.count("SELECT COUNT(*) FROM media_files WHERE missing_since IS NOT NULL") != 0 {
		t.Fatal("un disco desconectado no debe marcar ausencias")
	}
	if f.count("SELECT is_available FROM library_paths WHERE id = ?", paths[0]) != 0 {
		t.Fatal("la raíz debe quedar no disponible")
	}

	if err := os.Rename(root+"-fuera", root); err != nil {
		t.Fatal(err)
	}
	f.scan(lib)
	if f.count("SELECT is_available FROM library_paths WHERE id = ?", paths[0]) != 1 {
		t.Fatal("la raíz debe volver a estar disponible")
	}
}

func TestMontajeVacioNoMarcaAusentes(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	p := write(t, root, "A (2001).mkv", 100)
	lib, paths := f.library(media.LibraryMovies, root)
	f.scan(lib)

	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	f.scan(lib)
	if f.count("SELECT COUNT(*) FROM media_files WHERE missing_since IS NOT NULL") != 0 ||
		f.count("SELECT is_available FROM library_paths WHERE id = ?", paths[0]) != 0 {
		t.Fatal("una raíz vacía con archivos conocidos se trata como desmontada")
	}

	// Si la raíz tiene contenido, la ausencia es real.
	write(t, root, "otra cosa.txt", 1)
	f.scan(lib)
	if f.count("SELECT COUNT(*) FROM media_files WHERE missing_since IS NOT NULL") != 1 {
		t.Fatal("con contenido en la raíz, el archivo falta de verdad")
	}
}

func TestRenombrarConservaIdentificadorYElemento(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	old := write(t, root, "Peli.mkv", 3<<20)
	lib, paths := f.library(media.LibraryMovies, root)
	f.scan(lib)
	before := f.file(paths[0], "Peli.mkv")
	calls := f.prober.calls.Load()

	if err := os.MkdirAll(filepath.Join(root, "Nueva"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(old, filepath.Join(root, "Nueva", "Peli (2001).mkv")); err != nil {
		t.Fatal(err)
	}
	f.scan(lib)
	after := f.file(paths[0], "Nueva/Peli (2001).mkv")
	if after.ID != before.ID || after.MissingSince != nil {
		t.Fatalf("antes %s, después %+v", before.ID, after)
	}
	if f.prober.calls.Load() != calls {
		t.Fatal("un movimiento no necesita FFprobe")
	}
	if f.count("SELECT COUNT(*) FROM media_files") != 1 || f.count("SELECT COUNT(*) FROM media_items") != 1 {
		t.Fatal("no debe duplicarse nada")
	}
	if f.count("SELECT COUNT(*) FROM media_file_items WHERE media_file_id = ?", before.ID) != 1 {
		t.Fatal("el archivo conserva su elemento, y con él el progreso")
	}
}

func TestMovimientoEntreRaicesDeLaMismaBiblioteca(t *testing.T) {
	f := newFixture(t)
	a, b := t.TempDir(), t.TempDir()
	write(t, a, "X (2001).mkv", 100)
	write(t, b, "Y (2002).mkv", 100)
	p := write(t, b, "Z (2003).mkv", 100)
	// Las raíces se recorren por orden de ruta; la de destino puede ir primero.
	lib, paths := f.library(media.LibraryMovies, a, b)
	f.scan(lib)
	before := f.file(paths[1], "Z (2003).mkv")

	data, _ := os.ReadFile(p)
	_ = os.Remove(p)
	if err := os.WriteFile(filepath.Join(a, "Z (2003).mkv"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	f.scan(lib)
	after := f.file(paths[0], "Z (2003).mkv")
	if after.ID != before.ID {
		t.Fatal("debe reconocerse el movimiento entre raíces")
	}
}

func TestBorradoSeMarcaAusenteYSePurgaTrasLaRetencion(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	write(t, root, "Keep (2000).mkv", 100)
	p := write(t, root, "Show/Season 1/Show.S01E01.mkv", 100)
	write(t, root, "Show/Season 2/Show.S02E01.mkv", 100)
	lib, paths := f.library(media.LibraryTV, root)
	f.scan(lib)
	if f.count("SELECT COUNT(*) FROM media_items WHERE type = 'season'") != 2 {
		t.Fatal("dos temporadas")
	}

	_ = os.Remove(p)
	f.scan(lib)
	gone := f.file(paths[0], "Show/Season 1/Show.S01E01.mkv")
	if gone.MissingSince == nil {
		t.Fatal("el archivo borrado debe quedar ausente")
	}
	if f.count("SELECT COUNT(*) FROM media_items WHERE type = 'episode'") != 2 {
		t.Fatal("el elemento y su progreso se conservan durante la retención")
	}

	f.advance(29 * 24 * time.Hour)
	f.scan(lib)
	if f.count("SELECT COUNT(*) FROM media_files WHERE id = ?", gone.ID) != 1 {
		t.Fatal("aún dentro de la retención")
	}

	f.advance(2 * 24 * time.Hour)
	f.scan(lib)
	if f.count("SELECT COUNT(*) FROM media_files WHERE id = ?", gone.ID) != 0 {
		t.Fatal("pasada la retención se purga")
	}
	if f.count("SELECT COUNT(*) FROM media_items WHERE type = 'episode'") != 1 ||
		f.count("SELECT COUNT(*) FROM media_items WHERE type = 'season'") != 1 ||
		f.count("SELECT COUNT(*) FROM media_items WHERE type = 'series'") != 1 {
		t.Fatal("se purgan el episodio y la temporada vacía, no la serie")
	}
}

func TestReaparecerLimpiaLaAusencia(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	write(t, root, "Keep (2000).mkv", 100)
	p := write(t, root, "A (2001).mkv", 100)
	lib, paths := f.library(media.LibraryMovies, root)
	f.scan(lib)
	data, _ := os.ReadFile(p)
	info, _ := os.Stat(p)
	_ = os.Remove(p)
	f.scan(lib)
	if f.file(paths[0], "A (2001).mkv").MissingSince == nil {
		t.Fatal("debe quedar ausente")
	}
	_ = os.WriteFile(p, data, 0o644)
	_ = os.Chtimes(p, info.ModTime(), info.ModTime())
	calls := f.prober.calls.Load()
	f.scan(lib)
	if f.file(paths[0], "A (2001).mkv").MissingSince != nil || f.prober.calls.Load() != calls {
		t.Fatal("al reaparecer igual se limpia la ausencia sin reanalizar")
	}
}

func TestModificadoSeReanalizaYConservaElemento(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	write(t, root, "A (2001).mkv", 100)
	lib, paths := f.library(media.LibraryMovies, root)
	f.scan(lib)
	before := f.file(paths[0], "A (2001).mkv")
	write(t, root, "A (2001).mkv", 200)
	calls := f.prober.calls.Load()
	f.scan(lib)
	after := f.file(paths[0], "A (2001).mkv")
	if after.ID != before.ID || *after.FileSize != 200 || f.prober.calls.Load() != calls+1 {
		t.Fatalf("%+v", after)
	}
	if f.count("SELECT COUNT(*) FROM media_streams WHERE media_file_id = ?", after.ID) != 2 {
		t.Fatal("las pistas se sustituyen, no se duplican")
	}
	if f.count("SELECT COUNT(*) FROM media_file_items WHERE media_file_id = ?", after.ID) != 1 {
		t.Fatal("conserva su elemento")
	}
}

func TestInterrumpirYRepetirNoDuplica(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	for _, name := range []string{"A (2001)", "B (2002)", "C (2003)", "D (2004)", "E (2005)", "F (2006)", "G (2007)"} {
		write(t, root, name+".mkv", 100)
	}
	write(t, root, "Show/Show.S01E01.mkv", 100)
	write(t, root, "Show/Show.S01E02.mkv", 100)
	lib, _ := f.library(media.LibraryMovies, root)

	f.prober.gate = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan sqlc.ScanJob)
	go func() {
		job, _ := f.sc.Scan(ctx, lib, media.TriggerManual)
		done <- job
	}()
	for range 4 {
		f.prober.gate <- struct{}{}
	}
	// Espera a que el primer lote llegue a la base.
	deadline := time.Now().Add(5 * time.Second)
	for f.count("SELECT COUNT(*) FROM media_files") == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	job := <-done
	if job.Status != string(media.ScanInterrupted) {
		t.Fatalf("estado = %s", job.Status)
	}
	partial := f.count("SELECT COUNT(*) FROM media_files")
	if partial == 0 || partial == 9 {
		t.Fatalf("se esperaba un escaneo parcial: %d", partial)
	}

	f.prober.gate = nil
	f.scan(lib)
	f.scan(lib)
	if f.count("SELECT COUNT(*) FROM media_files") != 9 || f.count("SELECT COUNT(*) FROM media_items") != 9 {
		t.Fatalf("archivos %d, elementos %d", f.count("SELECT COUNT(*) FROM media_files"), f.count("SELECT COUNT(*) FROM media_items"))
	}
}

func TestArchivoCorruptoQuedaFailedYElEscaneoSigue(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	write(t, root, "corrupt (2000).mkv", 100)
	write(t, root, "Buena (2001).mkv", 100)
	lib, paths := f.library(media.LibraryMovies, root)
	job := f.scan(lib)
	if job.Status != string(media.ScanCompleted) {
		t.Fatalf("%+v", job)
	}
	bad := f.file(paths[0], "corrupt (2000).mkv")
	if bad.ProbeStatus != "failed" || f.count("SELECT COUNT(*) FROM media_file_items WHERE media_file_id = ?", bad.ID) != 0 {
		t.Fatalf("%+v", bad)
	}
	if f.file(paths[0], "Buena (2001).mkv").ProbeStatus != "done" {
		t.Fatal("el resto se indexa")
	}
}

func TestSinFFprobeElEscaneoFallaSinMarcarArchivos(t *testing.T) {
	f := newFixture(t)
	f.prober.unavailable = true
	root := t.TempDir()
	write(t, root, "A (2001).mkv", 100)
	lib, _ := f.library(media.LibraryMovies, root)
	job := f.scan(lib)
	if job.Status != string(media.ScanFailed) || job.ErrorMessage == nil || !strings.Contains(*job.ErrorMessage, "FFprobe") {
		t.Fatalf("%+v", job)
	}
	if f.count("SELECT COUNT(*) FROM media_files WHERE probe_status = 'failed'") != 0 {
		t.Fatal("sin FFprobe nada debe quedar como failed: no se reintentaría")
	}
}

func TestDosBibliotecasCompartenRuta(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	write(t, root, "A (2001).mkv", 100)
	write(t, root, "Show/Show.S01E01.mkv", 100)
	movies, _ := f.library(media.LibraryMovies, root)
	tv, _ := f.library(media.LibraryTV, root)
	f.scan(movies)
	f.scan(tv)
	if f.count("SELECT COUNT(*) FROM media_files") != 4 {
		t.Fatal("cada biblioteca indexa la ruta compartida")
	}
	if f.count("SELECT COUNT(*) FROM media_items WHERE library_id = ? AND type = 'episode'", tv) != 1 ||
		f.count("SELECT COUNT(*) FROM media_items WHERE library_id = ? AND type = 'movie'", movies) != 2 {
		t.Fatal("cada una identifica según su tipo")
	}
}

func TestSerieConRangoCreaDosEpisodios(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	write(t, root, "Lost/Season 1/Lost.S01E01-E02.mkv", 100)
	write(t, root, "Lost/Season 1/Lost.S01E03.mkv", 100)
	write(t, root, "Lost/Extras/Making of.mkv", 100)
	lib, paths := f.library(media.LibraryTV, root)
	f.scan(lib)
	double := f.file(paths[0], "Lost/Season 1/Lost.S01E01-E02.mkv")
	if f.count("SELECT COUNT(*) FROM media_file_items WHERE media_file_id = ?", double.ID) != 2 {
		t.Fatal("un rango enlaza dos episodios")
	}
	if f.count("SELECT COUNT(*) FROM media_items WHERE type = 'series'") != 1 ||
		f.count("SELECT COUNT(*) FROM media_items WHERE type = 'season'") != 1 ||
		f.count("SELECT COUNT(*) FROM media_items WHERE type = 'episode'") != 3 {
		t.Fatal("una serie, una temporada y tres episodios")
	}
	if f.count("SELECT COUNT(*) FROM media_items WHERE type = 'episode' AND duration_ms IS NOT NULL") != 1 {
		t.Fatal("el archivo doble no asigna su duración a ningún episodio")
	}
	extra := f.file(paths[0], "Lost/Extras/Making of.mkv")
	if f.count("SELECT COUNT(*) FROM media_file_items WHERE media_file_id = ?", extra.ID) != 0 {
		t.Fatal("sin patrón de episodio queda sin identificar")
	}
}

func TestVersionesAgrupanEnUnElemento(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	write(t, root, "Coco (2017)/Coco (2017) - 4K.mkv", 100)
	write(t, root, "Coco (2017)/Coco.2017.1080p.mkv", 100)
	lib, _ := f.library(media.LibraryMovies, root)
	f.scan(lib)
	if f.count("SELECT COUNT(*) FROM media_items") != 1 || f.count("SELECT COUNT(*) FROM media_file_items") != 2 {
		t.Fatal("dos versiones de un mismo elemento")
	}
}

func TestSubtituloBorradoSeQuita(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	write(t, root, "A (2001).mkv", 100)
	sub := write(t, root, "A (2001).es.srt", 10)
	lib, _ := f.library(media.LibraryMovies, root)
	f.scan(lib)
	_ = os.Remove(sub)
	f.scan(lib)
	if f.count("SELECT COUNT(*) FROM subtitles") != 0 {
		t.Fatal("el subtítulo borrado debe desaparecer")
	}
}

func TestUnSoloEscaneoPorBiblioteca(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	write(t, root, "A (2001).mkv", 100)
	lib, _ := f.library(media.LibraryMovies, root)
	f.prober.gate = make(chan struct{})
	first, err := f.sc.Start(lib, media.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.sc.Start(lib, media.TriggerWatcher)
	if err != nil || second.ID != first.ID {
		t.Fatalf("debe devolver el mismo escaneo: %v %s %s", err, first.ID, second.ID)
	}
	close(f.prober.gate)
}

func TestExclusiveDetieneElEscaneoEImpideOtro(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	write(t, root, "A (2001).mkv", 100)
	lib, _ := f.library(media.LibraryMovies, root)
	f.prober.gate = make(chan struct{})
	job, err := f.sc.Start(lib, media.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	err = f.sc.Exclusive(lib, func() error {
		if _, running := f.sc.Running(lib); running {
			t.Error("el escaneo debe haber terminado")
		}
		if _, err := f.sc.Start(lib, media.TriggerManual); err == nil {
			t.Error("no debe empezar otro escaneo durante la operación")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	row, _ := f.db.R.GetScanJob(f.ctx, job.ID)
	if row.Status != string(media.ScanInterrupted) {
		t.Fatalf("estado = %s", row.Status)
	}
	if _, err := f.sc.Start(lib, media.TriggerManual); err != nil {
		t.Fatalf("después se puede escanear: %v", err)
	}
	close(f.prober.gate)
}

func TestRecoverMarcaInterrumpidos(t *testing.T) {
	f := newFixture(t)
	lib, _ := f.library(media.LibraryMovies)
	err := f.db.Q.CreateScanJob(f.ctx, sqlc.CreateScanJobParams{ID: "j", LibraryID: lib, Trigger: "manual", StartedAt: new(database.Now())})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.sc.Recover(f.ctx); err != nil {
		t.Fatal(err)
	}
	row, _ := f.db.R.GetScanJob(f.ctx, "j")
	if row.Status != string(media.ScanInterrupted) || row.FinishedAt == nil {
		t.Fatalf("%+v", row)
	}
}

func TestUnionFueraDeLaRaizNoSeSigue(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("las uniones son de Windows; los symlinks se prueban aparte")
	}
	f := newFixture(t)
	root, outside := t.TempDir(), t.TempDir()
	write(t, outside, "Secreto (2000).mkv", 100)
	write(t, root, "A (2001).mkv", 100)
	out, err := exec.Command("cmd", "/c", "mklink", "/J", filepath.Join(root, "union"), outside).CombinedOutput()
	if err != nil {
		t.Fatalf("mklink: %v %s", err, out)
	}
	lib, _ := f.library(media.LibraryMovies, root)
	f.scan(lib)
	if f.count("SELECT COUNT(*) FROM media_files") != 1 {
		t.Fatal("una unión que sale de la raíz no se recorre")
	}
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("este sistema no permite crear symlinks: %v", err)
	}
}

func TestSymlinkFueraDeLaRaizSeIgnora(t *testing.T) {
	f := newFixture(t)
	root, outside := t.TempDir(), t.TempDir()
	secret := write(t, outside, "Secreto (2000).mkv", 100)
	inside := write(t, root, "A (2001).mkv", 100)
	symlinkOrSkip(t, secret, filepath.Join(root, "Enlace (2002).mkv"))
	symlinkOrSkip(t, outside, filepath.Join(root, "carpeta"))
	symlinkOrSkip(t, inside, filepath.Join(root, "Interno (2003).mkv"))
	lib, paths := f.library(media.LibraryMovies, root)
	f.scan(lib)
	if f.count("SELECT COUNT(*) FROM media_files") != 2 {
		t.Fatalf("archivos = %d", f.count("SELECT COUNT(*) FROM media_files"))
	}
	f.file(paths[0], "Interno (2003).mkv")
}

func TestUnderAny(t *testing.T) {
	cases := []struct {
		rel  string
		dirs []string
		want bool
	}{
		{"a/b.mkv", []string{"a"}, true},
		{"ab/c.mkv", []string{"a"}, false},
		{"a.mkv", []string{"."}, true},
		{"x/y/z.mkv", []string{"x/y"}, true},
		{"x/z.mkv", nil, false},
	}
	for _, c := range cases {
		if got := underAny(c.rel, c.dirs); got != c.want {
			t.Fatalf("%s %v = %v", c.rel, c.dirs, got)
		}
	}
}

func TestPartialHashDistingueFinal(t *testing.T) {
	root := t.TempDir()
	a := write(t, root, "a.bin", 3<<20)
	data, _ := os.ReadFile(a)
	data[len(data)-1] ^= 0xff
	_ = os.WriteFile(filepath.Join(root, "b.bin"), data, 0o644)
	data[len(data)-1] ^= 0xff
	data[(3<<20)/2] ^= 0xff // el centro no se lee
	_ = os.WriteFile(filepath.Join(root, "c.bin"), data, 0o644)

	ha, _ := partialHash(root, "a.bin", 3<<20)
	hb, _ := partialHash(root, "b.bin", 3<<20)
	hc, _ := partialHash(root, "c.bin", 3<<20)
	if ha == hb {
		t.Fatal("el último MiB forma parte de la huella")
	}
	if ha != hc {
		t.Fatal("la huella es parcial: el centro no cuenta")
	}
	small := write(t, root, "s.bin", 10)
	if _, err := partialHash(root, "s.bin", 10); err != nil {
		t.Fatalf("archivo pequeño: %v (%s)", err, small)
	}
	if _, err := partialHash(root, "../fuera.bin", 10); err == nil {
		t.Fatal("no debe salir de la raíz")
	}
}

func TestEscaneoConFFprobeReal(t *testing.T) {
	path, err := probe.Locate()
	if err != nil {
		t.Skip("FFprobe no está instalado")
	}
	src := filepath.Join("..", "..", "testdata", "fixtures")
	entries, err := os.ReadDir(src)
	if err != nil || len(entries) == 0 {
		t.Skip("faltan los fixtures")
	}
	root := t.TempDir()
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, e.Name()), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f := newFixture(t)
	f.sc.prober = &probe.Runner{Path: path, Timeout: 30 * time.Second}
	lib, paths := f.library(media.LibraryMovies, root)
	job := f.scan(lib)
	if job.Status != string(media.ScanCompleted) {
		t.Fatalf("%+v", job)
	}
	if f.file(paths[0], "corrupt_truncated.mp4").ProbeStatus != "failed" {
		t.Fatal("el archivo truncado queda failed")
	}
	hdr := f.file(paths[0], "mkv_hdr10_hevc.mkv")
	if f.count("SELECT COUNT(*) FROM media_streams WHERE media_file_id = ? AND hdr_format = 'hdr10' AND bit_depth = 10", hdr.ID) != 1 {
		t.Fatal("HDR10 y 10 bits en media_streams")
	}
	if f.count("SELECT COUNT(*) FROM media_streams WHERE stream_type = 'audio' AND language IN ('spa', 'eng')") < 2 {
		t.Fatal("idiomas de audio")
	}
	mp4 := f.file(paths[0], "mp4_h264_aac.mp4")
	if f.count("SELECT COUNT(*) FROM subtitles WHERE media_file_id = ? AND language = 'es'", mp4.ID) != 1 {
		t.Fatal("subtítulo externo del fixture")
	}
}

func TestErrorDeLecturaNoEsAusencia(t *testing.T) {
	// Un archivo que desaparece entre el recorrido y la huella se omite sin
	// guardarse: el siguiente escaneo lo resolverá.
	f := newFixture(t)
	root := t.TempDir()
	write(t, root, "A (2001).mkv", 100)
	lib, paths := f.library(media.LibraryMovies, root)
	o, err := f.sc.prepare(f.ctx, media.LibraryMovies, task{
		lp: sqlc.LibraryPath{ID: paths[0], LibraryID: lib, Path: root}, f: found{rel: "no-existe.mkv", size: 1},
	}, zerolog.Nop())
	if err != nil || o != nil {
		t.Fatalf("%v %v", o, err)
	}
}

// denyRead impide listar dir y devuelve cómo restaurarlo.
func denyRead(t *testing.T, dir string) func() {
	t.Helper()
	if runtime.GOOS == "windows" {
		user := os.Getenv("USERNAME")
		if out, err := exec.Command("icacls", dir, "/deny", user+":(RD)").CombinedOutput(); err != nil {
			t.Skipf("icacls: %v %s", err, out)
		}
		return func() { _ = exec.Command("icacls", dir, "/remove:d", user).Run() }
	}
	if os.Geteuid() == 0 {
		t.Skip("root lee cualquier carpeta")
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	return func() { _ = os.Chmod(dir, 0o755) }
}

func TestCarpetaIlegibleNoMarcaAusentes(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	write(t, root, "Sub/A (2001).mkv", 100)
	write(t, root, "Sub/A (2001).es.srt", 10)
	write(t, root, "B (2002).mkv", 100)
	lib, paths := f.library(media.LibraryMovies, root)
	f.scan(lib)
	if f.count("SELECT COUNT(*) FROM subtitles") != 1 {
		t.Fatal("subtítulo inicial")
	}

	restore := denyRead(t, filepath.Join(root, "Sub"))
	defer restore()
	if _, err := os.ReadDir(filepath.Join(root, "Sub")); err == nil {
		t.Skip("no se pudo retirar el permiso de lectura")
	}
	f.scan(lib)
	if f.file(paths[0], "Sub/A (2001).mkv").MissingSince != nil {
		t.Fatal("un archivo de una carpeta ilegible no está ausente")
	}
	if f.count("SELECT COUNT(*) FROM subtitles") != 1 {
		t.Fatal("tampoco se pierden sus subtítulos")
	}
}

func TestDosCopiasDeUnAusenteSoloUnaEsMovimiento(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	write(t, root, "Keep (2000).mkv", 100)
	p := write(t, root, "A (2001).mkv", 100)
	lib, paths := f.library(media.LibraryMovies, root)
	f.scan(lib)
	original := f.file(paths[0], "A (2001).mkv")

	data, _ := os.ReadFile(p)
	_ = os.Remove(p)
	_ = os.WriteFile(filepath.Join(root, "Copia1 (2001).mkv"), data, 0o644)
	_ = os.WriteFile(filepath.Join(root, "Copia2 (2001).mkv"), data, 0o644)
	f.scan(lib)

	moved := f.count("SELECT COUNT(*) FROM media_files WHERE id = ? AND missing_since IS NULL", original.ID)
	if moved != 1 || f.count("SELECT COUNT(*) FROM media_files") != 3 {
		t.Fatalf("una copia hereda el id y la otra es nueva: movido %d, total %d", moved, f.count("SELECT COUNT(*) FROM media_files"))
	}
	f.file(paths[0], "Copia1 (2001).mkv")
	f.file(paths[0], "Copia2 (2001).mkv")
	f.scan(lib)
	if f.count("SELECT COUNT(*) FROM media_files WHERE probe_status = 'done'") != 3 {
		t.Fatal("la copia guardada como pendiente se analiza en el siguiente escaneo")
	}
}

func TestProfundidadYCarpetasDeSistema(t *testing.T) {
	f := newFixture(t)
	f.sc.opts.MaxDepth = 2
	root := t.TempDir()
	write(t, root, "a/A (2001).mkv", 100)
	write(t, root, "a/b/B (2002).mkv", 100)
	write(t, root, "a/b/c/C (2003).mkv", 100)
	write(t, root, "$RECYCLE.BIN/D (2004).mkv", 100)
	write(t, root, "System Volume Information/E (2005).mkv", 100)
	lib, _ := f.library(media.LibraryMovies, root)
	f.scan(lib)
	if n := f.count("SELECT COUNT(*) FROM media_files"); n != 2 {
		t.Fatalf("archivos = %d, se esperaban los dos primeros niveles", n)
	}
}

// Tamaño y fecha se comprueban por separado: cualquiera de los dos basta.
func TestCambioDeTamanoOFechaSeReanaliza(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	p := write(t, root, "A (2001).mkv", 100)
	lib, _ := f.library(media.LibraryMovies, root)
	f.scan(lib)
	info, _ := os.Stat(p)

	write(t, root, "A (2001).mkv", 150)
	_ = os.Chtimes(p, info.ModTime(), info.ModTime())
	calls := f.prober.calls.Load()
	f.scan(lib)
	if f.prober.calls.Load() != calls+1 {
		t.Fatal("otro tamaño con la misma fecha se reanaliza")
	}

	info, _ = os.Stat(p)
	write(t, root, "A (2001).mkv", 150)
	later := info.ModTime().Add(time.Hour)
	_ = os.Chtimes(p, later, later)
	calls = f.prober.calls.Load()
	f.scan(lib)
	if f.prober.calls.Load() != calls+1 {
		t.Fatal("el mismo tamaño con otra fecha se reanaliza")
	}
}

// Un nombre como «Serie 01 - Título» se identifica sin SxxEyy, y con el
// título del archivo hasta que haya metadatos.
func TestSerieConNumeroSuelto(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	write(t, root, "Samurai X 01 - El batusai Himura.mp4", 100)
	write(t, root, "Samurai X 02 - Yahiko Myoujin.mp4", 100)
	write(t, root, "Samurai X 10 - Final.mp4", 100)
	lib, _ := f.library(media.LibraryTV, root)
	f.scan(lib)
	if f.count("SELECT COUNT(*) FROM media_items WHERE type = 'series' AND title = 'Samurai X'") != 1 ||
		f.count("SELECT COUNT(*) FROM media_items WHERE type = 'season'") != 1 ||
		f.count("SELECT COUNT(*) FROM media_items WHERE type = 'episode'") != 3 {
		t.Fatal("una serie, una temporada y tres episodios")
	}
	if f.count("SELECT COUNT(*) FROM media_items WHERE type = 'episode' AND episode_number = 1 AND title = 'El batusai Himura' AND sort_title IS NOT NULL") != 1 {
		t.Fatal("el episodio toma el título del archivo")
	}
}

// Los archivos que se analizaron cuando el identificador aún no los entendía
// se identifican en el siguiente escaneo, sin volver a lanzar FFprobe.
func TestReescaneoIdentificaLoQueQuedoSinElemento(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	write(t, root, "Samurai X 01 - Uno.mp4", 100)
	write(t, root, "Samurai X 02 - Dos.mp4", 100)
	write(t, root, "Extras/Making of.mp4", 100)
	lib, _ := f.library(media.LibraryTV, root)
	f.scan(lib)
	// Estado de una versión anterior: analizados, sin elementos.
	f.exec("DELETE FROM media_items")
	if f.count("SELECT COUNT(*) FROM media_file_items") != 0 {
		t.Fatal("la cascada debía vaciar los enlaces")
	}
	calls := f.prober.calls.Load()
	f.advance(time.Minute)
	f.scan(lib)
	if f.prober.calls.Load() != calls {
		t.Fatal("identificar no debe lanzar FFprobe")
	}
	if f.count("SELECT COUNT(*) FROM media_items WHERE type = 'episode'") != 2 {
		t.Fatal("los dos episodios debían identificarse")
	}
	// Lo que sigue sin entenderse no se duplica ni falla al repetir.
	f.advance(time.Minute)
	f.scan(lib)
	if f.count("SELECT COUNT(*) FROM media_items WHERE type = 'episode'") != 2 {
		t.Fatal("repetir no debe duplicar")
	}
}

func (f *fixture) exec(query string, args ...any) {
	f.t.Helper()
	if _, err := f.db.Write.ExecContext(f.ctx, query, args...); err != nil {
		f.t.Fatal(err)
	}
}
