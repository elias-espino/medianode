// Package storage gestiona los directorios de datos del servidor.
package storage

import (
	"fmt"
	"os"
)

// EnsureDirs crea los directorios indicados si no existen.
func EnsureDirs(paths ...string) error {
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o750); err != nil {
			return fmt.Errorf("crear directorio %s: %w", p, err)
		}
	}
	return nil
}

// IsNetworkPath indica si path está en un sistema de archivos de red. La base
// SQLite no debe vivir ahí: WAL no funciona sobre NFS ni SMB (§2, principio 6).
// path debe existir.
func IsNetworkPath(path string) (bool, error) {
	return isNetworkPath(path)
}
