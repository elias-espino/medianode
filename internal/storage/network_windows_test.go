//go:build windows

package storage

import "testing"

func TestRutaUNCEsDeRed(t *testing.T) {
	network, err := IsNetworkPath(`\\servidor\recurso\data`)
	if err != nil {
		t.Fatal(err)
	}
	if !network {
		t.Fatal("una ruta UNC debe considerarse de red")
	}
}
