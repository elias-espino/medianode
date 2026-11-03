package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	saltBytes = 16
	keyBytes  = 32
)

// Params son los costes de Argon2id.
type Params struct {
	MemoryKiB  uint32
	Iterations uint32
	Threads    uint8
}

// DefaultParams son los valores de la Fase 3.
var DefaultParams = Params{MemoryKiB: 64 * 1024, Iterations: 3, Threads: 2}

// Hasher calcula y comprueba contraseñas. Cada operación reserva 64 MiB, por lo
// que limita las que corren a la vez: sin ello, una avalancha de logins agota la
// memoria del servidor aunque cada cuenta tenga su límite de intentos.
type Hasher struct {
	params Params
	slots  chan struct{}
}

// NewHasher crea un Hasher con esos costes.
func NewHasher(p Params) *Hasher {
	return &Hasher{params: p, slots: make(chan struct{}, max(runtime.NumCPU()/2, 1))}
}

func (h *Hasher) acquire(ctx context.Context) error {
	select {
	case h.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Hasher) release() { <-h.slots }

// Hash devuelve la contraseña en formato PHC.
func (h *Hasher) Hash(ctx context.Context, password string) (string, error) {
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()

	salt := make([]byte, saltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generar sal: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, h.params.Iterations, h.params.MemoryKiB, h.params.Threads, keyBytes)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, h.params.MemoryKiB, h.params.Iterations, h.params.Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// Verify comprueba la contraseña con los costes guardados en encoded, que
// sobreviven a un cambio de los valores por defecto.
func (h *Hasher) Verify(ctx context.Context, encoded, password string) (bool, error) {
	p, salt, want, err := parseHash(encoded)
	if err != nil {
		return false, err
	}
	if err := h.acquire(ctx); err != nil {
		return false, err
	}
	defer h.release()

	got := argon2.IDKey([]byte(password), salt, p.Iterations, p.MemoryKiB, p.Threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func parseHash(encoded string) (Params, []byte, []byte, error) {
	var p Params
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return p, nil, nil, errors.New("hash de contraseña con formato desconocido")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return p, nil, nil, errors.New("hash de contraseña con versión desconocida")
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.MemoryKiB, &p.Iterations, &p.Threads); err != nil {
		return p, nil, nil, fmt.Errorf("costes del hash: %w", err)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return p, nil, nil, fmt.Errorf("sal del hash: %w", err)
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return p, nil, nil, fmt.Errorf("clave del hash: %w", err)
	}
	return p, salt, key, nil
}
