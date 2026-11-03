package monitoring

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewLoggerEscribeJSONEnArchivoYConsola(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	var console bytes.Buffer

	log, closer, err := NewLogger(LogConfig{Dir: dir, Level: "info", Console: &console})
	if err != nil {
		t.Fatal(err)
	}
	module := Module(log, "prueba")
	module.Info().Str("job_id", "j1").Msg("hola")
	log.Debug().Msg("no debe aparecer")
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, logFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, console.Bytes()) {
		t.Fatalf("archivo y consola difieren:\n%s\n%s", data, console.Bytes())
	}
	if strings.Contains(string(data), "no debe aparecer") {
		t.Fatal("el nivel no se aplicó")
	}

	var entry map[string]any
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("no es JSON: %v", err)
	}
	if entry["module"] != "prueba" || entry["job_id"] != "j1" || entry["level"] != "info" {
		t.Fatalf("entrada = %v", entry)
	}
	ts, err := time.Parse(time.RFC3339Nano, entry["time"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if _, offset := ts.Zone(); offset != 0 {
		t.Fatalf("la hora debe estar en UTC: %v", ts)
	}
}

func TestNewLoggerNivelInvalido(t *testing.T) {
	if _, _, err := NewLogger(LogConfig{Dir: t.TempDir(), Level: "ruido"}); err == nil {
		t.Fatal("se esperaba un error")
	}
}

func TestWithRequestID(t *testing.T) {
	var seen string
	h := WithRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestID(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(HeaderRequestID, "inyectado\ncon salto")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if len(seen) != 32 {
		t.Fatalf("id = %q", seen)
	}
	if rec.Header().Get(HeaderRequestID) != seen {
		t.Fatal("la cabecera y el contexto deben coincidir")
	}
}

func TestRequestIDSinValor(t *testing.T) {
	if id := RequestID(httptest.NewRequest(http.MethodGet, "/", nil).Context()); id != "" {
		t.Fatalf("id = %q", id)
	}
}
