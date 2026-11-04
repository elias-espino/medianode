package admin

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"medianode/internal/monitoring"
)

// tailBytes es lo máximo que se lee del final del archivo de log: lo reciente
// cabe de sobra y un log de 50 MiB no se carga entero para ver 200 líneas.
const tailBytes = 4 << 20

// MaxLogLines es el tope de líneas que se devuelven.
const MaxLogLines = 1000

// LogEntry es una línea del log, ya interpretada.
type LogEntry struct {
	Time    string         `json:"time"`
	Level   string         `json:"level"`
	Module  string         `json:"module,omitempty"`
	Message string         `json:"message"`
	Fields  map[string]any `json:"fields,omitempty"`
}

var levelRank = map[string]int{"trace": 0, "debug": 1, "info": 2, "warn": 3, "error": 4, "fatal": 5, "panic": 6}

// ValidLevel indica si es un nivel de log conocido.
func ValidLevel(l string) bool { _, ok := levelRank[l]; return ok }

// secretField reconoce campos que nunca deben salir del servidor.
var secretField = regexp.MustCompile(`(?i)token|secret|password|passwd|authorization|api_?key|cookie|hash`)

// streamURL reconoce la URL opaca de reproducción, cuyo último segmento es el
// token de la sesión (§22.1).
var streamURL = regexp.MustCompile(`/stream/session/[^/\s"]+/[^/\s"]+`)

func scrub(s string) string { return streamURL.ReplaceAllString(s, "/stream/session/[oculto]") }

// Logs devuelve las últimas líneas del log, las más recientes primero.
// minLevel filtra por nivel mínimo; q, por texto en el mensaje o los campos.
// Aunque el servidor ya no registra secretos, lo que sale aquí se vuelve a
// limpiar.
func (s *Service) Logs(lines int, minLevel, q string) ([]LogEntry, error) {
	lines = min(max(lines, 1), MaxLogLines)
	path := filepath.Join(s.Config.Paths.Logs, monitoring.LogFileName)
	data, err := readTail(path, tailBytes)
	if errors.Is(err, os.ErrNotExist) {
		return []LogEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	want := levelRank[strings.ToLower(minLevel)]
	q = strings.ToLower(strings.TrimSpace(q))
	var out []LogEntry
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		e, ok := parseLine(sc.Bytes())
		if !ok || levelRank[e.Level] < want {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(e.Message+" "+e.Module+" "+flatten(e.Fields)), q) {
			continue
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	// Las más recientes primero y solo las pedidas.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if len(out) > lines {
		out = out[:lines]
	}
	if out == nil {
		out = []LogEntry{}
	}
	return out, nil
}

func flatten(m map[string]any) string {
	var b strings.Builder
	for k, v := range m {
		b.WriteString(k)
		b.WriteByte(' ')
		if s, ok := v.(string); ok {
			b.WriteString(s)
		}
		b.WriteByte(' ')
	}
	return b.String()
}

// parseLine interpreta una línea JSON del log; una que no lo es se descarta.
func parseLine(line []byte) (LogEntry, bool) {
	var raw map[string]any
	if json.Unmarshal(line, &raw) != nil {
		return LogEntry{}, false
	}
	str := func(k string) string { s, _ := raw[k].(string); delete(raw, k); return s }
	e := LogEntry{Time: str("time"), Level: str("level"), Module: str("module"), Message: scrub(str("message"))}
	if e.Level == "" || e.Time == "" {
		return LogEntry{}, false
	}
	for k, v := range raw {
		if secretField.MatchString(k) {
			raw[k] = "[oculto]"
			continue
		}
		if s, ok := v.(string); ok {
			raw[k] = scrub(s)
		}
	}
	if len(raw) > 0 {
		e.Fields = raw
	}
	return e, true
}

// readTail lee el final de un archivo, empezando en una línea entera.
func readTail(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	start := max(info.Size()-n, 0)
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	if start > 0 {
		// La primera línea puede estar cortada.
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	return data, nil
}
