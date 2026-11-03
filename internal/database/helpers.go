package database

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// TimeFormat es el formato único de fechas de §6.3. Su ancho fijo permite
// comparar y ordenar las fechas como texto.
const TimeFormat = "2006-01-02T15:04:05.000Z"

// NewID genera un UUID v7, que ordena por creación e inserta mejor en índices.
func NewID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generar id: %w", err)
	}
	return id.String(), nil
}

// Now devuelve la hora actual en el formato de la base.
func Now() string {
	return FormatTime(time.Now())
}

// FormatTime convierte t al formato de la base, en UTC.
func FormatTime(t time.Time) string {
	return t.UTC().Format(TimeFormat)
}

// ParseTime interpreta una fecha guardada en la base.
func ParseTime(s string) (time.Time, error) {
	t, err := time.Parse(TimeFormat, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("fecha inválida %q: %w", s, err)
	}
	return t, nil
}
