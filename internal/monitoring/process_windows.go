//go:build windows

package monitoring

import (
	"time"

	"golang.org/x/sys/windows"
)

// processCPUTime es el tiempo de CPU (núcleo y usuario) que lleva el proceso.
func processCPUTime() (time.Duration, error) {
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &creation, &exit, &kernel, &user); err != nil {
		return 0, err
	}
	// FILETIME cuenta intervalos de 100 ns.
	ticks := func(f windows.Filetime) int64 { return int64(f.HighDateTime)<<32 | int64(f.LowDateTime) }
	return time.Duration(ticks(kernel)+ticks(user)) * 100, nil
}

// DiskSpace devuelve el espacio libre y el total del volumen que contiene path.
func DiskSpace(path string) (free, total uint64, err error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, err
	}
	var avail, totalBytes, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &totalBytes, &totalFree); err != nil {
		return 0, 0, err
	}
	return avail, totalBytes, nil
}
