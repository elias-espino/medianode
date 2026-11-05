// Package web incrusta el panel compilado (web/dist) en el binario.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// csp limita el panel a su propio origen. Las carátulas de las coincidencias
// llegan directamente de TMDB al elegir una en la identificación manual.
// hls.js reproduce con MediaSource, que expone el vídeo como una URL blob:, y
// lanza su worker desde otra; sin media-src y worker-src el navegador las bloquea.
const csp = "default-src 'self'; img-src 'self' data: https://image.tmdb.org; " +
	"media-src 'self' blob:; worker-src 'self' blob:; " +
	"object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'"

// Handler sirve el panel, o devuelve nil si no se compiló (dist solo tiene
// .gitkeep). Las rutas sin archivo devuelven index.html, porque las resuelve
// el enrutador del navegador.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil
	}
	return newHandler(sub)
}

func newHandler(sub fs.FS) http.Handler {
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil
	}
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")

		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" || name == "index.html" {
			serveIndex(w, r, sub)
			return
		}
		// Una carpeta tampoco se sirve: FileServer listaría su contenido.
		if info, err := fs.Stat(sub, name); err != nil || info.IsDir() {
			if err != nil && path.Ext(name) != "" {
				http.NotFound(w, r) // Un recurso que falta no es una ruta del panel.
				return
			}
			serveIndex(w, r, sub)
			return
		}
		// Vite pone un hash en el nombre de lo que hay en assets/.
		if strings.HasPrefix(name, "assets/") {
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}

// serveIndex no se cachea: apunta a los recursos de la versión instalada.
func serveIndex(w http.ResponseWriter, r *http.Request, sub fs.FS) {
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFileFS(w, r, sub, "index.html")
}
