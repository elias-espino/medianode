//go:build !windows

package admin

// isMappedNetworkDrive solo existe en Windows: las letras de unidad no tienen
// equivalente en otros sistemas.
func isMappedNetworkDrive(string) bool { return false }
