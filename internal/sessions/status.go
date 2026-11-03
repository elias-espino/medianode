// Package sessions gestiona el ciclo de vida de las sesiones de reproducción
// (§12): estados, heartbeats, progreso y expiración por inactividad.
package sessions

import "errors"

// Status es el estado de una sesión. En la API se escribe en minúsculas.
type Status string

// Estados de §12.1.
const (
	Created      Status = "created"
	Initializing Status = "initializing"
	Playing      Status = "playing"
	Paused       Status = "paused"
	Stopping     Status = "stopping"
	Completed    Status = "completed"
	Stopped      Status = "stopped"
	Failed       Status = "failed"
	Expired      Status = "expired"
)

// ErrInvalidTransition indica un cambio de estado fuera de la tabla.
var ErrInvalidTransition = errors.New("transición de estado no permitida")

// transitions es la tabla de §12.1. Desde cualquier estado no terminal se
// puede fallar o expirar. Respecto al plan se añaden tres:
//   - initializing → paused: un navegador que bloquea la reproducción
//     automática empieza en pausa.
//   - created → stopping: cerrar una sesión recién creada.
//   - stopping → expired: el documento permite expirar desde cualquier
//     estado no terminal.
var transitions = map[Status][]Status{
	Created:      {Initializing, Stopping, Failed, Expired},
	Initializing: {Playing, Paused, Stopping, Failed, Expired},
	Playing:      {Paused, Stopping, Failed, Expired},
	Paused:       {Playing, Stopping, Failed, Expired},
	Stopping:     {Completed, Stopped, Failed, Expired},
}

// Terminal indica si la sesión ya terminó.
func (s Status) Terminal() bool {
	switch s {
	case Completed, Stopped, Failed, Expired:
		return true
	}
	return false
}

// CanTransition indica si se puede pasar de from a to. Seguir en el mismo
// estado no es una transición: un heartbeat «playing» repetido es válido.
func CanTransition(from, to Status) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}
