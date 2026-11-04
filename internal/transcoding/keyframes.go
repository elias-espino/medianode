package transcoding

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// indexTimeout limita la lectura de un archivo enorme en una red lenta. ADR
// 0004 estima 8 minutos para 50 GB a 1 Gb/s.
const indexTimeout = 30 * time.Minute

// Index son los keyframes de la pista de vídeo de un archivo, con el tamaño y
// la fecha con que se calcularon: si el archivo cambia, el índice no vale.
type Index struct {
	Size        int64   `json:"size"`
	ModifiedAt  string  `json:"modified_at"`
	DurationUS  int64   `json:"duration_us"`
	KeyframesUS []int64 `json:"keyframes_us"`
}

// Indexer calcula índices en segundo plano, de uno en uno, y los guarda en
// {dir}/{file_id}.json. Nunca se calcula al crear una sesión (ADR 0004).
type Indexer struct {
	dir     string
	ffprobe string
	log     zerolog.Logger

	mu      sync.Mutex
	pending map[string]bool
	queue   chan indexJob
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

type indexJob struct {
	fileID, path string
	size         int64
	modifiedAt   string
}

// NewIndexer crea el indexador.
func NewIndexer(dir, ffprobe string, log zerolog.Logger) *Indexer {
	return &Indexer{dir: dir, ffprobe: ffprobe, log: log, pending: map[string]bool{}, queue: make(chan indexJob, 256)}
}

func (x *Indexer) file(fileID string) string { return filepath.Join(x.dir, fileID+".json") }

// Get devuelve el índice si existe y corresponde al archivo actual.
func (x *Indexer) Get(fileID string, size int64, modifiedAt string) *Index {
	data, err := os.ReadFile(x.file(fileID))
	if err != nil {
		return nil
	}
	var idx Index
	if json.Unmarshal(data, &idx) != nil || idx.Size != size || idx.ModifiedAt != modifiedAt || len(idx.KeyframesUS) == 0 {
		return nil
	}
	return &idx
}

// Request pone el archivo en la cola si no hay índice válido ni está ya
// pendiente. Nunca bloquea: con la cola llena, se pedirá en otra sesión.
func (x *Indexer) Request(fileID, path string, size int64, modifiedAt string) {
	if x.Get(fileID, size, modifiedAt) != nil {
		return
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.pending[fileID] {
		return
	}
	select {
	case x.queue <- indexJob{fileID: fileID, path: path, size: size, modifiedAt: modifiedAt}:
		x.pending[fileID] = true
	default:
	}
}

// Name implementa lifecycle.Component.
func (x *Indexer) Name() string { return "keyframes" }

// Start lanza el trabajador en segundo plano.
func (x *Indexer) Start(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	x.cancel = cancel
	x.wg.Go(func() { x.Run(runCtx) })
	return nil
}

// Stop cancela el cálculo en curso: ffprobe se mata con su contexto.
func (x *Indexer) Stop(context.Context) error {
	if x.cancel != nil {
		x.cancel()
	}
	x.wg.Wait()
	return nil
}

// Run atiende la cola hasta que se cancela ctx.
func (x *Indexer) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-x.queue:
			x.build(ctx, j)
			x.mu.Lock()
			delete(x.pending, j.fileID)
			x.mu.Unlock()
		}
	}
}

func (x *Indexer) build(ctx context.Context, j indexJob) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, indexTimeout)
	defer cancel()
	kf, dur, err := BuildIndex(ctx, x.ffprobe, j.path)
	if err != nil {
		if ctx.Err() == nil {
			x.log.Warn().Err(err).Str("file_id", j.fileID).Msg("no se pudo calcular el índice de keyframes")
		}
		return
	}
	data, _ := json.Marshal(Index{Size: j.size, ModifiedAt: j.modifiedAt, DurationUS: dur, KeyframesUS: kf})
	if err := os.MkdirAll(x.dir, 0o750); err != nil {
		x.log.Error().Err(err).Msg("no se pudo crear el directorio de índices")
		return
	}
	tmp := x.file(j.fileID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		x.log.Error().Err(err).Msg("no se pudo guardar el índice")
		return
	}
	if err := os.Rename(tmp, x.file(j.fileID)); err != nil {
		x.log.Error().Err(err).Msg("no se pudo guardar el índice")
		return
	}
	x.log.Info().Str("file_id", j.fileID).Int("keyframes", len(kf)).Dur("duracion", time.Since(start)).
		Msg("índice de keyframes calculado")
}

// BuildIndex lee los paquetes de la primera pista de vídeo sin decodificar
// (ADR 0004) y devuelve los keyframes y la duración, en microsegundos.
func BuildIndex(ctx context.Context, ffprobe, path string) ([]int64, int64, error) {
	cmd := exec.CommandContext(ctx, ffprobe, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "packet=pts_time,flags:format=duration", "-of", "csv=p=0", "file:"+path)
	out, err := cmd.Output()
	if err != nil {
		return nil, 0, fmt.Errorf("leer paquetes: %w", err)
	}
	kf, dur := parsePackets(out)
	if len(kf) == 0 {
		return nil, 0, errors.New("el archivo no tiene keyframes legibles")
	}
	return kf, dur, nil
}

// parsePackets lee «pts_time,flags» por paquete y una última línea con la
// duración. Los paquetes sin tiempo se ignoran.
func parsePackets(out []byte) ([]int64, int64) {
	var kf []int64
	var dur int64
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		fields := strings.Split(strings.TrimSpace(sc.Text()), ",")
		switch {
		case len(fields) >= 2:
			if !strings.HasPrefix(fields[1], "K") {
				continue
			}
			if us, ok := toUS(fields[0]); ok && us >= 0 {
				kf = append(kf, us)
			}
		case len(fields) == 1:
			if us, ok := toUS(fields[0]); ok {
				dur = us
			}
		}
	}
	slices.Sort(kf)
	return slices.Compact(kf), dur
}

// toUS convierte los segundos de FFprobe, con seis decimales, sin pasar por
// coma flotante: «36.520000» son 36 520 000 µs exactos.
func toUS(s string) (int64, bool) {
	whole, frac, _ := strings.Cut(strings.TrimSpace(s), ".")
	if strings.HasPrefix(whole, "-") {
		return 0, false
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, false
	}
	frac = (frac + "000000")[:6]
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, false
	}
	return w*1_000_000 + f, true
}
