package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func panel() http.Handler {
	return newHandler(fstest.MapFS{
		"index.html":          {Data: []byte("<!doctype html><title>MediaNode</title>")},
		"assets/index-abc.js": {Data: []byte("console.log(1)")},
	})
}

func get(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestSinCompilarNoHayPanel(t *testing.T) {
	if newHandler(fstest.MapFS{".gitkeep": {}}) != nil {
		t.Fatal("sin index.html no debe montarse el panel")
	}
}

func TestRutasDelPanelDevuelvenIndex(t *testing.T) {
	h := panel()
	for _, path := range []string{"/", "/libraries", "/media/abc", "/index.html", "/assets", "/assets/"} {
		rec := get(h, http.MethodGet, path)
		if path == "/index.html" && rec.Code == http.StatusMovedPermanently {
			continue // ServeFileFS redirige /index.html a /.
		}
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "MediaNode") {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		if rec.Header().Get("Cache-Control") != "no-cache" {
			t.Fatalf("%s: index no debe cachearse: %v", path, rec.Header())
		}
		if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'self'") {
			t.Fatalf("%s: falta la CSP", path)
		}
		// hls.js necesita blob: para el vídeo (MediaSource) y su worker.
		policy := rec.Header().Get("Content-Security-Policy")
		for _, want := range []string{"media-src 'self' blob:", "worker-src 'self' blob:"} {
			if !strings.Contains(policy, want) {
				t.Fatalf("%s: la CSP no permite %q: %s", path, want, policy)
			}
		}
	}
}

func TestRecursos(t *testing.T) {
	h := panel()
	rec := get(h, http.MethodGet, "/assets/index-abc.js")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	if rec := get(h, http.MethodGet, "/assets/falta.js"); rec.Code != http.StatusNotFound {
		t.Fatalf("un recurso que falta no es una ruta del panel: %d", rec.Code)
	}
	if rec := get(h, http.MethodPost, "/"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("%d", rec.Code)
	}
	// Fuera de dist no hay nada que servir: se normaliza a «embed.go», que no existe.
	if rec := get(h, http.MethodGet, "/../embed.go"); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "package") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
