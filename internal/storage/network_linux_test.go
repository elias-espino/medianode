//go:build linux

package storage

import "testing"

func TestIsNetworkMagic(t *testing.T) {
	for _, m := range []uint32{magicNFS, magicSMB, magicCIFS, magicSMB2} {
		if !isNetworkMagic(m) {
			t.Fatalf("%#x debería ser de red", m)
		}
	}
	if isNetworkMagic(0xEF53) { // ext4
		t.Fatal("ext4 no es de red")
	}
}
