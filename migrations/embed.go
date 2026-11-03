// Package migrations embebe las migraciones SQL en el binario.
package migrations

import "embed"

// FS contiene las migraciones, ordenadas por su prefijo numérico.
//
//go:embed *.sql
var FS embed.FS
