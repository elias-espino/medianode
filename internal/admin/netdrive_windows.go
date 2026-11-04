//go:build windows

package admin

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

// isMappedNetworkDrive indica si path está en una unidad de red asignada a una
// letra (Z:\, por ejemplo). Un servicio de Windows no ve esas letras, que son de
// cada sesión de usuario: hay que registrar la biblioteca con su ruta UNC
// (\servidor\recurso).
func isMappedNetworkDrive(path string) bool {
	vol := filepath.VolumeName(path)
	if len(vol) != 2 || vol[1] != ':' {
		return false // Ya es UNC, o no tiene letra.
	}
	root, err := windows.UTF16PtrFromString(vol + `\`)
	if err != nil {
		return false
	}
	return windows.GetDriveType(root) == windows.DRIVE_REMOTE
}
