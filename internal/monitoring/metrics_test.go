package monitoring

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func TestPercentile(t *testing.T) {
	var d []time.Duration
	for i := 1; i <= 100; i++ {
		d = append(d, time.Duration(i)*time.Millisecond)
	}
	cases := []struct {
		p    float64
		want time.Duration
	}{{0.50, 50 * time.Millisecond}, {0.95, 95 * time.Millisecond}, {1, 100 * time.Millisecond}, {0, time.Millisecond}}
	for _, c := range cases {
		if got := percentile(d, c.p); got != c.want {
			t.Errorf("p%v = %v, se esperaba %v", c.p, got, c.want)
		}
	}
	if percentile(nil, 0.5) != 0 {
		t.Error("sin muestras, cero")
	}
	if got := percentile([]time.Duration{7 * time.Millisecond}, 0.95); got != 7*time.Millisecond {
		t.Errorf("una muestra: %v", got)
	}
}

func TestMetricsAgrupaPorPatronYCuentaErrores(t *testing.T) {
	m := NewMetrics()
	r := chi.NewRouter()
	r.Use(m.Middleware)
	r.Get("/media/{id}", func(w http.ResponseWriter, r *http.Request) {
		if chi.URLParam(r, "id") == "roto" {
			http.Error(w, "x", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	r.Get("/stream/session/{id}", func(http.ResponseWriter, *http.Request) {})
	r.Get("/api/v1/events", func(http.ResponseWriter, *http.Request) {})

	for _, id := range []string{"a", "b", "c", "roto"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/media/"+id, nil))
	}
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/stream/session/x", nil))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/events", nil))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/no-existe", nil))

	got := map[string]RouteLatency{}
	for _, l := range m.Latency() {
		got[l.Route] = l
	}
	media := got["GET /media/{id}"]
	if media.Count != 4 || media.Errors != 1 || media.Samples != 4 {
		t.Fatalf("GET /media/{id} = %+v", media)
	}
	if media.P95MS < media.P50MS || media.MaxMS < media.P95MS {
		t.Fatalf("percentiles desordenados: %+v", media)
	}
	for _, skipped := range []string{"GET /stream/session/{id}", "GET /api/v1/events"} {
		if _, ok := got[skipped]; ok {
			t.Errorf("%s no debe medirse", skipped)
		}
	}
	if got["GET (sin ruta)"].Count != 1 {
		t.Errorf("una ruta que no existe se agrupa aparte: %v", got)
	}
	// La más usada va primero.
	if l := m.Latency(); l[0].Route != "GET /media/{id}" {
		t.Errorf("orden: %v", l)
	}
}

func TestMetricsRingSoloGuardaLasUltimas(t *testing.T) {
	m := NewMetrics()
	for i := 0; i < ringSize+50; i++ {
		m.record("GET /x", time.Duration(i)*time.Millisecond, 200)
	}
	l := m.Latency()[0]
	if l.Count != ringSize+50 || l.Samples != ringSize {
		t.Fatalf("%+v", l)
	}
	// El máximo es histórico; el p50 solo ve las últimas ringSize.
	if l.MaxMS != float64(ringSize+49) || l.P50MS < 50 {
		t.Fatalf("%+v", l)
	}
}

func TestStatusWriterConservaElPrimerEstado(t *testing.T) {
	rec := httptest.NewRecorder()
	w := &statusWriter{ResponseWriter: rec}
	w.WriteHeader(http.StatusNotFound)
	w.WriteHeader(http.StatusOK)
	if w.status != http.StatusNotFound {
		t.Fatalf("status = %d", w.status)
	}
	w2 := &statusWriter{ResponseWriter: httptest.NewRecorder()}
	_, _ = w2.Write([]byte("x"))
	if w2.status != http.StatusOK {
		t.Fatalf("una escritura sin cabecera es 200: %d", w2.status)
	}
	if w2.Unwrap() == nil {
		t.Fatal("Unwrap")
	}
}

func TestPercentDeCPU(t *testing.T) {
	cases := []struct {
		cpu, wall time.Duration
		cpus      int
		want      float64
	}{
		{time.Second, 4 * time.Second, 1, 25},
		{time.Second, 4 * time.Second, 4, 6.25},
		{8 * time.Second, 4 * time.Second, 4, 50},
		{99 * time.Second, time.Second, 2, 100}, // Nunca más de 100.
		{time.Second, 0, 2, 0},
		{time.Second, time.Second, 0, 0},
		{-time.Second, time.Second, 2, 0},
	}
	for _, c := range cases {
		if got := percent(c.cpu, c.wall, c.cpus); got < c.want-0.001 || got > c.want+0.001 {
			t.Errorf("percent(%v, %v, %d) = %.3f, se esperaba %.3f", c.cpu, c.wall, c.cpus, got, c.want)
		}
	}
}

func TestProcessSamplerMideEntreLecturas(t *testing.T) {
	clock := time.Unix(1000, 0)
	cpu := time.Duration(0)
	s := &ProcessSampler{cpus: 2, lastWall: clock, now: func() time.Time { return clock },
		read: func() (time.Duration, error) { return cpu, nil }}
	clock = clock.Add(10 * time.Second)
	cpu = 5 * time.Second // 5 s de CPU en 10 s sobre 2 núcleos: 25 %.
	if got := s.CPUPercent(); got < 24.99 || got > 25.01 {
		t.Fatalf("primera lectura = %.2f", got)
	}
	// Una segunda lectura enseguida devuelve la anterior, no una distinta.
	clock = clock.Add(500 * time.Millisecond)
	cpu += 5 * time.Second
	if got := s.CPUPercent(); got < 24.99 || got > 25.01 {
		t.Fatalf("lectura dentro de minGap = %.2f", got)
	}
	// Ya pasó el plazo: desde la última lectura real hay 5 s de CPU en 10,5 s
	// sobre 2 núcleos.
	clock = clock.Add(10 * time.Second)
	if got := s.CPUPercent(); got < 23.80 || got > 23.82 {
		t.Fatalf("lectura nueva = %.2f, se esperaba 23.81", got)
	}
}

func TestDiskSpace(t *testing.T) {
	free, total, err := DiskSpace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if total == 0 || free == 0 || free > total {
		t.Fatalf("libre %d, total %d", free, total)
	}
	if _, _, err := DiskSpace("/ruta/que/no/existe/seguro"); err == nil {
		t.Error("una ruta inexistente debía fallar")
	}
}

func TestProcessCPUTimeAvanza(t *testing.T) {
	a, err := processCPUTime()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(200 * time.Millisecond)
	x := 0
	for time.Now().Before(deadline) {
		x++
	}
	b, _ := processCPUTime()
	if b <= a || x == 0 {
		t.Fatalf("el tiempo de CPU no avanzó: %v -> %v", a, b)
	}
}
