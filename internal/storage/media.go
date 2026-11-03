package storage

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Errores de ValidateLibraryRoot. Sus mensajes no incluyen la ruta.
var (
	ErrRootNotAbsolute = errors.New("la ruta debe ser absoluta")
	ErrRootUnavailable = errors.New("la ruta no existe, no es un directorio o no se puede leer")
	ErrRootReserved    = errors.New("la ruta se solapa con los directorios del servidor")
)

// ValidateLibraryRoot comprueba que path pueda ser raíz de una biblioteca y la
// devuelve limpia. reserved son los directorios del servidor: una biblioteca
// que los contenga indexaría, por ejemplo, los segmentos de transcodificación.
func ValidateLibraryRoot(path string, reserved []string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", ErrRootNotAbsolute
	}
	path = filepath.Clean(path)
	if !RootAvailable(path) {
		return "", ErrRootUnavailable
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", ErrRootUnavailable
	}
	for _, r := range reserved {
		if r == "" {
			continue
		}
		if realR, err := filepath.EvalSymlinks(r); err == nil {
			r = realR
		}
		if within(r, real) || within(real, r) {
			return "", ErrRootReserved
		}
	}
	return path, nil
}

// RootAvailable indica si path es un directorio que se puede listar.
func RootAvailable(path string) bool {
	_, err := listOne(path)
	return err == nil
}

// RootEmpty indica si el directorio no tiene ninguna entrada. Un punto de
// montaje sin el disco montado suele verse así.
func RootEmpty(path string) (bool, error) {
	names, err := listOne(path)
	return len(names) == 0, err
}

func listOne(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, ErrRootUnavailable
	}
	names, err := f.Readdirnames(1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return names, nil
}

// IsWithinRoot indica si target, con sus symlinks resueltos, queda dentro de
// root, también resuelta.
func IsWithinRoot(root, target string) (bool, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false, fmt.Errorf("resolver raíz: %w", err)
	}
	realTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return false, fmt.Errorf("resolver destino: %w", err)
	}
	return within(realRoot, realTarget), nil
}

// within indica si child es parent o está dentro de él. En Windows,
// filepath.Rel compara sin distinguir mayúsculas.
func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

// OpenMedia abre un archivo de una biblioteca. Es el único punto del servidor
// que abre medios. os.Root impide salir de la raíz, también mediante symlinks
// o «..». relativePath usa «/» y nunca procede de un cliente.
func OpenMedia(root, relativePath string) (*os.File, error) {
	rel := filepath.FromSlash(relativePath)
	if !filepath.IsLocal(rel) {
		return nil, fmt.Errorf("ruta relativa no válida")
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("abrir raíz: %w", err)
	}
	defer r.Close()
	f, err := r.Open(rel)
	if err != nil {
		return nil, fmt.Errorf("abrir archivo: %w", err)
	}
	return f, nil
}

// Overlaps indica si las rutas coinciden o una contiene a la otra.
func Overlaps(a, b string) bool {
	return within(a, b) || within(b, a)
}

// Within indica si child es parent o está dentro de él.
func Within(parent, child string) bool {
	return within(parent, child)
}
