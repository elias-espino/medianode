package dlna

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"medianode/internal/database/sqlc"
)

// maxSearchRows acota cuántos candidatos se evalúan en una búsqueda: el criterio
// se interpreta en memoria sobre títulos, y una biblioteca doméstica cabe de
// sobra. Más allá, la búsqueda dice que no encuentra más.
const maxSearchRows = 5000

// errCriteria es un criterio ilegible o con propiedades que no se admiten
// (ContentDirectory:1 §2.5.5, error 708).
var errCriteria = errors.New("criterio de búsqueda no válido")

// Propiedades admitidas: son las que anuncia GetSearchCapabilities.
const searchCaps = "dc:title,upnp:class"

type token struct {
	kind string // "(", ")", "str", "word"
	text string
}

// tokenize parte un criterio en paréntesis, cadenas entre comillas dobles (con
// \\ y \" como únicos escapes) y palabras.
func tokenize(s string) ([]token, error) {
	var out []token
	for i := 0; i < len(s); {
		c := s[i]
		switch c {
		case ' ', '\t', '\r', '\n':
			i++
		case '(', ')':
			out = append(out, token{kind: string(c)})
			i++
		case '"':
			var b strings.Builder
			i++
			closed := false
			for i < len(s) {
				switch {
				case s[i] == '\\' && i+1 < len(s) && (s[i+1] == '"' || s[i+1] == '\\'):
					b.WriteByte(s[i+1])
					i += 2
				case s[i] == '"':
					closed = true
					i++
				default:
					b.WriteByte(s[i])
					i++
				}
				if closed {
					break
				}
			}
			if !closed {
				return nil, errCriteria
			}
			out = append(out, token{kind: "str", text: b.String()})
		default:
			j := i
			for j < len(s) && !strings.ContainsRune(" \t\r\n()\"", rune(s[j])) {
				j++
			}
			out = append(out, token{kind: "word", text: s[i:j]})
			i = j
		}
	}
	return out, nil
}

// criterion es un nodo del árbol de un criterio.
type criterion struct {
	op          string // "and", "or" o un operador de relación.
	left, right *criterion
	prop, val   string
}

// subject es lo que se compara: las dos propiedades que se admiten.
type subject struct{ title, class string }

type parser struct {
	toks  []token
	pos   int
	depth int
}

const maxDepth = 16 // Un cliente no puede pedir un árbol sin fondo.

func (p *parser) peek() (token, bool) {
	if p.pos >= len(p.toks) {
		return token{}, false
	}
	return p.toks[p.pos], true
}

func (p *parser) word(want string) bool {
	t, ok := p.peek()
	if ok && t.kind == "word" && strings.EqualFold(t.text, want) {
		p.pos++
		return true
	}
	return false
}

// parseCriteria interpreta un criterio. «*» es «todo» y devuelve nil.
func parseCriteria(s string) (*criterion, error) {
	if strings.TrimSpace(s) == "*" {
		return nil, nil
	}
	toks, err := tokenize(s)
	if err != nil {
		return nil, errCriteria
	}
	p := &parser{toks: toks}
	c, err := p.or()
	if err != nil || p.pos != len(toks) {
		return nil, errCriteria
	}
	return c, nil
}

func (p *parser) or() (*criterion, error) {
	left, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.word("or") {
		right, err := p.and()
		if err != nil {
			return nil, err
		}
		left = &criterion{op: "or", left: left, right: right}
	}
	return left, nil
}

func (p *parser) and() (*criterion, error) {
	left, err := p.primary()
	if err != nil {
		return nil, err
	}
	for p.word("and") {
		right, err := p.primary()
		if err != nil {
			return nil, err
		}
		left = &criterion{op: "and", left: left, right: right}
	}
	return left, nil
}

var relOps = []string{"=", "!=", "<", "<=", ">", ">=", "contains", "doesNotContain", "derivedfrom"}

func (p *parser) primary() (*criterion, error) {
	t, ok := p.peek()
	if !ok {
		return nil, errCriteria
	}
	if t.kind == "(" {
		if p.depth++; p.depth > maxDepth {
			return nil, errCriteria
		}
		p.pos++
		c, err := p.or()
		if err != nil {
			return nil, err
		}
		if next, ok := p.peek(); !ok || next.kind != ")" {
			return nil, errCriteria
		}
		p.pos++
		p.depth--
		return c, nil
	}
	if t.kind != "word" || (t.text != "dc:title" && t.text != "upnp:class") {
		return nil, errCriteria // Una propiedad que no se admite.
	}
	p.pos++
	opTok, ok := p.peek()
	if !ok || opTok.kind != "word" {
		return nil, errCriteria
	}
	p.pos++
	if strings.EqualFold(opTok.text, "exists") {
		v, ok := p.peek()
		if !ok || v.kind != "word" || (v.text != "true" && v.text != "false") {
			return nil, errCriteria
		}
		p.pos++
		return &criterion{op: "exists", prop: t.text, val: v.text}, nil
	}
	op := ""
	for _, o := range relOps {
		if strings.EqualFold(o, opTok.text) {
			op = o
		}
	}
	if op == "" {
		return nil, errCriteria
	}
	v, ok := p.peek()
	if !ok || v.kind != "str" {
		return nil, errCriteria
	}
	p.pos++
	// derivedfrom solo tiene sentido para la clase.
	if op == "derivedfrom" && t.text != "upnp:class" {
		return nil, errCriteria
	}
	return &criterion{op: op, prop: t.text, val: v.text}, nil
}

// match evalúa el criterio. nil lo cumple todo.
func (c *criterion) match(t subject) bool {
	if c == nil {
		return true
	}
	switch c.op {
	case "and":
		return c.left.match(t) && c.right.match(t)
	case "or":
		return c.left.match(t) || c.right.match(t)
	case "exists":
		return c.val == "true" // Los dos campos existen siempre.
	}
	got := t.title
	if c.prop == "upnp:class" {
		got = t.class
	}
	a, b := strings.ToLower(got), strings.ToLower(c.val)
	switch c.op {
	case "=":
		return a == b
	case "!=":
		return a != b
	case "contains":
		return strings.Contains(a, b)
	case "doesNotContain":
		return !strings.Contains(a, b)
	case "<":
		return a < b
	case "<=":
		return a <= b
	case ">":
		return a > b
	case ">=":
		return a >= b
	case "derivedfrom":
		// Una clase deriva de otra si la iguala o la continúa tras un punto.
		return a == b || strings.HasPrefix(a, b+".")
	}
	return false
}

func classOf(typ string) string {
	if typ == "movie" {
		return classMovie
	}
	return classEpisode
}

// search atiende ContentDirectory#Search: evalúa el criterio sobre las películas
// y los episodios publicados, dentro del contenedor pedido.
func (s *Server) search(ctx context.Context, r searchRequest) (*browseResult, *upnpError) {
	crit, err := parseCriteria(r.criteria)
	if err != nil {
		return nil, &upnpError{708, "Unsupported or invalid search criteria"}
	}
	scope, uerr := s.searchScope(ctx, r.containerID)
	if uerr != nil {
		return nil, uerr
	}
	rows, err := s.db.R.ListDLNASearchCandidates(ctx, maxSearchRows)
	if err != nil {
		return nil, s.failed(err)
	}
	var hits []sqlc.GetDLNAItemRow
	for _, row := range rows {
		if !scope(row) || !crit.match(subject{title: titleOf(toItemRow(row)), class: classOf(row.Type)}) {
			continue
		}
		hits = append(hits, toItemRow(row))
	}
	if r.desc {
		slices.Reverse(hits)
	}
	total := int64(len(hits))
	objs, uerr := s.objects(ctx, r.host, window(hits, r.start, min(pageSize(r.count), maxPage)))
	if uerr != nil {
		return nil, uerr
	}
	return &browseResult{didl: didl(objs), returned: len(objs), total: total, updateID: s.updateID(ctx)}, nil
}

type searchRequest struct {
	containerID string
	criteria    string
	start       int64
	count       int64
	desc        bool
	host        string
}

func toItemRow(r sqlc.ListDLNASearchCandidatesRow) sqlc.GetDLNAItemRow {
	return itemRow(r.ID, r.LibraryID, r.ParentID, r.Type, r.Title, r.Year, r.Overview, r.ReleaseDate, r.DurationMs, r.SeasonNumber, r.EpisodeNumber, r.AgeRating, r.LibraryType)
}

// searchScope devuelve el filtro de pertenencia al contenedor. Un contenedor que
// no existe o no está publicado es un 710.
func (s *Server) searchScope(ctx context.Context, id string) (func(sqlc.ListDLNASearchCandidatesRow) bool, *upnpError) {
	noContainer := &upnpError{710, "No such container"}
	switch {
	case id == rootID:
		return func(sqlc.ListDLNASearchCandidatesRow) bool { return true }, nil
	case strings.HasPrefix(id, libPrefix):
		lib := strings.TrimPrefix(id, libPrefix)
		if !reDBID.MatchString(lib) {
			return nil, noContainer
		}
		if _, err := s.db.R.GetDLNALibrary(ctx, lib); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, noContainer
			}
			return nil, s.failed(err)
		}
		return func(r sqlc.ListDLNASearchCandidatesRow) bool { return r.LibraryID == lib }, nil
	case strings.HasPrefix(id, itemPrefix):
		item := strings.TrimPrefix(id, itemPrefix)
		if !reDBID.MatchString(item) {
			return nil, noContainer
		}
		if _, err := s.db.R.GetDLNAItem(ctx, item); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, noContainer
			}
			return nil, s.failed(err)
		}
		return func(r sqlc.ListDLNASearchCandidatesRow) bool {
			return (r.ParentID != nil && *r.ParentID == item) || (r.GrandparentID != nil && *r.GrandparentID == item)
		}, nil
	}
	return nil, noContainer
}
