//go:build !linux && !windows

package transcoding

import (
	"os"
	"os/exec"
)

// En macOS no hay un equivalente a Pdeathsig ni a los Job Objects: §12.5 pide
// un proceso auxiliar de supervisión, que aún no existe. Si el servidor muere
// de golpe, FFmpeg puede seguir vivo hasta terminar su trabajo.
func prepare(*exec.Cmd) {}

func attach(*os.Process) error { return nil }
