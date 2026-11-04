package transcoding

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"

	"medianode/internal/database"
	"medianode/internal/database/sqlc"
	"medianode/internal/events"
	"medianode/internal/ffmpeg"
)

// pipeDirPrefix distingue el directorio de trabajo de un flujo progresivo del
// de una sesión HLS: el recolector no debe borrar el primero mientras corre.
const pipeDirPrefix = "pipe-"

// PipePlan es un flujo progresivo de MPEG-TS para una sesión DLNA (§13.2). A
// diferencia de Plan no hay segmentos: un único trabajo de FFmpeg escribe en la
// respuesta HTTP.
type PipePlan struct {
	SessionID  string
	FileID     string
	Path       string // Absoluta.
	DurationUS int64
	Kind       JobKind // Heavy recodifica el vídeo.
	VideoIndex int
	AudioIndex int // -1 sin audio.
	AudioCopy  bool
	StartMS    int64
	// Solo al recodificar el vídeo.
	Height       int
	VideoBitrate int
	ToneMap      string
	Burn         ffmpeg.BurnKind
	BurnFile     string
	BurnStream   int
	// Abort, si no es nil, se llama cuando el servidor cierra la sesión mientras
	// el flujo sigue abierto. Quien escribe puede estar bloqueado en Write
	// (el televisor en pausa no lee): Abort debe desbloquearlo, por ejemplo con
	// un plazo de escritura vencido.
	Abort func()
}

type pipeJob struct {
	id        string
	sessionID string
	kind      JobKind
	encoder   ffmpeg.Encoder
	started   time.Time
	bandwidth int
	cancel    context.CancelFunc
	startMS   int64
	written   atomic.Int64
	ended     atomic.Bool // La sesión se cerró: no fue el cliente quien se fue.
}

// ErrPipeFailed indica que FFmpeg terminó con error. El flujo no llegó a
// empezar si bytes es cero: quien llama aún puede responder un error HTTP.
var ErrPipeFailed = errors.New("FFmpeg terminó con error")

// flushWriter vacía el buffer de la respuesta tras cada escritura: un flujo en
// directo no puede esperar a que se llene.
type flushWriter struct {
	w io.Writer
	f interface{ Flush() }
}

func (fw flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if fw.f != nil && n > 0 {
		fw.f.Flush()
	}
	return n, err
}

func newFlushWriter(w io.Writer) io.Writer {
	if f, ok := w.(interface{ Flush() }); ok {
		return flushWriter{w: w, f: f}
	}
	return w
}

// StreamTS lanza FFmpeg y copia su salida a w hasta que termina, el cliente
// se va (ctx) o la sesión se cierra. Devuelve los bytes escritos. El cupo se
// reserva aquí, y se libera al acabar el último flujo de la sesión: un salto
// abre otra petición antes de que cierre la anterior.
func (m *Manager) StreamTS(ctx context.Context, p PipePlan, w io.Writer) (int64, error) {
	if err := m.admission.Reserve(p.Kind, p.SessionID); err != nil {
		return 0, ErrCapacity
	}
	m.mu.Lock()
	m.pipeRefs[p.SessionID]++
	m.mu.Unlock()
	defer m.releasePipe(p.SessionID)

	var total int64
	for attempt := 0; ; attempt++ {
		n, retry, err := m.runPipe(ctx, p, w)
		total += n
		if !retry || attempt > 0 {
			return total, err
		}
	}
}

// releasePipe libera el cupo cuando la sesión se queda sin flujos. Se cuenta al
// empezar, antes de que FFmpeg arranque: así un flujo que acaba no libera el
// cupo de otro que acaba de empezar para la misma sesión.
func (m *Manager) releasePipe(sessionID string) {
	m.mu.Lock()
	m.pipeRefs[sessionID]--
	busy := m.pipeRefs[sessionID] > 0
	if !busy {
		delete(m.pipeRefs, sessionID)
	}
	m.mu.Unlock()
	if !busy {
		m.admission.Release(sessionID)
	}
}

func (m *Manager) runPipe(ctx context.Context, p PipePlan, w io.Writer) (written int64, retry bool, err error) {
	id, err := database.NewID()
	if err != nil {
		return 0, false, err
	}
	encoder := m.Encoder()
	dir := filepath.Join(m.opts.Dir, pipeDirPrefix+id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return 0, false, fmt.Errorf("crear directorio del trabajo: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if p.Burn == ffmpeg.BurnText {
		if err := copyFile(p.BurnFile, filepath.Join(dir, burnName)); err != nil {
			return 0, false, fmt.Errorf("copiar el subtítulo: %w", err)
		}
	}
	burn := ffmpeg.Burn{Kind: p.Burn, Stream: p.BurnStream}
	if p.Burn == ffmpeg.BurnText {
		burn.File = burnName
	}
	video := p.Kind == Heavy
	args, err := ffmpeg.BuildPipe(ffmpeg.PipeParams{
		Input:     ffmpeg.Input{Path: p.Path, StartMS: p.StartMS, Video: p.VideoIndex, Audio: p.AudioIndex},
		Audio:     ffmpeg.Audio{Copy: p.AudioCopy},
		CopyVideo: !video, Encoder: encoder, Height: p.Height, VideoBitrate: p.VideoBitrate,
		ToneMap: p.ToneMap, Burn: burn,
	})
	if err != nil {
		return 0, false, fmt.Errorf("construir comando: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(runCtx, m.opts.FFmpeg, args...)
	cmd.Dir = dir
	prepare(cmd)
	cmd.WaitDelay = stopTimeout
	stderr := newRing(stderrKeep)
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, false, err
	}
	if err := cmd.Start(); err != nil {
		return 0, false, fmt.Errorf("arrancar FFmpeg: %w", err)
	}
	if err := attach(cmd.Process); err != nil {
		m.log.Warn().Err(err).Msg("no se pudo vincular FFmpeg al servidor")
	}

	jobKind, profile := "remux", "ts-copy"
	switch {
	case video:
		jobKind, profile = "video", fmt.Sprintf("ts-h264-%dp-%dk", p.Height, p.VideoBitrate/1000)
	case !p.AudioCopy && p.AudioIndex >= 0:
		jobKind, profile = "audio", "ts-copy-aac"
	}
	job := &pipeJob{id: id, sessionID: p.SessionID, kind: p.Kind, started: time.Now(), cancel: cancel, startMS: p.StartMS}
	if video {
		job.encoder = encoder
		job.bandwidth = p.VideoBitrate + 192_000
	}
	m.mu.Lock()
	m.pipes[id] = job
	m.mu.Unlock()
	_ = m.db.Q.CreateTranscodeJob(context.Background(), sqlc.CreateTranscodeJobParams{
		ID: id, PlaybackSessionID: p.SessionID, JobKind: jobKind, InputFileID: p.FileID,
		OutputProfile: profile, StartPositionMs: p.StartMS, StartedAt: ptr(database.Now()),
	})
	m.bus.Publish(events.Event{Name: "transcode.started", AdminOnly: true, Data: map[string]any{
		"job_id": id, "session_id": p.SessionID, "kind": jobKind,
	}})
	m.log.Info().Str("job_id", id).Str("session_id", p.SessionID).Str("perfil", profile).
		Str("codificador", string(job.encoder)).Msg("flujo MPEG-TS iniciado")

	if p.Abort != nil {
		go func() {
			<-runCtx.Done()
			if job.ended.Load() {
				p.Abort()
			}
		}()
	}
	copyErr := copyOut(stdout, newFlushWriter(w), &job.written)
	if copyErr != nil {
		cancel() // El cliente se fue: no hay a quién enviar.
	}
	waitErr := cmd.Wait()
	m.mu.Lock()
	delete(m.pipes, id)
	m.mu.Unlock()

	written = job.written.Load()
	// Lo paró el cliente o el servidor: no es un fallo de FFmpeg.
	stopped := runCtx.Err() != nil
	status, code, msg := "completed", (*string)(nil), (*string)(nil)
	switch {
	case stopped:
		status = "stopped"
	case waitErr != nil:
		status = "failed"
		c := "TRANSCODE_INITIALIZATION_FAILED"
		if written > 0 {
			c = "TRANSCODE_FAILED"
		}
		tail := stderr.String()
		code, msg = &c, &tail
		m.failures.Add(1)
		m.log.Error().Err(waitErr).Str("job_id", id).Str("stderr", tail).Msg("FFmpeg terminó con error")
		m.bus.Publish(events.Event{Name: "transcode.failed", AdminOnly: true, Data: map[string]any{
			"job_id": id, "session_id": p.SessionID, "error_code": c,
		}})
		err = fmt.Errorf("%w: %w", ErrPipeFailed, waitErr)
		if written == 0 && job.encoder != "" && job.encoder != ffmpeg.X264 {
			m.mu.Lock()
			if m.encoder == job.encoder {
				m.encoder = ffmpeg.X264
				m.log.Warn().Str("codificador", string(job.encoder)).Msg("el codificador de hardware falló; se usará libx264")
			}
			m.mu.Unlock()
			retry = true
		}
	}
	progress := 0.0
	if p.DurationUS > 0 {
		progress = min(100, float64(p.StartMS)*1000*100/float64(p.DurationUS))
	}
	_ = m.db.Q.FinishTranscodeJob(context.Background(), sqlc.FinishTranscodeJobParams{
		ID: id, Status: status, Progress: progress, FinishedAt: ptr(database.Now()), ErrorCode: code, ErrorMessage: msg,
	})
	return written, retry, err
}

// copyOut copia r en w y cuenta lo escrito. Termina con el primer error de
// escritura: el cliente cerró la conexión.
func copyOut(r io.Reader, w io.Writer, n *atomic.Int64) error {
	buf := make([]byte, 64<<10)
	for {
		k, err := r.Read(buf)
		if k > 0 {
			wn, werr := w.Write(buf[:k])
			n.Add(int64(wn))
			if werr != nil {
				return werr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return nil // FFmpeg cerró su salida o la cancelaron; Wait dirá cómo terminó.
		}
	}
}

// endPipes para los flujos de una sesión que se cierra.
func (m *Manager) endPipes(sessionID string) {
	m.mu.Lock()
	var cancels []context.CancelFunc
	for _, j := range m.pipes {
		if j.sessionID == sessionID {
			j.ended.Store(true)
			cancels = append(cancels, j.cancel)
		}
	}
	m.mu.Unlock()
	for _, c := range cancels {
		c()
	}
}

// pipeJobs son los flujos progresivos activos, para /admin/transcodes.
func (m *Manager) pipeJobs() []JobInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]JobInfo, 0, len(m.pipes))
	for _, j := range m.pipes {
		out = append(out, JobInfo{
			ID: j.id, SessionID: j.sessionID, Kind: j.kind, Encoder: string(j.encoder),
			StartedAt: j.started, Bandwidth: j.bandwidth, PositionUS: j.startMS * 1000,
		})
	}
	return out
}

// pipeDirActive indica si un directorio del recolector es de un flujo en curso.
func (m *Manager) pipeDirActive(name string) bool {
	if len(name) <= len(pipeDirPrefix) || name[:len(pipeDirPrefix)] != pipeDirPrefix {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.pipes[name[len(pipeDirPrefix):]]
	return ok
}
