package transcoding

import (
	"errors"
	"sync"
)

// ErrCapacity indica que no hay cupo para otro trabajo de FFmpeg.
var ErrCapacity = errors.New("sin cupo de transcodificación")

// JobKind clasifica un trabajo por su coste.
type JobKind string

// Clases de trabajo. Recodificar vídeo es caro; copiarlo y, como mucho,
// recodificar el audio, barato.
const (
	Heavy JobKind = "heavy"
	Light JobKind = "light"
)

// Admission reparte los cupos de max_concurrent_jobs y
// max_concurrent_light_jobs (§10.6). El cupo pertenece a la sesión, no al
// proceso: reiniciar FFmpeg por un salto no lo pierde.
type Admission struct {
	mu       sync.Mutex
	limits   map[JobKind]int
	used     map[JobKind]int
	sessions map[string]JobKind
}

// NewAdmission crea el control con los límites de cada clase.
func NewAdmission(heavy, light int) *Admission {
	return &Admission{
		limits:   map[JobKind]int{Heavy: heavy, Light: light},
		used:     map[JobKind]int{},
		sessions: map[string]JobKind{},
	}
}

// Reserve reserva un cupo para la sesión. Reservar otra vez para la misma
// sesión y clase no consume más.
func (a *Admission) Reserve(kind JobKind, sessionID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if prev, ok := a.sessions[sessionID]; ok {
		if prev == kind {
			return nil
		}
		a.used[prev]--
		delete(a.sessions, sessionID)
	}
	if a.used[kind] >= a.limits[kind] {
		return ErrCapacity
	}
	a.used[kind]++
	a.sessions[sessionID] = kind
	return nil
}

// Release libera el cupo de la sesión. Es idempotente.
func (a *Admission) Release(sessionID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if kind, ok := a.sessions[sessionID]; ok {
		a.used[kind]--
		delete(a.sessions, sessionID)
	}
}

// Usage devuelve los cupos en uso y los límites.
func (a *Admission) Usage() (used, limits map[JobKind]int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	used, limits = map[JobKind]int{}, map[JobKind]int{}
	for k, v := range a.used {
		used[k] = v
	}
	for k, v := range a.limits {
		limits[k] = v
	}
	return used, limits
}
