//go:build windows

package dlna

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// reuseAddr permite que otros programas UPnP del equipo (el servicio SSDP de
// Windows, por ejemplo) compartan el puerto.
func reuseAddr(_, _ string, c syscall.RawConn) error {
	var serr error
	if err := c.Control(func(fd uintptr) {
		serr = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_REUSEADDR, 1)
	}); err != nil {
		return err
	}
	return serr
}
