package sessions

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"medianode/internal/authorization"
	"medianode/internal/core"
	"medianode/internal/database"
	"medianode/internal/database/sqlc"
	"medianode/internal/events"
)

const settingStreamSecret = "sessions.stream_secret"

// Clases de cliente. Las sesiones DLNA no tienen usuario y no escriben
// progreso ni historial.
const (
	KindApp  = "app"
	KindDLNA = "dlna"
)

// Options ajusta los tiempos del gestor.
type Options struct {
	IdleTimeout        time.Duration // heartbeat_timeout_seconds
	FlushInterval      time.Duration // progress_persist_interval_seconds
	CompletedThreshold float64       // Fracción de la duración, 0,9 por defecto.
	SweepInterval      time.Duration // Revisión de inactividad; por defecto un tercio de IdleTimeout, como mucho 10 s.
	TranscodeDir       string        // Se vacía al reconciliar.
}

func (o *Options) defaults() {
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = 90 * time.Second
	}
	if o.FlushInterval <= 0 {
		o.FlushInterval = 30 * time.Second
	}
	if o.CompletedThreshold <= 0 || o.CompletedThreshold > 1 {
		o.CompletedThreshold = 0.9
	}
	if o.SweepInterval <= 0 {
		o.SweepInterval = min(o.IdleTimeout/3, 10*time.Second)
	}
}

// Session es una copia del estado de una sesión.
type Session struct {
	ID              string
	ClientKind      string
	UserID          string
	DeviceID        string
	ItemID          string
	FileID          string
	LibraryID       string
	Title           string
	Mode            string
	Status          Status
	StartPositionMS int64
	PositionMS      int64
	DurationMS      *int64
	StartedAt       time.Time
	LastHeartbeat   time.Time // Cero si aún no llegó ninguno.
	LastActivity    time.Time
	EndedAt         *time.Time

	dirty bool // Hay posición sin volcar.
}

// Completed indica si la posición supera el umbral de completado.
func (s *Session) completed(threshold float64) bool {
	return s.DurationMS != nil && *s.DurationMS > 0 &&
		float64(s.PositionMS) >= threshold*float64(*s.DurationMS)
}

// watched indica si la sesión llegó a reproducir algo. Sin ningún heartbeat
// no se escribe progreso ni historial: una sesión abierta por error desde el
// principio no debe borrar la posición guardada.
func (s *Session) watched() bool {
	return !s.LastHeartbeat.IsZero() && s.ClientKind == KindApp
}

// CreateParams son los datos de una sesión nueva. La autorización y la
// decisión de reproducción las hace quien llama (Fase 7).
type CreateParams struct {
	ID              string // Opcional: lo genera quien llama para reservar el cupo antes de crear la sesión.
	ClientKind      string
	UserID          string
	DeviceID        string
	ItemID          string
	FileID          string
	Mode            string
	StartPositionMS int64
	AudioStreamID   *string
	SubtitleID      *string
	ClientIP        string
}

// Heartbeat es el estado que informa el cliente.
type Heartbeat struct {
	State      Status // Playing o Paused.
	PositionMS int64
}

// Listener recibe las sesiones que terminan, para liberar trabajos y cupos.
type Listener interface {
	OnSessionEnded(s Session)
}

// ListenerFunc adapta una función a Listener.
type ListenerFunc func(Session)

// OnSessionEnded implementa Listener.
func (f ListenerFunc) OnSessionEnded(s Session) { f(s) }

// Manager mantiene las sesiones activas en memoria y las vuelca a SQLite. Las
// posiciones se escriben cada FlushInterval, o al momento al pausar o
// terminar; Touch y la verificación del token no tocan disco.
type Manager struct {
	db   *database.DB
	bus  *events.Bus
	opts Options
	log  zerolog.Logger
	now  func() time.Time

	mu        sync.Mutex
	active    map[string]*Session
	listeners []Listener
	secret    []byte

	// writeMu ordena las escrituras: un volcado periódico nunca se intercala
	// con el cierre de la misma sesión.
	writeMu sync.Mutex

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New crea el gestor.
func New(db *database.DB, bus *events.Bus, opts Options, log zerolog.Logger) *Manager {
	opts.defaults()
	return &Manager{db: db, bus: bus, opts: opts, log: log, now: time.Now, active: map[string]*Session{}}
}

// AddListener registra un receptor de sesiones terminadas. Debe llamarse
// antes de Start.
func (m *Manager) AddListener(l Listener) {
	m.listeners = append(m.listeners, l)
}

// IdleTimeout es el tiempo sin actividad tras el que expira una sesión.
func (m *Manager) IdleTimeout() time.Duration { return m.opts.IdleTimeout }

// Name implementa lifecycle.Component.
func (m *Manager) Name() string { return "sessions" }

// Start reconcilia el estado de una ejecución anterior y lanza los bucles.
// Va antes que el servidor HTTP: ninguna petición ve sesiones huérfanas.
func (m *Manager) Start(ctx context.Context) error {
	if err := m.loadSecret(ctx); err != nil {
		return err
	}
	if err := m.Reconcile(ctx); err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	m.cancel = cancel
	m.wg.Go(func() { m.loop(runCtx, m.opts.FlushInterval, m.flush) })
	m.wg.Go(func() { m.loop(runCtx, m.opts.SweepInterval, m.sweep) })
	return nil
}

// Stop detiene los bucles y cierra las sesiones activas como expiradas, con
// su progreso volcado. Tras reiniciar, el cliente recibe SESSION_EXPIRED y
// continúa desde ahí.
func (m *Manager) Stop(ctx context.Context) error {
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
	return m.Shutdown(ctx)
}

func (m *Manager) loop(ctx context.Context, every time.Duration, fn func(context.Context)) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn(ctx)
		}
	}
}

func (m *Manager) loadSecret(ctx context.Context) error {
	return m.db.InTx(ctx, func(q *sqlc.Queries) error {
		stored, err := q.GetSetting(ctx, settingStreamSecret)
		if err == nil {
			m.secret, err = base64.StdEncoding.DecodeString(stored)
			if err != nil {
				return fmt.Errorf("secreto de streaming dañado: %w", err)
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("leer secreto de streaming: %w", err)
		}
		m.secret = make([]byte, 32)
		if _, err := rand.Read(m.secret); err != nil {
			return err
		}
		return q.SetSetting(ctx, sqlc.SetSettingParams{Key: settingStreamSecret, Value: base64.StdEncoding.EncodeToString(m.secret)})
	})
}

// Reconcile aplica §12.5: las sesiones abiertas de una ejecución anterior
// pasan a expired con su última posición volcada, los trabajos activos a
// failed con SERVER_RESTART, y se vacía el directorio de transcodificación.
// Los escaneos interrumpidos los marca el escáner.
func (m *Manager) Reconcile(ctx context.Context) error {
	open, err := m.db.R.ListOpenSessions(ctx)
	if err != nil {
		return fmt.Errorf("leer sesiones abiertas: %w", err)
	}
	now := m.now()
	for _, row := range open {
		s, err := m.fromRow(ctx, row)
		if err != nil {
			return err
		}
		if err := m.persistEnd(ctx, s, Expired, now); err != nil {
			return err
		}
	}
	if _, err := m.db.Q.FailActiveTranscodes(ctx, optionalTime(now)); err != nil {
		return fmt.Errorf("cerrar trabajos activos: %w", err)
	}
	if len(open) > 0 {
		m.log.Warn().Int("sesiones", len(open)).Msg("sesiones de una ejecución anterior cerradas como expiradas")
	}
	return m.cleanTranscodes()
}

// cleanTranscodes borra todo lo que hay en el directorio de trabajos: tras
// reconciliar no queda ninguna sesión activa.
func (m *Manager) cleanTranscodes() error {
	if m.opts.TranscodeDir == "" {
		return nil
	}
	entries, err := os.ReadDir(m.opts.TranscodeDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("leer transcodes: %w", err)
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(m.opts.TranscodeDir, e.Name())); err != nil {
			return fmt.Errorf("limpiar transcodes: %w", err)
		}
	}
	return nil
}

// fromRow reconstruye una sesión guardada, con su biblioteca y su título.
func (m *Manager) fromRow(ctx context.Context, row sqlc.PlaybackSession) (*Session, error) {
	s := &Session{
		ID: row.ID, ClientKind: row.ClientKind, ItemID: row.MediaItemID, FileID: row.MediaFileID,
		Mode: row.PlaybackMode, Status: Status(row.Status), StartPositionMS: row.StartPositionMs,
		PositionMS: row.PositionMs, DurationMS: row.DurationMs,
	}
	if row.UserID != nil {
		s.UserID = *row.UserID
	}
	if row.DeviceID != nil {
		s.DeviceID = *row.DeviceID
	}
	s.StartedAt, _ = database.ParseTime(row.StartedAt)
	if row.LastHeartbeatAt != nil {
		s.LastHeartbeat, _ = database.ParseTime(*row.LastHeartbeatAt)
	}
	if row.LastActivityAt != nil {
		s.LastActivity, _ = database.ParseTime(*row.LastActivityAt)
	}
	if row.EndedAt != nil {
		t, _ := database.ParseTime(*row.EndedAt)
		s.EndedAt = &t
	}
	target, err := m.db.R.GetSessionTarget(ctx, sqlc.GetSessionTargetParams{ID: row.MediaItemID, ID_2: row.MediaFileID})
	if err == nil {
		s.LibraryID, s.Title = target.LibraryID, target.Title
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("leer elemento de la sesión: %w", err)
	}
	return s, nil
}

// Create abre una sesión en initializing. El archivo debe pertenecer al
// elemento.
func (m *Manager) Create(ctx context.Context, p CreateParams) (*Session, error) {
	if p.ClientKind == "" {
		p.ClientKind = KindApp
	}
	if p.ClientKind == KindApp && (p.UserID == "" || p.DeviceID == "") {
		return nil, errors.New("una sesión de aplicación necesita usuario y dispositivo")
	}
	if p.StartPositionMS < 0 {
		return nil, core.ErrValidation("La posición inicial no es válida")
	}
	target, err := m.db.R.GetSessionTarget(ctx, sqlc.GetSessionTargetParams{ID: p.ItemID, ID_2: p.FileID})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrNotFound()
	}
	if err != nil {
		return nil, fmt.Errorf("leer elemento: %w", err)
	}
	id := p.ID
	if id == "" {
		if id, err = database.NewID(); err != nil {
			return nil, err
		}
	}
	now := m.now()
	duration := target.ItemDurationMs
	if duration == nil {
		duration = target.FileDurationMs
	}
	s := &Session{
		ID: id, ClientKind: p.ClientKind, UserID: p.UserID, DeviceID: p.DeviceID,
		ItemID: p.ItemID, FileID: p.FileID, LibraryID: target.LibraryID, Title: target.Title,
		Mode: p.Mode, Status: Initializing, StartPositionMS: p.StartPositionMS, PositionMS: p.StartPositionMS,
		DurationMS: duration, StartedAt: now, LastActivity: now,
	}
	nowStr := database.FormatTime(now)
	err = m.db.Q.CreateSession(ctx, sqlc.CreateSessionParams{
		ID: id, ClientKind: p.ClientKind, UserID: optional(p.UserID), DeviceID: optional(p.DeviceID),
		MediaItemID: p.ItemID, MediaFileID: p.FileID, SelectedAudioStreamID: p.AudioStreamID,
		SelectedSubtitleID: p.SubtitleID, PlaybackMode: p.Mode, Status: string(Initializing),
		StartPositionMs: p.StartPositionMS, DurationMs: duration, StartedAt: nowStr, ClientIp: optional(p.ClientIP),
	})
	if err != nil {
		return nil, fmt.Errorf("crear sesión: %w", err)
	}
	m.mu.Lock()
	m.active[id] = s
	snap := *s
	m.mu.Unlock()
	m.publish(events.SessionCreated, &snap)
	return &snap, nil
}

// lookup devuelve la sesión activa del actor. Si ya terminó, SESSION_EXPIRED;
// si no existe o es de otro usuario, 404. Hay que tener m.mu.
func (m *Manager) lookupLocked(id string, actor authorization.Actor) (*Session, error) {
	s, ok := m.active[id]
	if !ok {
		return nil, nil
	}
	if s.UserID != actor.UserID {
		return nil, core.ErrNotFound()
	}
	return s, nil
}

// ended responde por una sesión que no está activa: SESSION_EXPIRED si es del
// actor, 404 si no existe o es de otro.
func (m *Manager) ended(ctx context.Context, actor authorization.Actor, id string) error {
	row, err := m.db.R.GetSession(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ErrNotFound()
	}
	if err != nil {
		return fmt.Errorf("leer sesión: %w", err)
	}
	if row.UserID == nil || *row.UserID != actor.UserID {
		return core.ErrNotFound()
	}
	return core.ErrSessionExpired()
}

// Get devuelve una sesión del actor, activa o terminada. Una terminada sirve
// al cliente para retomar desde su última posición.
func (m *Manager) Get(ctx context.Context, actor authorization.Actor, id string) (*Session, error) {
	m.mu.Lock()
	s, err := m.lookupLocked(id, actor)
	if s != nil {
		snap := *s
		m.mu.Unlock()
		return &snap, nil
	}
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	row, err := m.db.R.GetSession(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrNotFound()
	}
	if err != nil {
		return nil, fmt.Errorf("leer sesión: %w", err)
	}
	if row.UserID == nil || *row.UserID != actor.UserID {
		return nil, core.ErrNotFound()
	}
	return m.fromRow(ctx, row)
}

// SetTracks guarda las pistas de audio y de subtítulos elegidas de una sesión
// abierta. Una terminada no se modifica.
func (m *Manager) SetTracks(ctx context.Context, id string, audio, subtitle *string) error {
	n, err := m.db.Q.UpdateSessionTracks(ctx, sqlc.UpdateSessionTracksParams{ID: id, SelectedAudioStreamID: audio, SelectedSubtitleID: subtitle})
	if err != nil {
		return fmt.Errorf("guardar pistas: %w", err)
	}
	if n == 0 {
		return core.ErrSessionExpired()
	}
	return nil
}

// Heartbeat registra el estado que informa el cliente. Un cambio de estado se
// guarda al momento; la posición, en el siguiente volcado, salvo al pausar.
func (m *Manager) Heartbeat(ctx context.Context, actor authorization.Actor, id string, hb Heartbeat) (*Session, error) {
	if hb.State != Playing && hb.State != Paused {
		return nil, core.ErrValidation("El estado debe ser playing o paused")
	}
	if hb.PositionMS < 0 {
		return nil, core.ErrValidation("La posición no es válida")
	}
	m.mu.Lock()
	s, err := m.lookupLocked(id, actor)
	if err != nil || s == nil {
		m.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return nil, m.ended(ctx, actor, id)
	}
	changed := s.Status != hb.State
	if changed && !CanTransition(s.Status, hb.State) {
		m.mu.Unlock()
		return nil, core.ErrValidation(fmt.Sprintf("No se puede pasar de %s a %s", s.Status, hb.State))
	}
	now := m.now()
	pos := hb.PositionMS
	if s.DurationMS != nil && *s.DurationMS > 0 {
		pos = min(pos, *s.DurationMS)
	}
	s.Status, s.PositionMS, s.LastHeartbeat, s.LastActivity, s.dirty = hb.State, pos, now, now, true
	snap := *s
	m.mu.Unlock()

	if changed {
		if err := m.db.Q.SetSessionStatus(ctx, sqlc.SetSessionStatusParams{ID: id, Status: string(hb.State)}); err != nil {
			return nil, fmt.Errorf("guardar estado: %w", err)
		}
		m.publish(events.SessionUpdated, &snap)
		if hb.State == Paused {
			m.flushOne(ctx, id)
		}
	}
	return &snap, nil
}

// Touch registra actividad sin heartbeat: un segmento o una lectura por
// rango. Solo toca memoria.
func (m *Manager) Touch(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.active[id]; ok {
		s.LastActivity = m.now()
	}
}

// StopSession cierra una sesión del actor: completed si se superó el umbral,
// stopped si no. Cerrar una sesión ya terminada no hace nada.
func (m *Manager) StopSession(ctx context.Context, actor authorization.Actor, id string) error {
	m.mu.Lock()
	s, err := m.lookupLocked(id, actor)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if s == nil {
		if err := m.ended(ctx, actor, id); core.CodeOf(err) != core.CodeSessionExpired {
			return err
		}
		return nil
	}
	return m.finish(ctx, id, true)
}

// finish lleva la sesión a stopping y después a su estado final. Con byUser,
// el final es completed o stopped; si no, expired.
func (m *Manager) finish(ctx context.Context, id string, byUser bool) error {
	m.mu.Lock()
	s, ok := m.active[id]
	if !ok {
		m.mu.Unlock()
		return nil
	}
	final := Expired
	if byUser {
		if !CanTransition(s.Status, Stopping) {
			m.mu.Unlock()
			return core.ErrValidation("La sesión no se puede cerrar en su estado actual")
		}
		final = Stopped
		if s.completed(m.opts.CompletedThreshold) {
			final = Completed
		}
	}
	delete(m.active, id)
	snap := *s
	m.mu.Unlock()
	return m.closeSession(ctx, &snap, final)
}

func (m *Manager) closeSession(ctx context.Context, s *Session, final Status) error {
	now := m.now()
	err := m.persistEnd(ctx, s, final, now)
	s.Status, s.EndedAt = final, &now
	m.publish(events.SessionEnded, s)
	for _, l := range m.listeners {
		l.OnSessionEnded(*s)
	}
	return err
}

// persistEnd guarda el cierre, el progreso y una única entrada de historial,
// en una transacción.
func (m *Manager) persistEnd(ctx context.Context, s *Session, final Status, at time.Time) error {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	return m.db.InTx(ctx, func(q *sqlc.Queries) error {
		n, err := q.EndSession(ctx, sqlc.EndSessionParams{
			ID: s.ID, Status: string(final), PositionMs: s.PositionMS,
			LastHeartbeatAt: optionalTime(s.LastHeartbeat), LastActivityAt: optionalTime(s.LastActivity),
			EndedAt: optionalTime(at),
		})
		if err != nil {
			return fmt.Errorf("cerrar sesión: %w", err)
		}
		// Sin fila, el elemento o el usuario se borraron: no hay dónde escribir.
		if n == 0 || !s.watched() {
			return nil
		}
		if err := m.writeProgress(ctx, q, s); err != nil {
			return err
		}
		histID, err := database.NewID()
		if err != nil {
			return err
		}
		return q.AddHistory(ctx, sqlc.AddHistoryParams{
			ID: histID, UserID: s.UserID, DeviceID: optional(s.DeviceID), MediaItemID: &s.ItemID,
			Title: s.Title, PositionMs: s.PositionMS, Completed: boolInt(s.completed(m.opts.CompletedThreshold)),
			WatchedAt: database.FormatTime(at),
		})
	})
}

// writeProgress guarda la posición con la hora del último heartbeat, no la
// del volcado: así un dato más reciente de otro dispositivo no queda pisado.
func (m *Manager) writeProgress(ctx context.Context, q *sqlc.Queries, s *Session) error {
	_, err := q.UpsertProgress(ctx, sqlc.UpsertProgressParams{
		UserID: s.UserID, MediaItemID: s.ItemID, PositionMs: s.PositionMS, DurationMs: s.DurationMS,
		Completed: boolInt(s.completed(m.opts.CompletedThreshold)), UpdatedAt: database.FormatTime(s.LastHeartbeat),
	})
	if err != nil {
		return fmt.Errorf("guardar progreso: %w", err)
	}
	return nil
}

// flush vuelca las sesiones con posición pendiente.
func (m *Manager) flush(ctx context.Context) {
	m.mu.Lock()
	var ids []string
	for id, s := range m.active {
		if s.dirty {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.flushOne(ctx, id)
	}
}

func (m *Manager) flushOne(ctx context.Context, id string) {
	m.writeMu.Lock()
	m.mu.Lock()
	s, ok := m.active[id]
	if !ok {
		m.mu.Unlock()
		m.writeMu.Unlock()
		return
	}
	snap := *s
	s.dirty = false
	m.mu.Unlock()

	var vanished bool
	err := m.db.InTx(ctx, func(q *sqlc.Queries) error {
		n, err := q.FlushSession(ctx, sqlc.FlushSessionParams{
			ID: id, PositionMs: snap.PositionMS, LastHeartbeatAt: optionalTime(snap.LastHeartbeat),
			LastActivityAt: optionalTime(snap.LastActivity),
		})
		if err != nil {
			return err
		}
		if n == 0 {
			vanished = true
			return nil
		}
		if !snap.watched() {
			return nil
		}
		return m.writeProgress(ctx, q, &snap)
	})
	m.writeMu.Unlock()
	if err != nil {
		m.mu.Lock()
		if s, ok := m.active[id]; ok {
			s.dirty = true // Se reintenta en el siguiente volcado.
		}
		m.mu.Unlock()
		m.log.Error().Err(err).Str("session_id", id).Msg("no se pudo volcar la sesión")
		return
	}
	if vanished {
		// Un escaneo purgó el elemento o se borró el usuario: la fila se fue
		// en cascada. La sesión termina sin nada que guardar.
		m.mu.Lock()
		delete(m.active, id)
		m.mu.Unlock()
		snap.Status = Failed
		now := m.now()
		snap.EndedAt = &now
		m.publish(events.SessionEnded, &snap)
		for _, l := range m.listeners {
			l.OnSessionEnded(snap)
		}
		return
	}
	if snap.watched() {
		m.bus.Publish(events.Event{Name: events.PlaybackProgress, UserID: snap.UserID, Data: map[string]any{
			"session_id": snap.ID, "media_item_id": snap.ItemID, "position_ms": snap.PositionMS,
		}})
	}
}

// sweep expira las sesiones sin actividad durante IdleTimeout.
func (m *Manager) sweep(ctx context.Context) {
	limit := m.now().Add(-m.opts.IdleTimeout)
	m.mu.Lock()
	var idle []string
	for id, s := range m.active {
		if s.LastActivity.Before(limit) {
			idle = append(idle, id)
		}
	}
	m.mu.Unlock()
	for _, id := range idle {
		if err := m.finish(ctx, id, false); err != nil {
			m.log.Error().Err(err).Str("session_id", id).Msg("no se pudo expirar la sesión")
		}
	}
}

// Terminate cierra las sesiones que cumplen match, antes de borrar su
// biblioteca, su usuario o su dispositivo (§6.3).
func (m *Manager) Terminate(ctx context.Context, match func(Session) bool) error {
	m.mu.Lock()
	var ids []string
	for id, s := range m.active {
		if match(*s) {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	var errs []error
	for _, id := range ids {
		errs = append(errs, m.finish(ctx, id, true))
	}
	return errors.Join(errs...)
}

// TerminateLibrary cierra las sesiones de una biblioteca.
func (m *Manager) TerminateLibrary(ctx context.Context, libraryID string) error {
	return m.Terminate(ctx, func(s Session) bool { return s.LibraryID == libraryID })
}

// TerminateUser cierra las sesiones de un usuario.
func (m *Manager) TerminateUser(ctx context.Context, userID string) error {
	return m.Terminate(ctx, func(s Session) bool { return s.UserID == userID })
}

// TerminateDevice cierra las sesiones de un dispositivo.
func (m *Manager) TerminateDevice(ctx context.Context, deviceID string) error {
	return m.Terminate(ctx, func(s Session) bool { return s.DeviceID == deviceID })
}

// Shutdown cierra todas las sesiones activas como expiradas.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	ids := make([]string, 0, len(m.active))
	for id := range m.active {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	var errs []error
	for _, id := range ids {
		errs = append(errs, m.finish(ctx, id, false))
	}
	return errors.Join(errs...)
}

// Active devuelve cuántas sesiones hay activas.
func (m *Manager) Active() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.active)
}

// StreamToken es el token de las URL de streaming: HMAC del identificador.
// No se guarda; vive lo que la sesión, porque VerifyToken exige que siga
// activa.
func (m *Manager) StreamToken(id string) string {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(id))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// VerifyToken comprueba el token en tiempo constante y devuelve la sesión si
// sigue activa. Un token falso responde 404; uno válido de una sesión
// terminada, SESSION_EXPIRED.
func (m *Manager) VerifyToken(id, token string) (*Session, error) {
	if !hmac.Equal([]byte(token), []byte(m.StreamToken(id))) {
		return nil, core.ErrNotFound()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.active[id]
	if !ok {
		return nil, core.ErrSessionExpired()
	}
	snap := *s
	return &snap, nil
}

func (m *Manager) publish(name string, s *Session) {
	if s.UserID == "" {
		return // DLNA: no hay usuario al que avisar.
	}
	data := map[string]any{
		"session_id": s.ID, "media_item_id": s.ItemID, "status": string(s.Status), "position_ms": s.PositionMS,
	}
	m.bus.Publish(events.Event{Name: name, UserID: s.UserID, Data: data})
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optionalTime(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := database.FormatTime(t)
	return &s
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
