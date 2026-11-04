package scanner

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/rs/zerolog"

	"medianode/internal/core"
	"medianode/internal/media"
	"medianode/internal/storage"
)

// Watcher lanza escaneos cuando cambian los archivos y, cada cierto tiempo,
// una reconciliación completa. Los eventos del sistema son una optimización:
// en SMB, NFS o discos extraíbles pueden perderse, y la reconciliación los
// cubre (§7.3).
type Watcher struct {
	sc        *Scanner
	log       zerolog.Logger
	Debounce  time.Duration // silencio necesario antes de escanear
	Reconcile time.Duration // intervalo de la reconciliación completa
	refresh   chan struct{}
	tick      time.Duration
}

// NewWatcher crea el vigilante.
func NewWatcher(sc *Scanner, debounce, reconcile time.Duration, log zerolog.Logger) *Watcher {
	return &Watcher{
		sc: sc, log: log.With().Str("module", "watcher").Logger(),
		Debounce: debounce, Reconcile: reconcile,
		refresh: make(chan struct{}, 1), tick: time.Second,
	}
}

// Refresh pide releer las rutas vigiladas, por ejemplo tras añadir una.
func (w *Watcher) Refresh() {
	select {
	case w.refresh <- struct{}{}:
	default:
	}
}

// pending acumula los cambios de una biblioteca hasta que se calman.
type pending struct {
	last  time.Time
	sizes map[string]int64 // tamaño visto en el último evento de cada archivo
}

// Run vigila hasta que ctx se cancela.
func (w *Watcher) Run(ctx context.Context) error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer fsw.Close()

	roots := w.watchAll(ctx, fsw)
	waiting := map[string]*pending{}
	ticker := time.NewTicker(w.tick)
	defer ticker.Stop()
	reconcile := time.NewTicker(w.Reconcile)
	defer reconcile.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-w.refresh:
			roots = w.watchAll(ctx, fsw)
		case <-reconcile.C:
			roots = w.watchAll(ctx, fsw)
			w.reconcileAll(ctx)
		case err := <-fsw.Errors:
			w.handleError(roots, waiting, err)
		case ev := <-fsw.Events:
			w.handle(fsw, roots, waiting, ev)
		case now := <-ticker.C:
			w.fire(waiting, now)
		}
	}
}

// handleError atiende los fallos del vigilante. Si se desbordó el búfer de
// eventos, algo cambió sin saber qué: se escanean todas las bibliotecas
// vigiladas tras el silencio habitual, en lugar de esperar a la reconciliación.
func (w *Watcher) handleError(roots map[string][]string, waiting map[string]*pending, err error) {
	w.log.Warn().Err(err).Msg("error del vigilante")
	if !errors.Is(err, fsnotify.ErrEventOverflow) {
		return
	}
	now := time.Now()
	for _, ids := range roots {
		for _, id := range ids {
			if waiting[id] == nil {
				waiting[id] = &pending{sizes: map[string]int64{}}
			}
			waiting[id].last = now
		}
	}
}

func (w *Watcher) handle(fsw *fsnotify.Watcher, roots map[string][]string, waiting map[string]*pending, ev fsnotify.Event) {
	if ev.Op == fsnotify.Chmod {
		return
	}
	var libs []string
	for root, ids := range roots {
		if storage.Within(root, ev.Name) {
			libs = append(libs, ids...)
		}
	}
	if len(libs) == 0 {
		return
	}
	size := int64(-1)
	if info, err := os.Stat(ev.Name); err == nil {
		if info.IsDir() && ev.Has(fsnotify.Create) {
			w.addTree(fsw, ev.Name)
		}
		size = info.Size()
	}
	now := time.Now()
	for _, id := range libs {
		p := waiting[id]
		if p == nil {
			p = &pending{sizes: map[string]int64{}}
			waiting[id] = p
		}
		p.last = now
		p.sizes[ev.Name] = size
	}
}

// fire escanea las bibliotecas sin eventos durante Debounce cuyos archivos ya
// no cambian de tamaño: una copia en curso no debe analizarse a medias.
func (w *Watcher) fire(waiting map[string]*pending, now time.Time) {
	for id, p := range waiting {
		if now.Sub(p.last) < w.Debounce {
			continue
		}
		stable := true
		for name, size := range p.sizes {
			current := int64(-1)
			if info, err := os.Stat(name); err == nil {
				current = info.Size()
			}
			if current != size {
				p.sizes[name] = current
				stable = false
			}
		}
		if !stable {
			p.last = now
			continue
		}
		delete(waiting, id)
		w.start(id, media.TriggerWatcher)
	}
}

func (w *Watcher) reconcileAll(ctx context.Context) {
	ids, err := w.sc.db.R.ListEnabledLibraryIDs(ctx)
	if err != nil {
		w.log.Error().Err(err).Msg("no se pudieron leer las bibliotecas")
		return
	}
	for _, id := range ids {
		w.start(id, media.TriggerScheduled)
	}
}

func (w *Watcher) start(libraryID string, trigger media.ScanTrigger) {
	_, err := w.sc.Start(libraryID, trigger)
	if err != nil && core.CodeOf(err) != core.CodeNotFound {
		w.log.Warn().Err(err).Str("library_id", libraryID).Msg("no se pudo iniciar el escaneo")
	}
}

// watchAll sustituye las vigilancias por las de las rutas actuales y devuelve
// cada raíz con las bibliotecas que la usan. Dos bibliotecas pueden compartir
// raíz.
func (w *Watcher) watchAll(ctx context.Context, fsw *fsnotify.Watcher) map[string][]string {
	for _, name := range fsw.WatchList() {
		_ = fsw.Remove(name)
	}
	paths, err := w.sc.db.R.ListEnabledLibraryPaths(ctx)
	if err != nil {
		w.log.Error().Err(err).Msg("no se pudieron leer las rutas")
		return nil
	}
	roots := map[string][]string{}
	for _, lp := range paths {
		if _, ok := roots[lp.Path]; !ok && storage.RootAvailable(lp.Path) {
			w.addTree(fsw, lp.Path)
		}
		roots[lp.Path] = append(roots[lp.Path], lp.LibraryID)
	}
	return roots
}

// addTree vigila dir y sus subcarpetas: fsnotify no es recursivo en todos los
// sistemas.
func (w *Watcher) addTree(fsw *fsnotify.Watcher, dir string) {
	depth := strings.Count(filepath.Clean(dir), string(filepath.Separator))
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil //nolint:nilerr // una carpeta ilegible no detiene el recorrido: se vigila el resto
		}
		name := d.Name()
		if p != dir && (strings.HasPrefix(name, ".") || skippedDirs[strings.ToLower(name)]) {
			return fs.SkipDir
		}
		if strings.Count(p, string(filepath.Separator))-depth > w.sc.opts.MaxDepth {
			return fs.SkipDir
		}
		if err := fsw.Add(p); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return fs.SkipDir
			}
			// Suele ser el límite de vigilancias del sistema: insistir solo
			// llenaría el log. La reconciliación cubre el resto.
			w.log.Warn().Err(err).Str("ruta", p).Msg("no se pudo vigilar; se deja de añadir carpetas")
			return fs.SkipAll
		}
		return nil
	})
}
