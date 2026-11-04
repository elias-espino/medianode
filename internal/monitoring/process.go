package monitoring

import (
	"runtime"
	"sync"
	"time"
)

// ProcessSampler calcula el uso de CPU del servidor entre dos lecturas, como
// porcentaje de toda la capacidad de la máquina (100 % es usar todos los
// núcleos). Consultar con más frecuencia que minGap devuelve la última medida,
// para que varias pantallas abiertas a la vez no la falseen.
type ProcessSampler struct {
	mu       sync.Mutex
	lastCPU  time.Duration
	lastWall time.Time
	last     float64
	cpus     int
	now      func() time.Time
	read     func() (time.Duration, error)
}

const minGap = 2 * time.Second

// NewProcessSampler crea el medidor; la primera lectura promedia desde el
// arranque.
func NewProcessSampler() *ProcessSampler {
	return &ProcessSampler{cpus: runtime.NumCPU(), now: time.Now, read: processCPUTime, lastWall: time.Now()}
}

// CPUPercent devuelve el uso de CPU del proceso desde la lectura anterior.
func (p *ProcessSampler) CPUPercent() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	wall := now.Sub(p.lastWall)
	if wall < minGap && p.lastCPU != 0 {
		return p.last
	}
	cpu, err := p.read()
	if err != nil || wall <= 0 {
		return p.last
	}
	p.last = percent(cpu-p.lastCPU, wall, p.cpus)
	p.lastCPU, p.lastWall = cpu, now
	return p.last
}

// percent es el tiempo de CPU consumido en wall, sobre la capacidad de cpus
// núcleos.
func percent(cpu, wall time.Duration, cpus int) float64 {
	if wall <= 0 || cpus <= 0 || cpu < 0 {
		return 0
	}
	v := float64(cpu) / (float64(wall) * float64(cpus)) * 100
	return min(v, 100)
}
