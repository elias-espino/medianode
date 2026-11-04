//go:build !windows && !linux && !darwin && !freebsd

package dlna

import "syscall"

// reuseAddr no hace nada: sin compartir el puerto, si otro programa lo tiene
// ocupado SSDP no arranca y el servidor lo avisa en el log.
func reuseAddr(_, _ string, _ syscall.RawConn) error { return nil }
