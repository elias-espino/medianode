package middleware

import (
	"bytes"
	"sync"
)

// syncBuffer es un bytes.Buffer seguro entre goroutines: el servidor de la
// prueba escribe los logs mientras la prueba los lee.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
