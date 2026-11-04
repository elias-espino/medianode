// Package transcoding gestiona los trabajos de FFmpeg de las sesiones que no
// son Direct Play (§10): HLS con playlist sintética, reinicios al saltar,
// cupos y limpieza del disco.
package transcoding

import (
	"bufio"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"medianode/internal/database"
	"medianode/internal/database/sqlc"
	"medianode/internal/events"
	"medianode/internal/ffmpeg"
)

// Errores de Segment.
var (
	ErrNoStream       = errors.New("la sesión no tiene trabajo de transcodificación")
	ErrBadName        = errors.New("nombre de recurso no permitido")
	ErrSegmentTimeout = errors.New("el segmento no estuvo listo a tiempo")
)

const (
	pollEvery     = 100 * time.Millisecond
	stopTimeout   = 5 * time.Second
	progressEvery = 5 * time.Second
	stderrKeep    = 8 << 10
	gcEvery       = time.Minute
	orphanAge     = 2 * time.Minute
)

// Nombres que se aceptan en /stream/session/{id}/{token}/{path}: nada más
// llega al disco.
var reSegment = regexp.MustCompile(`^seg_(\d{5})\.(m4s|ts)$`)

// Options configura el gestor.
type Options struct {
	FFmpeg          string
	Dir             string // data/transcodes
	SegmentSeconds  int
	RestartSegments int // Un salto más allá de esto reinicia FFmpeg.
	WaitTimeout     time.Duration
	MaxDiskBytes    int64
	Encoder         ffmpeg.Encoder
}

// Plan es lo que hay que producir para una sesión.
type Plan struct {
	SessionID  string
	FileID     string
	Path       string // Absoluta.
	DurationUS int64
	StartUS    int64
	Kind       JobKind // Heavy recodifica el vídeo.
	VideoIndex int
	AudioIndex int // -1 sin audio.
	AudioCopy  bool
	// Solo al recodificar el vídeo.
	Height       int
	VideoBitrate int
	// Solo al copiarlo; nil sin índice: playlist EVENT.
	Keyframes *Index
	Variant   Variant
}

// JobInfo es la vista de un trabajo para /admin/transcodes.
type JobInfo struct {
	ID           string
	SessionID    string
	Kind         JobKind
	Encoder      string
	StartSegment int
	Segment      int // Último segmento terminado.
	PositionUS   int64
	StartedAt    time.Time
}

type job struct {
	id         string
	gen        int
	startSeg   int
	encoder    ffmpeg.Encoder
	cmd        *exec.Cmd
	cancel     context.CancelFunc
	done       chan struct{}
	err        error
	stopped    bool // Lo paró el servidor: no es un fallo.
	stderr     *ring
	positionUS int64
	started    time.Time
	produced   bool // Llegó a terminar algún segmento.
}

type stream struct {
	mu        sync.Mutex
	plan      Plan
	dir       string
	ext       string
	segs      []Segment // nil en modo EVENT.
	job       *job
	gen       int
	genStart  map[int]int // Primer segmento de cada trabajo, por generación.
	completed map[int]bool
	lastAsked int
}

// Manager coordina los trabajos.
type Manager struct {
	opts      Options
	db        *database.DB
	bus       *events.Bus
	log       zerolog.Logger
	admission *Admission

	mu      sync.Mutex
	streams map[string]*stream
	encoder ffmpeg.Encoder // Pasa a libx264 si el de hardware falla al arrancar.

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewManager crea el gestor.
func NewManager(opts Options, db *database.DB, bus *events.Bus, adm *Admission, log zerolog.Logger) *Manager {
	if opts.SegmentSeconds <= 0 {
		opts.SegmentSeconds = 6
	}
	if opts.RestartSegments <= 0 {
		opts.RestartSegments = 5
	}
	if opts.WaitTimeout <= 0 {
		opts.WaitTimeout = 20 * time.Second
	}
	if opts.Encoder == "" {
		opts.Encoder = ffmpeg.X264
	}
	return &Manager{opts: opts, db: db, bus: bus, log: log, admission: adm, streams: map[string]*stream{}, encoder: opts.Encoder}
}

// Dir es el directorio de trabajo: un subdirectorio por sesión.
func (m *Manager) Dir() string { return m.opts.Dir }

// Admission devuelve el control de cupos.
func (m *Manager) Admission() *Admission { return m.admission }

// Encoder devuelve el codificador efectivo.
func (m *Manager) Encoder() ffmpeg.Encoder {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.encoder
}

// SegmentSeconds es la duración nominal de los segmentos.
func (m *Manager) SegmentSeconds() int { return m.opts.SegmentSeconds }

// Name implementa lifecycle.Component.
func (m *Manager) Name() string { return "transcoding" }

// Start lanza el recolector.
func (m *Manager) Start(ctx context.Context) error {
	if err := os.MkdirAll(m.opts.Dir, 0o750); err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	m.cancel = cancel
	m.wg.Go(func() { m.gcLoop(runCtx) })
	return nil
}

// Stop para todos los trabajos.
func (m *Manager) Stop(context.Context) error {
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
	m.mu.Lock()
	ids := make([]string, 0, len(m.streams))
	for id := range m.streams {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.end(id)
	}
	return nil
}

// Prepare registra la sesión y arranca su primer trabajo en el segmento que
// contiene la posición inicial. El cupo ya debe estar reservado.
func (m *Manager) Prepare(ctx context.Context, p Plan) error {
	st := &stream{plan: p, dir: filepath.Join(m.opts.Dir, p.SessionID), completed: map[int]bool{}, genStart: map[int]int{}}
	switch {
	case p.Kind == Heavy:
		st.ext = "m4s"
		st.segs = EncodeSegments(p.DurationUS, m.opts.SegmentSeconds)
	case p.Keyframes != nil:
		st.ext = "ts"
		st.segs = CopySegments(p.Keyframes.KeyframesUS, p.Keyframes.DurationUS, m.opts.SegmentSeconds)
	default:
		st.ext = "ts"
	}
	if p.Kind == Heavy && len(st.segs) == 0 {
		return errors.New("duración desconocida: no se puede generar la playlist")
	}
	if err := os.MkdirAll(st.dir, 0o750); err != nil {
		return fmt.Errorf("crear directorio del trabajo: %w", err)
	}
	m.mu.Lock()
	m.streams[p.SessionID] = st
	m.mu.Unlock()

	first := 0
	if st.segs != nil {
		first = segmentAt(st.segs, p.StartUS)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return m.startJob(st, first)
}

// segmentAt es el segmento que contiene t.
func segmentAt(segs []Segment, t int64) int {
	for i := len(segs) - 1; i >= 0; i-- {
		if segs[i].StartUS <= t {
			return i
		}
	}
	return 0
}

func (m *Manager) get(sessionID string) (*stream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.streams[sessionID]
	if !ok {
		return nil, ErrNoStream
	}
	return st, nil
}

// Master devuelve el master de la sesión.
func (m *Manager) Master(sessionID string) ([]byte, error) {
	st, err := m.get(sessionID)
	if err != nil {
		return nil, err
	}
	return MasterPlaylist(st.plan.Variant), nil
}

// Media devuelve la playlist de medios: la sintética VOD o, sin índice, la
// EVENT con lo ya generado.
func (m *Manager) Media(sessionID string) ([]byte, error) {
	st, err := m.get(sessionID)
	if err != nil {
		return nil, err
	}
	if st.segs != nil {
		init := ""
		if st.ext == "m4s" {
			init = "init.mp4"
		}
		return MediaPlaylist(st.segs, st.ext, init), nil
	}
	// Playlist EVENT: una sin segmentos hace fallar a los reproductores, así
	// que se espera al primero (o a que el trabajo termine).
	deadline := time.Now().Add(m.opts.WaitTimeout)
	for {
		st.mu.Lock()
		entries, err := readList(filepath.Join(st.dir, listName(st.gen)))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			st.mu.Unlock()
			return nil, err
		}
		finished := st.job != nil && isDone(st.job)
		done := finished && st.job.err == nil
		st.mu.Unlock()
		if len(entries) > 0 || finished || time.Now().After(deadline) {
			return EventPlaylist(entries, st.ext, done), nil
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// File devuelve la ruta de un recurso de la sesión, esperando a que FFmpeg lo
// termine o reiniciándolo si hace falta (lógica de §Fase 8). El nombre debe
// ser init.mp4 o un segmento de la playlist.
func (m *Manager) File(ctx context.Context, sessionID, name string) (string, error) {
	st, err := m.get(sessionID)
	if err != nil {
		return "", err
	}
	if name == "init.mp4" {
		if st.ext != "m4s" {
			return "", ErrBadName
		}
		return m.waitInit(ctx, st)
	}
	match := reSegment.FindStringSubmatch(name)
	if match == nil || match[2] != st.ext {
		return "", ErrBadName
	}
	n, _ := strconv.Atoi(match[1])
	if st.segs != nil && n >= len(st.segs) {
		return "", ErrBadName
	}
	return m.waitSegment(ctx, st, n)
}

func (m *Manager) waitSegment(ctx context.Context, st *stream, n int) (string, error) {
	path := filepath.Join(st.dir, SegmentName(n, st.ext))
	deadline := time.Now().Add(m.opts.WaitTimeout)
	restarts := 0
	for {
		st.mu.Lock()
		st.lastAsked = n
		if m.isComplete(st, n) {
			st.mu.Unlock()
			return path, nil
		}
		// Solo hay reinicios con playlist sintética: sin índice no se sabe
		// dónde empieza cada segmento.
		if st.segs != nil && restarts < 2 && m.needsRestart(st, n) {
			restarts++
			if err := m.restart(st, n); err != nil {
				st.mu.Unlock()
				return "", err
			}
		}
		failed := st.job != nil && isDone(st.job) && st.job.err != nil && !st.job.stopped
		// Sin playlist sintética, un trabajo terminado no va a producir más.
		gone := st.segs == nil && st.job != nil && isDone(st.job)
		st.mu.Unlock()
		if gone {
			return "", ErrBadName
		}
		if failed && restarts >= 2 {
			return "", fmt.Errorf("FFmpeg falló al generar el segmento %d", n)
		}
		if time.Now().After(deadline) {
			return "", ErrSegmentTimeout
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(pollEvery):
		}
	}
}

// needsRestart aplica la lógica del plan: sin trabajo activo, un segmento
// anterior a su inicio o demasiado por delante de lo generado reinicia.
func (m *Manager) needsRestart(st *stream, n int) bool {
	j := st.job
	if j == nil || isDone(j) {
		return true
	}
	if n < j.startSeg {
		return true
	}
	return n > m.current(st)+m.opts.RestartSegments
}

// current es el último segmento terminado del trabajo activo, o el anterior a
// su inicio si aún no hay ninguno.
func (m *Manager) current(st *stream) int {
	j := st.job
	cur := j.startSeg - 1
	for n := j.startSeg; st.completed[n] || m.isComplete(st, n); n++ {
		cur = n
	}
	return cur
}

// isComplete indica si el segmento está terminado. Al recodificar, FFmpeg
// escribe a un temporal y renombra (temp_file): si existe, está completo. El
// muxer segment no lo hace; un segmento solo cuenta cuando aparece en la
// lista CSV de algún trabajo.
func (m *Manager) isComplete(st *stream, n int) bool {
	if st.completed[n] {
		return true
	}
	if st.ext == "m4s" {
		if _, err := os.Stat(filepath.Join(st.dir, SegmentName(n, st.ext))); err == nil {
			st.completed[n] = true
		}
		return st.completed[n]
	}
	for gen := 0; gen <= st.gen; gen++ {
		entries, err := readListNames(filepath.Join(st.dir, listName(gen)))
		if err != nil {
			continue
		}
		for _, name := range entries {
			if mm := reSegment.FindStringSubmatch(name); mm != nil {
				k, _ := strconv.Atoi(mm[1])
				st.completed[k] = true
			}
		}
	}
	return st.completed[n]
}

func (m *Manager) waitInit(ctx context.Context, st *stream) (string, error) {
	deadline := time.Now().Add(m.opts.WaitTimeout)
	for {
		st.mu.Lock()
		if st.job == nil || (isDone(st.job) && st.job.err != nil && !st.job.stopped) {
			if err := m.restart(st, segmentAt(st.segs, st.plan.StartUS)); err != nil {
				st.mu.Unlock()
				return "", err
			}
		}
		// La playlist declara un solo init: vale el del trabajo más reciente,
		// que tiene los mismos parámetros de códec (ADR 0003).
		// FFmpeg crea el archivo al empezar y lo rellena al cerrar el primer
		// segmento: servirlo antes entregaría un init vacío. Solo vale el de
		// un trabajo que ya terminó un segmento y cuyo init tiene contenido.
		for gen := st.gen; gen >= 0; gen-- {
			p := filepath.Join(st.dir, initName(gen))
			if info, err := os.Stat(p); err == nil && info.Size() > 0 && m.isComplete(st, st.genStart[gen]) {
				st.mu.Unlock()
				return p, nil
			}
		}
		st.mu.Unlock()
		if time.Now().After(deadline) {
			return "", ErrSegmentTimeout
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(pollEvery):
		}
	}
}

func initName(gen int) string { return fmt.Sprintf("init_%d.mp4", gen) }
func listName(gen int) string { return fmt.Sprintf("list_%d.csv", gen) }

// restart para el trabajo activo y arranca otro en n. Hay que tener st.mu.
// Mientras se espera a FFmpeg el candado se suelta; si otra petición arrancó
// ya un trabajo, se respeta el suyo y quien llama vuelve a evaluar.
func (m *Manager) restart(st *stream, n int) error {
	old := st.job
	if old != nil {
		m.stopJob(st, old)
		if st.job != old {
			return nil
		}
	}
	st.gen++
	return m.startJob(st, n)
}

// limitFrom calcula hasta dónde debe llegar un trabajo que empieza en n: justo
// antes del primer segmento ya terminado, para no reescribirlo mientras se
// sirve. Cero sin límite.
func (m *Manager) limitFrom(st *stream, n int) int64 {
	if st.segs == nil {
		return 0
	}
	for k := n + 1; k < len(st.segs); k++ {
		if m.isComplete(st, k) {
			return st.segs[k].StartUS - st.segs[n].StartUS
		}
	}
	return 0
}

// startJob arranca FFmpeg en el segmento n. Hay que tener st.mu.
func (m *Manager) startJob(st *stream, n int) error {
	p := st.plan
	encoder := m.Encoder()
	var args []string
	var err error
	jobKind, profile := "remux", "copy-ts"
	in := ffmpeg.Input{Path: p.Path, Video: p.VideoIndex, Audio: p.AudioIndex, LimitUS: m.limitFrom(st, n)}
	audio := ffmpeg.Audio{Copy: p.AudioCopy}
	switch {
	case p.Kind == Heavy:
		in.StartMS = int64(n) * int64(m.opts.SegmentSeconds) * 1000
		jobKind = "video"
		profile = fmt.Sprintf("h264-%dp-%dk", p.Height, p.VideoBitrate/1000)
		args, err = ffmpeg.BuildEncode(ffmpeg.EncodeParams{
			Input: in, Audio: audio, Encoder: encoder, Height: p.Height, VideoBitrate: p.VideoBitrate,
			SegmentSeconds: m.opts.SegmentSeconds, StartSegment: n,
			InitName: initName(st.gen), Playlist: fmt.Sprintf("ffmpeg_%d.m3u8", st.gen),
		})
	case st.segs != nil:
		if !p.AudioCopy && p.AudioIndex >= 0 {
			jobKind = "audio"
		}
		cp := ffmpeg.CopyParams{Input: in, Audio: audio, StartSegment: n, List: listName(st.gen)}
		if n > 0 {
			// El salto, medio milisegundo después del keyframe para no caer en el
			// anterior; los cortes, un milisegundo antes del suyo para no
			// saltárselo. La línea de tiempo se conserva con el mismo
			// desplazamiento.
			cp.SeekUS = st.segs[n].StartUS + 500
			cp.OffsetUS = cp.SeekUS
		}
		for _, s := range st.segs[n+1:] {
			cp.CutsRelUS = append(cp.CutsRelUS, s.StartUS-cp.SeekUS-1000)
		}
		args, err = ffmpeg.BuildCopy(cp)
	default:
		if !p.AudioCopy && p.AudioIndex >= 0 {
			jobKind = "audio"
		}
		args, err = ffmpeg.BuildCopy(ffmpeg.CopyParams{Input: in, Audio: audio, SegmentSeconds: m.opts.SegmentSeconds, List: listName(st.gen)})
	}
	if err != nil {
		return fmt.Errorf("construir comando: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, m.opts.FFmpeg, args...)
	cmd.Dir = st.dir
	prepare(cmd)
	// Cancelar envía la señal de parada; si FFmpeg no termina en
	// stopTimeout, se mata.
	cmd.WaitDelay = stopTimeout
	j := &job{gen: st.gen, startSeg: n, encoder: encoder, cmd: cmd, cancel: cancel,
		done: make(chan struct{}), stderr: newRing(stderrKeep), started: time.Now()}
	if p.Kind != Heavy {
		j.encoder = ""
	}
	cmd.Stderr = j.stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return err
	}
	if j.id, err = database.NewID(); err != nil {
		cancel()
		return err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("arrancar FFmpeg: %w", err)
	}
	if err := attach(cmd.Process); err != nil {
		m.log.Warn().Err(err).Msg("no se pudo vincular FFmpeg al servidor")
	}
	st.job = j
	st.genStart[st.gen] = n
	startMS := int64(0)
	if st.segs != nil {
		startMS = st.segs[n].StartUS / 1000
	}
	_ = m.db.Q.CreateTranscodeJob(context.Background(), sqlc.CreateTranscodeJobParams{
		ID: j.id, PlaybackSessionID: p.SessionID, JobKind: jobKind, InputFileID: p.FileID,
		OutputProfile: profile, StartPositionMs: startMS, StartedAt: ptr(database.Now()),
	})
	m.bus.Publish(events.Event{Name: "transcode.started", AdminOnly: true, Data: map[string]any{
		"job_id": j.id, "session_id": p.SessionID, "start_segment": n, "kind": jobKind,
	}})
	m.log.Info().Str("job_id", j.id).Str("session_id", p.SessionID).Int("segmento", n).Str("perfil", profile).
		Str("codificador", string(j.encoder)).Msg("trabajo de FFmpeg iniciado")
	go m.readProgress(st, j, stdout)
	go m.wait(st, j)
	return nil
}

// readProgress lee -progress pipe:1 y publica transcode.progress.
func (m *Manager) readProgress(st *stream, j *job, r io.Reader) {
	sc := bufio.NewScanner(r)
	var last time.Time
	for sc.Scan() {
		v, ok := strings.CutPrefix(sc.Text(), "out_time_us=")
		if !ok {
			continue
		}
		us, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			continue
		}
		st.mu.Lock()
		j.positionUS = us
		st.mu.Unlock()
		if time.Since(last) >= progressEvery {
			last = time.Now()
			m.bus.Publish(events.Event{Name: "transcode.progress", AdminOnly: true, Data: map[string]any{
				"job_id": j.id, "session_id": st.plan.SessionID, "position_ms": us / 1000,
			}})
		}
	}
}

// wait espera a que FFmpeg termine y registra el resultado. Un codificador
// de hardware que falla sin producir nada se cambia por libx264 una sola vez.
func (m *Manager) wait(st *stream, j *job) {
	err := j.cmd.Wait()
	st.mu.Lock()
	j.err = err
	stopped := j.stopped
	produced := j.produced || m.anyProduced(st, j)
	close(j.done)
	pos := j.positionUS
	st.mu.Unlock()
	j.cancel()

	status, code, msg := "completed", (*string)(nil), (*string)(nil)
	switch {
	case stopped:
		status = "stopped"
	case err != nil:
		status = "failed"
		c := "TRANSCODE_INITIALIZATION_FAILED"
		if produced {
			c = "TRANSCODE_FAILED"
		}
		tail := j.stderr.String()
		code, msg = &c, &tail
		m.log.Error().Err(err).Str("job_id", j.id).Str("stderr", tail).Msg("FFmpeg terminó con error")
		m.bus.Publish(events.Event{Name: "transcode.failed", AdminOnly: true, Data: map[string]any{
			"job_id": j.id, "session_id": st.plan.SessionID, "error_code": c,
		}})
		if !produced && j.encoder != "" && j.encoder != ffmpeg.X264 {
			m.mu.Lock()
			if m.encoder == j.encoder {
				m.encoder = ffmpeg.X264
				m.log.Warn().Str("codificador", string(j.encoder)).Msg("el codificador de hardware falló; se usará libx264")
			}
			m.mu.Unlock()
		}
	}
	progress := 0.0
	if st.plan.DurationUS > 0 {
		progress = min(100, float64(pos)*100/float64(st.plan.DurationUS))
	}
	_ = m.db.Q.FinishTranscodeJob(context.Background(), sqlc.FinishTranscodeJobParams{
		ID: j.id, Status: status, Progress: progress, FinishedAt: ptr(database.Now()), ErrorCode: code, ErrorMessage: msg,
	})
}

func (m *Manager) anyProduced(st *stream, j *job) bool {
	return m.isComplete(st, j.startSeg)
}

// stopJob para FFmpeg y espera a que salga. Hay que tener st.mu: se suelta
// mientras se espera, porque wait lo necesita para registrar la salida.
func (m *Manager) stopJob(st *stream, j *job) {
	if isDone(j) {
		return
	}
	j.stopped = true
	j.cancel()
	st.mu.Unlock()
	select {
	case <-j.done:
	case <-time.After(stopTimeout + time.Second):
		m.log.Error().Str("job_id", j.id).Msg("FFmpeg no se detuvo a tiempo")
	}
	st.mu.Lock()
}

func isDone(j *job) bool {
	select {
	case <-j.done:
		return true
	default:
		return false
	}
}

// OnSessionEnded implementa sessions.Listener: para FFmpeg, borra el
// directorio y libera el cupo.
func (m *Manager) OnSessionEnded(sessionID string) {
	m.end(sessionID)
}

func (m *Manager) end(sessionID string) {
	m.mu.Lock()
	st, ok := m.streams[sessionID]
	delete(m.streams, sessionID)
	m.mu.Unlock()
	m.admission.Release(sessionID)
	if !ok {
		return
	}
	st.mu.Lock()
	if st.job != nil {
		m.stopJob(st, st.job)
	}
	st.mu.Unlock()
	if err := os.RemoveAll(st.dir); err != nil {
		m.log.Warn().Err(err).Str("session_id", sessionID).Msg("no se pudo borrar el directorio del trabajo")
	}
}

// Jobs devuelve los trabajos activos.
func (m *Manager) Jobs() []JobInfo {
	m.mu.Lock()
	streams := make([]*stream, 0, len(m.streams))
	for _, st := range m.streams {
		streams = append(streams, st)
	}
	m.mu.Unlock()
	var out []JobInfo
	for _, st := range streams {
		st.mu.Lock()
		if j := st.job; j != nil && !isDone(j) {
			out = append(out, JobInfo{
				ID: j.id, SessionID: st.plan.SessionID, Kind: st.plan.Kind, Encoder: string(j.encoder),
				StartSegment: j.startSeg, Segment: m.current(st), PositionUS: j.positionUS, StartedAt: j.started,
			})
		}
		st.mu.Unlock()
	}
	return out
}

// SessionOfJob devuelve la sesión de un trabajo activo.
func (m *Manager) SessionOfJob(jobID string) (string, bool) {
	for _, j := range m.Jobs() {
		if j.ID == jobID {
			return j.SessionID, true
		}
	}
	return "", false
}

// gcLoop borra directorios huérfanos y, si se supera la cuota, los segmentos
// ya servidos de las sesiones activas.
func (m *Manager) gcLoop(ctx context.Context) {
	t := time.NewTicker(gcEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.collect()
		}
	}
}

func (m *Manager) collect() {
	entries, err := os.ReadDir(m.opts.Dir)
	if err != nil {
		return
	}
	m.mu.Lock()
	active := map[string]*stream{}
	for id, st := range m.streams {
		active[id] = st
	}
	m.mu.Unlock()
	var total int64
	for _, e := range entries {
		path := filepath.Join(m.opts.Dir, e.Name())
		if _, ok := active[e.Name()]; !ok {
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > orphanAge {
				_ = os.RemoveAll(path)
			}
			continue
		}
		total += dirSize(path)
	}
	if m.opts.MaxDiskBytes <= 0 || total <= m.opts.MaxDiskBytes {
		return
	}
	m.log.Warn().Int64("bytes", total).Msg("cuota de transcodes superada: se borran segmentos ya servidos")
	for _, st := range active {
		st.mu.Lock()
		keepFrom := st.lastAsked - 3
		for n := range st.completed {
			if n < keepFrom && st.segs != nil {
				_ = os.Remove(filepath.Join(st.dir, SegmentName(n, st.ext)))
				delete(st.completed, n)
			}
		}
		st.mu.Unlock()
	}
}

func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

// readListNames devuelve los segmentos terminados de una lista CSV del muxer
// segment.
func readListNames(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	var out []string
	for {
		rec, err := r.Read()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, nil // La última línea puede estar a medio escribir.
		}
		if len(rec) > 0 {
			out = append(out, rec[0])
		}
	}
}

// readList devuelve los segmentos terminados con su duración, para la
// playlist EVENT.
func readList(path string) ([]Segment, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	var out []Segment
	for {
		rec, err := r.Read()
		if err != nil {
			return out, nil
		}
		if len(rec) < 3 {
			continue
		}
		start, ok1 := toUS(rec[1])
		end, ok2 := toUS(rec[2])
		if ok1 && ok2 && end > start {
			out = append(out, Segment{StartUS: start, DurationUS: end - start})
		}
	}
}

func ptr[T any](v T) *T { return &v }

// ring guarda los últimos bytes de stderr para diagnosticar un fallo.
type ring struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func newRing(max int) *ring { return &ring{max: max} }

func (r *ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.max {
		r.buf = r.buf[len(r.buf)-r.max:]
	}
	return len(p), nil
}

func (r *ring) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.TrimSpace(string(r.buf))
}
