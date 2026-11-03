//go:build windows

package storage

import (
	"fmt"
	"path/filepath"
	"syscall"
	"unsafe"
)

const driveRemote = 4

func isNetworkPath(path string) (bool, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}

	// Sin letra de unidad es una ruta UNC, como \\servidor\recurso.
	volume := filepath.VolumeName(abs)
	if len(volume) != 2 || volume[1] != ':' {
		return true, nil
	}

	root, err := syscall.UTF16PtrFromString(volume + `\`)
	if err != nil {
		return false, fmt.Errorf("ruta inválida %s: %w", path, err)
	}
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetDriveTypeW")
	kind, _, _ := proc.Call(uintptr(unsafe.Pointer(root))) // #nosec G103: llamada a la API de Windows.
	return kind == driveRemote, nil
}
