package database

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestNewIDEsUUIDv7Ordenable(t *testing.T) {
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

	var ids []string
	for range 3 {
		id, err := NewID()
		if err != nil {
			t.Fatal(err)
		}
		if !pattern.MatchString(id) {
			t.Fatalf("id = %q", id)
		}
		ids = append(ids, id)
		time.Sleep(2 * time.Millisecond)
	}
	if !slices.IsSorted(ids) {
		t.Fatalf("no ordenan por creación: %v", ids)
	}
}

func TestFormatTimeTieneAnchoFijoEnUTC(t *testing.T) {
	loc := time.FixedZone("prueba", -5*3600)
	got := FormatTime(time.Date(2026, 11, 2, 10, 0, 0, 0, loc))
	if got != "2026-11-02T15:00:00.000Z" {
		t.Fatalf("formato = %q", got)
	}
}

func TestFechasOrdenanComoTexto(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	dates := []string{
		FormatTime(base),
		FormatTime(base.Add(time.Millisecond)),
		FormatTime(base.Add(time.Second)),
		FormatTime(base.Add(24 * time.Hour)),
	}
	if !slices.IsSorted(dates) {
		t.Fatalf("no ordenan: %v", dates)
	}
}

func TestParseTime(t *testing.T) {
	want := time.Date(2026, 11, 2, 15, 0, 0, 123_000_000, time.UTC)
	got, err := ParseTime(FormatTime(want))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(want) {
		t.Fatalf("%v != %v", got, want)
	}
	if _, err := ParseTime("2026-11-02 15:00:00"); err == nil || !strings.Contains(err.Error(), "fecha inválida") {
		t.Fatalf("error = %v", err)
	}
}

func TestNowUsaElFormatoUnico(t *testing.T) {
	if _, err := ParseTime(Now()); err != nil {
		t.Fatal(err)
	}
}
