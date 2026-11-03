package auth

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// idleAfter es lo que tarda en rellenarse un cubo. Pasado ese tiempo su entrada
// no aporta nada y se elimina.
const idleAfter = time.Minute

// Limiter limita los intentos por minuto de cada clave. Las claves vienen del
// exterior, así que barre las inactivas para que la memoria no crezca sin fin.
type Limiter struct {
	mu        sync.Mutex
	perMinute int
	entries   map[string]*limiterEntry
	nextSweep time.Time
	now       func() time.Time
}

type limiterEntry struct {
	bucket *rate.Limiter
	last   time.Time
}

// NewLimiter crea un Limiter de perMinute intentos por minuto.
func NewLimiter(perMinute int) *Limiter {
	return &Limiter{perMinute: perMinute, entries: map[string]*limiterEntry{}, now: time.Now}
}

// Allow consume un intento de key e indica si estaba permitido.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if now.After(l.nextSweep) {
		l.nextSweep = now.Add(idleAfter)
		for k, e := range l.entries {
			if now.Sub(e.last) > idleAfter {
				delete(l.entries, k)
			}
		}
	}

	e, ok := l.entries[key]
	if !ok {
		e = &limiterEntry{bucket: rate.NewLimiter(rate.Every(time.Minute/time.Duration(l.perMinute)), l.perMinute)}
		l.entries[key] = e
	}
	e.last = now
	return e.bucket.AllowN(now, 1)
}
