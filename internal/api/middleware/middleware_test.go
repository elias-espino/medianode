package middleware

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"medianode/internal/monitoring"
)

func chain(log zerolog.Logger, h http.Handler) http.Handler {
	return monitoring.WithRequestID(AccessLog(log)(Recover(log)(LimitBody(16)(h))))
}

func TestRecoverDevuelve500Estructurado(t *testing.T) {
	var logs syncBuffer
	h := chain(zerolog.New(&logs), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("fallo con ruta secreta C:\\medios\\peli.mkv")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("estado = %d", rec.Code)
	}
	var body struct {
		Error struct {
			Code      string `json:"code"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "INTERNAL_ERROR" || body.Error.RequestID == "" {
		t.Fatalf("cuerpo = %s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), "secreta") {
		t.Fatal("el detalle del panic no debe llegar al cliente")
	}
	if !strings.Contains(logs.String(), "secreta") {
		t.Fatal("el detalle del panic debe quedar en los logs")
	}
}

func TestRecoverNoSustituyeRespuestaYaIniciada(t *testing.T) {
	h := chain(zerolog.Nop(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, "parcial")
		panic("tarde")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusAccepted || rec.Body.String() != "parcial" {
		t.Fatalf("respuesta alterada: %d %q", rec.Code, rec.Body)
	}
}

func TestRecoverRespetaErrAbortHandler(t *testing.T) {
	h := Recover(zerolog.Nop())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	defer func() {
		if rec := recover(); rec != http.ErrAbortHandler {
			t.Fatalf("recover = %v", rec)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
}

func TestAccessLogNoExponeSecretos(t *testing.T) {
	var logs syncBuffer
	h := chain(zerolog.New(&logs), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/stream/session/abc/tokensecreto/file?x=querysecreta", nil)
	req.Header.Set("Authorization", "Bearer cabecerasecreta")
	req.Header.Set("Cookie", "s=cookiesecreta")
	h.ServeHTTP(httptest.NewRecorder(), req)

	out := logs.String()
	for _, secret := range []string{"tokensecreto", "querysecreta", "cabecerasecreta", "cookiesecreta"} {
		if strings.Contains(out, secret) {
			t.Fatalf("el log contiene %q: %s", secret, out)
		}
	}
	if !strings.Contains(out, `"status":200`) || !strings.Contains(out, "request_id") {
		t.Fatalf("log incompleto: %s", out)
	}
}

func TestAccessLogNivelPorEstado(t *testing.T) {
	cases := []struct {
		path   string
		status int
		want   string
	}{
		{"/a", http.StatusOK, "info"},
		{"/a", http.StatusNotFound, "warn"},
		{"/a", http.StatusBadGateway, "error"},
		{"/health", http.StatusOK, "debug"},
	}
	for _, c := range cases {
		var logs syncBuffer
		h := chain(zerolog.New(&logs), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
		}))
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, c.path, nil))
		if !strings.Contains(logs.String(), `"level":"`+c.want+`"`) {
			t.Fatalf("%s %d: %s", c.path, c.status, logs.String())
		}
	}
}

func TestLimitBody(t *testing.T) {
	var readErr error
	h := chain(zerolog.Nop(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	}))

	body := strings.NewReader(strings.Repeat("x", 17))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/x", body))

	var tooLarge *http.MaxBytesError
	if !errors.As(readErr, &tooLarge) {
		t.Fatalf("error = %v", readErr)
	}
}
