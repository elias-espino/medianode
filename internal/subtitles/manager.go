package subtitles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"medianode/internal/ffmpeg"
)

// ErrNoExtractor indica que no hay FFmpeg para extraer o convertir.
var ErrNoExtractor = errors.New("sin FFmpeg no se pueden extraer subtítulos")

// maxParallel acota las extracciones simultáneas: cada una lee el archivo de
// vídeo entero, y abrir una película por red no debe multiplicarse.
const maxParallel = 2

// Track es una pista de texto que se convierte: interna de un archivo de
// vídeo, o un archivo de subtítulos externo.
type Track struct {
	FileID  string // Archivo de vídeo al que pertenece.
	TrackID string // Identificador de la pista o del subtítulo externo.
	Input   string // Ruta absoluta de lo que lee FFmpeg.
	Stream  int    // Índice de la pista en Input; 0 en un archivo externo.
	// Size y ModifiedAt son de Input: si cambian, la copia en caché no vale.
	Size       int64
	ModifiedAt string
}

// Manager convierte pistas de texto a WebVTT (clientes y HLS) o a ASS (para
// grabarlas en la imagen) y guarda el resultado en
// {dir}/{file_id}/{track_id}.{vtt|ass}. La extracción corre fuera de la
// petición que la pide: si el cliente se va, sigue y la siguiente la encuentra.
type Manager struct {
	ffmpeg string
	dir    string
	log    zerolog.Logger
	// extract es ffmpeg.ExtractSubtitle; las pruebas lo sustituyen.
	extract func(ctx context.Context, ffmpegPath, input string, stream int, format, out string) error
	sem     chan struct{}

	mu      sync.Mutex
	flights map[string]*flight
}

type flight struct {
	done chan struct{}
	err  error
}

// NewManager crea el gestor. Sin ruta de FFmpeg, Get devuelve ErrNoExtractor.
func NewManager(ffmpegPath, dir string, log zerolog.Logger) *Manager {
	return &Manager{ffmpeg: ffmpegPath, dir: dir, log: log, extract: ffmpeg.ExtractSubtitle,
		sem: make(chan struct{}, maxParallel), flights: map[string]*flight{}}
}

type meta struct {
	Size       int64  `json:"size"`
	ModifiedAt string `json:"modified_at"`
}

func simpleName(s string) bool {
	return s != "" && filepath.Base(s) == s && filepath.IsLocal(s)
}

func (m *Manager) paths(t Track, format string) (out, metaPath string, err error) {
	if !simpleName(t.FileID) || !simpleName(t.TrackID) {
		return "", "", fmt.Errorf("identificador no válido: %q %q", t.FileID, t.TrackID)
	}
	ext := ".vtt"
	switch format {
	case ffmpeg.FormatWebVTT:
	case ffmpeg.FormatASS:
		ext = ".ass"
	default:
		return "", "", fmt.Errorf("formato no admitido: %q", format)
	}
	out = filepath.Join(m.dir, t.FileID, t.TrackID+ext)
	return out, out + ".json", nil
}

// SetExtractor sustituye a FFmpeg por otra función. Solo para pruebas.
func (m *Manager) SetExtractor(fn func(ctx context.Context, ffmpegPath, input string, stream int, format, out string) error) {
	m.extract = fn
}

// Cached devuelve la ruta si ya hay una copia vigente, sin extraer nada.
func (m *Manager) Cached(t Track, format string) (string, bool) {
	out, metaPath, err := m.paths(t, format)
	if err != nil {
		return "", false
	}
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return "", false
	}
	var mt meta
	if json.Unmarshal(data, &mt) != nil || mt.Size != t.Size || mt.ModifiedAt != t.ModifiedAt {
		return "", false
	}
	if _, err := os.Stat(out); err != nil {
		return "", false
	}
	return out, true
}

// Get devuelve la pista convertida, extrayéndola si hace falta y esperando a
// que termine o a que ctx se cancele. Varias peticiones de la misma pista
// comparten una sola extracción.
func (m *Manager) Get(ctx context.Context, t Track, format string) (string, error) {
	if m.ffmpeg == "" {
		return "", ErrNoExtractor
	}
	out, metaPath, err := m.paths(t, format)
	if err != nil {
		return "", err
	}
	if p, ok := m.Cached(t, format); ok {
		return p, nil
	}
	m.mu.Lock()
	fl, running := m.flights[out]
	if !running {
		fl = &flight{done: make(chan struct{})}
		m.flights[out] = fl
		// Independiente de la petición: si el cliente se va, la extracción
		// termina y la siguiente la encuentra en caché.
		go m.run(fl, out, metaPath, t, format) //nolint:gosec // a propósito sin el contexto de la petición, ver arriba
	}
	m.mu.Unlock()
	select {
	case <-fl.done:
		if fl.err != nil {
			return "", fl.err
		}
		return out, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Prefetch empieza a extraer sin esperar. Al crear una sesión, así el
// subtítulo ya está cuando el reproductor lo pide.
func (m *Manager) Prefetch(t Track, format string) {
	if m.ffmpeg == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if _, err := m.Get(ctx, t, format); err != nil && ctx.Err() == nil {
			m.log.Warn().Err(err).Str("file_id", t.FileID).Str("track_id", t.TrackID).Msg("no se pudo extraer el subtítulo")
		}
	}()
}

func (m *Manager) run(fl *flight, out, metaPath string, t Track, format string) {
	defer func() {
		m.mu.Lock()
		delete(m.flights, out)
		m.mu.Unlock()
		close(fl.done)
	}()
	m.sem <- struct{}{}
	defer func() { <-m.sem }()
	// Otro Get pudo terminar mientras esperaba el turno.
	if _, ok := m.Cached(t, format); ok {
		return
	}
	_ = os.Remove(metaPath)
	if err := m.extract(context.Background(), m.ffmpeg, t.Input, t.Stream, format, out); err != nil {
		fl.err = err
		return
	}
	data, err := json.Marshal(meta{Size: t.Size, ModifiedAt: t.ModifiedAt})
	if err == nil {
		err = os.WriteFile(metaPath, data, 0o600)
	}
	if err != nil {
		fl.err = err
		_ = os.Remove(out)
	}
}
