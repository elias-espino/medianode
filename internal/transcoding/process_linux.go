//go:build linux

package transcoding

import (
	"os"
	"os/exec"
	"syscall"
)

// prepare pide al núcleo que mate a FFmpeg si muere el hilo que lo lanzó
// (§12.5). Pdeathsig va ligado al hilo, no al proceso: el servidor puede
// morir sin que el hilo concreto termine antes, pero si el proceso muere,
// mueren todos sus hilos.
func prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}

// attach baja la prioridad del proceso.
func attach(p *os.Process) error {
	return syscall.Setpriority(syscall.PRIO_PROCESS, p.Pid, 10)
}
