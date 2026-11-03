package scanner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/rs/zerolog"

	"medianode/internal/media"
)

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func TestWatcherEscaneaTrasUnCambio(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	lib, _ := f.library(media.LibraryMovies, root)
	w := NewWatcher(f.sc, 300*time.Millisecond, time.Hour, zerolog.Nop())
	w.tick = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = w.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	time.Sleep(200 * time.Millisecond) // deja que registre las vigilancias
	write(t, root, "Nueva/Peli (2020).mkv", 100)
	ok := waitFor(t, 10*time.Second, func() bool {
		return f.count("SELECT COUNT(*) FROM media_files") == 1 &&
			f.count("SELECT COUNT(*) FROM scan_jobs WHERE library_id = ? AND trigger = 'watcher' AND status = 'completed'", lib) == 1
	})
	if !ok {
		t.Fatal("el cambio debe disparar un escaneo del vigilante")
	}
	// La carpeta creada después de arrancar también queda vigilada.
	write(t, root, "Nueva/Otra (2021).mkv", 100)
	if !waitFor(t, 10*time.Second, func() bool { return f.count("SELECT COUNT(*) FROM media_files") == 2 }) {
		t.Fatal("las carpetas nuevas deben vigilarse")
	}
}

func TestWatcherEsperaAQueElTamanoSeEstabilice(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	lib, _ := f.library(media.LibraryMovies, root)
	p := write(t, root, "Copia (2020).mkv", 100)
	w := NewWatcher(f.sc, time.Millisecond, time.Hour, zerolog.Nop())

	recent := map[string]*pending{lib: {last: time.Now(), sizes: map[string]int64{p: 100}}}
	w.Debounce = time.Hour
	w.fire(recent, time.Now())
	if len(recent) != 1 {
		t.Fatal("sin el silencio de Debounce aún no se escanea")
	}
	w.Debounce = time.Millisecond

	waiting := map[string]*pending{lib: {last: time.Now().Add(-time.Hour), sizes: map[string]int64{p: 50}}}
	w.fire(waiting, time.Now())
	if _, still := waiting[lib]; !still {
		t.Fatal("si el tamaño cambió, aún no se escanea")
	}
	if _, running := f.sc.Running(lib); running || f.count("SELECT COUNT(*) FROM scan_jobs") != 0 {
		t.Fatal("no debe haberse iniciado ningún escaneo")
	}
	w.fire(waiting, time.Now().Add(time.Second))
	if _, still := waiting[lib]; still {
		t.Fatal("con el tamaño estable se escanea")
	}
	if !waitFor(t, 5*time.Second, func() bool { return f.count("SELECT COUNT(*) FROM media_files") == 1 }) {
		t.Fatal("el escaneo debe indexar el archivo")
	}
}

func TestDesbordeDeEventosProgramaTodasLasBibliotecas(t *testing.T) {
	f := newFixture(t)
	w := NewWatcher(f.sc, time.Second, time.Hour, zerolog.Nop())
	roots := map[string][]string{"a": {"l1", "l2"}, "b": {"l3"}}
	waiting := map[string]*pending{}
	w.handleError(roots, waiting, errors.New("otro error"))
	if len(waiting) != 0 {
		t.Fatal("un error cualquiera no programa escaneos")
	}
	w.handleError(roots, waiting, fsnotify.ErrEventOverflow)
	if len(waiting) != 3 {
		t.Fatalf("un desborde programa todas: %v", waiting)
	}
}

func TestCloseDejaElEscaneoInterrumpido(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	write(t, root, "A (2001).mkv", 100)
	lib, _ := f.library(media.LibraryMovies, root)
	f.prober.gate = make(chan struct{})
	job, err := f.sc.Start(lib, media.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	f.sc.Close()
	row, _ := f.db.R.GetScanJob(f.ctx, job.ID)
	if row.Status != string(media.ScanInterrupted) {
		t.Fatalf("estado = %s", row.Status)
	}
	if _, err := f.sc.Start(lib, media.TriggerManual); err == nil {
		t.Fatal("tras Close no empiezan escaneos")
	}
}

func TestReconciliacionPeriodicaEscaneaLasActivas(t *testing.T) {
	f := newFixture(t)
	on, _ := f.library(media.LibraryMovies, t.TempDir())
	off, _ := f.library(media.LibraryMovies, t.TempDir())
	if _, err := f.db.Write.Exec("UPDATE libraries SET enabled = 0 WHERE id = ?", off); err != nil {
		t.Fatal(err)
	}
	w := NewWatcher(f.sc, time.Hour, 100*time.Millisecond, zerolog.Nop())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = w.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	if !waitFor(t, 5*time.Second, func() bool {
		return f.count("SELECT COUNT(*) FROM scan_jobs WHERE library_id = ? AND trigger = 'scheduled'", on) > 0
	}) {
		t.Fatal("la reconciliación debe escanear las bibliotecas activas")
	}
	if f.count("SELECT COUNT(*) FROM scan_jobs WHERE library_id = ?", off) != 0 {
		t.Fatal("una biblioteca desactivada no se escanea sola")
	}
}

// Criterio de salida de la Fase 4: 10 000 archivos, memoria estable y un
// reescaneo sin cambios en segundos sin lanzar FFprobe.
func TestCriterioDiezMilArchivos(t *testing.T) {
	if testing.Short() {
		t.Skip("prueba larga")
	}
	const total = 10000
	f := newFixture(t)
	f.sc.opts.BatchSize = 200
	root := t.TempDir()
	for i := range total {
		dir := filepath.Join(root, fmt.Sprintf("Carpeta %03d", i%100))
		if i < 100 {
			_ = os.MkdirAll(dir, 0o755)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("Peli %05d (2000).mkv", i)), []byte(fmt.Sprint(i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	lib, _ := f.library(media.LibraryMovies, root)

	start := time.Now()
	first := f.scan(lib)
	firstTook := time.Since(start)
	if first.FilesProcessed != total {
		t.Fatalf("%+v", first)
	}
	heap := func() uint64 {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return m.HeapInuse
	}
	afterFirst := heap()
	calls := f.prober.calls.Load()

	start = time.Now()
	second := f.scan(lib)
	secondTook := time.Since(start)
	for range 3 {
		f.scan(lib)
	}
	afterMore := heap()

	t.Logf("primer escaneo %s, reescaneo %s, heap %d KiB → %d KiB",
		firstTook.Round(time.Millisecond), secondTook.Round(time.Millisecond), afterFirst>>10, afterMore>>10)
	if f.prober.calls.Load() != calls || second.FilesProcessed != 0 {
		t.Fatal("el reescaneo sin cambios no lanza FFprobe")
	}
	if secondTook > 10*time.Second {
		t.Fatalf("el reescaneo debe tardar segundos: %s", secondTook)
	}
	if afterMore > afterFirst+16<<20 {
		t.Fatalf("la memoria crece entre escaneos: %d → %d", afterFirst, afterMore)
	}
}
