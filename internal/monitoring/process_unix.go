//go:build !windows

package monitoring

import (
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// processCPUTime es el tiempo de CPU (usuario y sistema) que lleva el proceso.
func processCPUTime() (time.Duration, error) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0, err
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano()), nil
}

// DiskSpace devuelve el espacio libre y el total del volumen que contiene path.
func DiskSpace(path string) (free, total uint64, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bsize := uint64(st.Bsize)                                        //nolint:unconvert // el tipo varía según el sistema
	return uint64(st.Bavail) * bsize, uint64(st.Blocks) * bsize, nil //nolint:unconvert
}
