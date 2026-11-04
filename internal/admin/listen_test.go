package admin

import (
	"net"
	"testing"
)

type testListener struct {
	net.Listener
	port int
}

// listen abre un puerto libre en el bucle local.
func listen(t *testing.T) *testListener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return &testListener{Listener: ln, port: ln.Addr().(*net.TCPAddr).Port}
}
