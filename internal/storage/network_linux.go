//go:build linux

package storage

import (
	"fmt"
	"syscall"
)

// Valores de statfs(2) para sistemas de archivos de red.
const (
	magicNFS  = 0x6969
	magicSMB  = 0x517B
	magicCIFS = 0xFF534D42
	magicSMB2 = 0xFE534D42
)

func isNetworkPath(path string) (bool, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return false, fmt.Errorf("statfs %s: %w", path, err)
	}
	return isNetworkMagic(uint32(st.Type)), nil // #nosec G115: el valor mágico cabe en 32 bits.
}

func isNetworkMagic(magic uint32) bool {
	switch magic {
	case magicNFS, magicSMB, magicCIFS, magicSMB2:
		return true
	}
	return false
}
