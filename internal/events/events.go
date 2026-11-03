// Package events es el bus interno de §17. Los módulos publican y cada
// conexión WebSocket recibe solo lo que su usuario puede consultar por REST.
// Los eventos son avisos: el cliente recupera el estado por REST al
// reconectarse.
package events

import (
	"context"
	"sync"
	"time"

	"medianode/internal/authorization"
	"medianode/internal/media"
)

// Nombres de §17 que el servidor ya emite.
const (
	SessionCreated   = "session.created"
	SessionUpdated   = "session.updated"
	SessionEnded     = "session.ended"
	PlaybackProgress = "playback.progress"
	ScanStarted      = "library.scan.started"
	ScanCompleted    = "library.scan.completed"
	LibraryUpdated   = "library.updated"
	DeviceRevoked    = "device.revoked"
)

// subscriberBuffer es lo que puede retrasarse una conexión antes de que se
// la desconecte. Un cliente lento no debe frenar a quien publica.
const subscriberBuffer = 64

// Event es un aviso. Exactamente uno de UserID, LibraryID o AdminOnly
// decide quién lo recibe.
type Event struct {
	Name      string
	Time      time.Time
	Data      map[string]any
	UserID    string // Solo para ese usuario: sesiones, progreso, dispositivos.
	LibraryID string // Para quien puede ver la biblioteca.
	AdminOnly bool   // Transcodificación y estado del servidor.
}

// LibraryChecker comprueba si el actor ve una biblioteca.
type LibraryChecker interface {
	CanLibrary(ctx context.Context, actor authorization.Actor, libraryID string, p media.Permission) error
}

// CanReceive aplica el filtro de §17. Un evento sin destinatario no llega a
// nadie: ante la duda, no se filtra información.
func CanReceive(ctx context.Context, libs LibraryChecker, actor authorization.Actor, e Event) bool {
	switch {
	case e.UserID != "":
		return e.UserID == actor.UserID
	case e.LibraryID != "":
		return libs.CanLibrary(ctx, actor, e.LibraryID, media.PermissionView) == nil
	case e.AdminOnly:
		return authorization.IsAdmin(actor)
	}
	return false
}

// Subscription recibe los eventos publicados. Dropped se cierra si la
// suscripción se descartó por no leer a tiempo.
type Subscription struct {
	C       <-chan Event
	Dropped <-chan struct{}

	c       chan Event
	dropped chan struct{}
	once    sync.Once
}

// Bus reparte cada evento a todas las suscripciones. Filtrar es cosa de quien
// recibe, porque puede exigir consultar la base y no debe frenar a quien
// publica.
type Bus struct {
	mu   sync.Mutex
	subs map[*Subscription]struct{}
	now  func() time.Time
}

// NewBus crea el bus.
func NewBus() *Bus {
	return &Bus{subs: map[*Subscription]struct{}{}, now: time.Now}
}

// Publish entrega el evento sin bloquear. Una suscripción con el búfer lleno
// se descarta y su conexión se cierra.
func (b *Bus) Publish(e Event) {
	if b == nil {
		return
	}
	if e.Time.IsZero() {
		e.Time = b.now()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for s := range b.subs {
		select {
		case s.c <- e:
		default:
			delete(b.subs, s)
			s.once.Do(func() { close(s.dropped) })
		}
	}
}

// Subscribe crea una suscripción. Hay que llamar a Unsubscribe al terminar.
func (b *Bus) Subscribe() *Subscription {
	s := &Subscription{c: make(chan Event, subscriberBuffer), dropped: make(chan struct{})}
	s.C, s.Dropped = s.c, s.dropped
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

// Unsubscribe retira la suscripción.
func (b *Bus) Unsubscribe(s *Subscription) {
	b.mu.Lock()
	delete(b.subs, s)
	b.mu.Unlock()
}

// Subscribers devuelve cuántas suscripciones hay. Para pruebas y diagnóstico.
func (b *Bus) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
