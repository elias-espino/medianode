package monitoring

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

// ringSize son las últimas latencias que se guardan por ruta para calcular
// percentiles: lo reciente importa más que el histórico, y la memoria es fija.
const ringSize = 256

// Metrics recoge la latencia de los endpoints (§22.2). Agrupa por el patrón de
// la ruta, no por la URL: «/media/{id}» es una sola entrada.
type Metrics struct {
	mu     sync.Mutex
	routes map[string]*routeStat
}

type routeStat struct {
	count, errors int64
	ring          [ringSize]time.Duration
	filled        int
	next          int
	max           time.Duration
}

// NewMetrics crea el recolector.
func NewMetrics() *Metrics { return &Metrics{routes: map[string]*routeStat{}} }

// RouteLatency es lo medido de una ruta. Los percentiles son de las últimas
// muestras; Count y Errors, desde el arranque.
type RouteLatency struct {
	Route   string
	Count   int64
	Errors  int64 // Respuestas 5xx.
	P50MS   float64
	P95MS   float64
	MaxMS   float64
	Samples int
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap permite a http.ResponseController llegar al escritor original (Flush,
// Hijack).
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// skipped son las rutas que no miden latencia: transmiten durante minutos y
// falsearían los percentiles.
func skipped(path string) bool {
	return strings.HasPrefix(path, "/stream/") || path == "/api/v1/events"
}

// Middleware mide cada petición. Debe ir dentro del enrutador (tras chi) para
// que el patrón de la ruta esté resuelto al terminar.
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if skipped(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		sw := &statusWriter{ResponseWriter: w}
		start := time.Now()
		next.ServeHTTP(sw, r)
		pattern := "(sin ruta)"
		if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
			pattern = rc.RoutePattern()
		}
		m.record(r.Method+" "+pattern, time.Since(start), sw.status)
	})
}

func (m *Metrics) record(route string, d time.Duration, status int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.routes[route]
	if st == nil {
		st = &routeStat{}
		m.routes[route] = st
	}
	st.count++
	if status >= 500 {
		st.errors++
	}
	st.ring[st.next] = d
	st.next = (st.next + 1) % ringSize
	st.filled = min(st.filled+1, ringSize)
	st.max = max(st.max, d)
}

// Latency devuelve las rutas medidas, de la más usada a la menos.
func (m *Metrics) Latency() []RouteLatency {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]RouteLatency, 0, len(m.routes))
	for route, st := range m.routes {
		samples := make([]time.Duration, st.filled)
		copy(samples, st.ring[:st.filled])
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		out = append(out, RouteLatency{
			Route: route, Count: st.count, Errors: st.errors, Samples: len(samples),
			P50MS: ms(percentile(samples, 0.50)), P95MS: ms(percentile(samples, 0.95)), MaxMS: ms(st.max),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Route < out[j].Route
	})
	return out
}

// percentile es el valor por debajo del cual queda p de las muestras ordenadas
// (método del rango más cercano).
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p*float64(len(sorted))+0.999999) - 1
	idx = min(max(idx, 0), len(sorted)-1)
	return sorted[idx]
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
