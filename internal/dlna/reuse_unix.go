//go:build linux || darwin || freebsd

package dlna

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// reuseAddr permite que otros programas UPnP del equipo (avahi, minidlna,
// otro MediaNode) compartan el puerto.
func reuseAddr(_, _ string, c syscall.RawConn) error {
	var serr error
	if err := c.Control(func(fd uintptr) {
		if serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); serr != nil {
			return
		}
		serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
	}); err != nil {
		return err
	}
	return serr
}
