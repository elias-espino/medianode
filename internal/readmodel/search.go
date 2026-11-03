package readmodel

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"medianode/internal/authorization"
	"medianode/internal/core"
)

// Límites de la búsqueda.
const (
	maxQueryLength = 200
	maxQueryTerms  = 10
	DefaultResults = 20
	MaxResults     = 100
)

// SearchResult es un elemento encontrado. ParentTitle es la serie de un
// episodio, para mostrarlo con contexto.
type SearchResult struct {
	ID          string
	Type        string
	Title       string
	Year        *int64
	LibraryID   string
	ParentID    *string
	ParentTitle *string
}

// SQLC no admite MATCH sobre FTS5 (ADR 0002). Cualquier permiso sobre la
// biblioteca incluye ver; la edad de un episodio sale de su temporada o su
// serie, igual que en authorization.
const searchSQL = `
SELECT mi.id, mi.type, mi.title, mi.year, mi.library_id, mi.parent_id, g.title
FROM media_search ms
JOIN media_items mi ON mi.id = ms.media_item_id
JOIN libraries l ON l.id = mi.library_id
LEFT JOIN media_items p ON p.id = mi.parent_id
LEFT JOIN media_items g ON g.id = p.parent_id
WHERE media_search MATCH ?1
  AND (?2 = 1 OR (l.enabled = 1 AND EXISTS (
    SELECT 1 FROM user_permissions up
    WHERE up.user_id = ?3 AND up.library_id = mi.library_id
  )))
  AND (?4 = 0 OR COALESCE(mi.min_age, p.min_age, g.min_age, -1) BETWEEN 0 AND ?5)
ORDER BY bm25(media_search), mi.id
LIMIT ?6`

// Search busca por título en las bibliotecas que el actor puede ver. Sin
// acentos ni mayúsculas, y cada palabra como prefijo: «pelic» encuentra
// «Película».
func (c *Catalog) Search(ctx context.Context, actor authorization.Actor, query string, limit int) ([]SearchResult, error) {
	match, err := matchExpr(query)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = DefaultResults
	}
	limit = min(limit, MaxResults)

	admin := 0
	if authorization.IsAdmin(actor) {
		admin = 1
	}
	restricted, maxAge := ageFilter(actor)
	rows, err := c.db.Read.QueryContext(ctx, searchSQL, match, admin, actor.UserID, restricted, maxAge, limit)
	if err != nil {
		return nil, fmt.Errorf("buscar: %w", err)
	}
	defer rows.Close()
	out := []SearchResult{}
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.ID, &r.Type, &r.Title, &r.Year, &r.LibraryID, &r.ParentID, &r.ParentTitle); err != nil {
			return nil, fmt.Errorf("leer resultado: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("buscar: %w", err)
	}
	return out, nil
}

// matchExpr convierte el texto del usuario en una expresión FTS5 segura: solo
// letras y dígitos, cada palabra entre comillas y como prefijo. Así un texto
// con comillas, AND o NEAR no altera la consulta ni la hace fallar.
func matchExpr(query string) (string, error) {
	if utf8.RuneCountInString(query) > maxQueryLength {
		return "", core.ErrValidation("La búsqueda es demasiado larga")
	}
	terms := strings.FieldsFunc(query, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(terms) == 0 {
		return "", core.ErrValidation("La búsqueda está vacía")
	}
	if len(terms) > maxQueryTerms {
		terms = terms[:maxQueryTerms]
	}
	for i, t := range terms {
		terms[i] = `"` + t + `"*`
	}
	return strings.Join(terms, " "), nil
}
