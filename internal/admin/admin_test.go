package admin

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"medianode/internal/config"
	"medianode/internal/ffmpeg"
	"medianode/internal/testutil"
)

func TestIsLocal(t *testing.T) {
	for ip, want := range map[string]bool{
		"127.0.0.1": true, "::1": true, "192.168.1.22": true, "10.0.0.5": true, "172.16.3.4": true,
		"169.254.1.1": true, "fe80::1": true, "fd00::1": true,
		"8.8.8.8": false, "1.1.1.1": false, "2001:4860:4860::8888": false,
		"": false, "no-es-ip": false, " 192.168.0.1 ": true,
	} {
		if got := IsLocal(ip); got != want {
			t.Errorf("IsLocal(%q) = %v, se esperaba %v", ip, got, want)
		}
	}
}

func TestHuman(t *testing.T) {
	cases := map[uint64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 1536: "1.5 KiB", 1 << 20: "1.0 MiB", 5 << 30: "5.0 GiB", 3 << 40: "3.0 TiB"}
	for n, want := range cases {
		if got := human(n); got != want {
			t.Errorf("human(%d) = %q, se esperaba %q", n, got, want)
		}
	}
}

func TestLocalAddresses(t *testing.T) {
	if got := localAddresses("192.168.1.22"); len(got) != 1 || got[0] != "192.168.1.22" {
		t.Errorf("una dirección concreta se devuelve tal cual: %v", got)
	}
	for _, bind := range []string{"0.0.0.0", "", "::"} {
		for _, ip := range localAddresses(bind) {
			if strings.HasPrefix(ip, "127.") || strings.Contains(ip, ":") {
				t.Errorf("%q: sin bucle local ni IPv6: %s", bind, ip)
			}
		}
	}
}

func TestDuration(t *testing.T) {
	if got := duration("2026-01-01T00:00:00.000Z", "2026-01-01T00:01:30.500Z"); got != 90.5 {
		t.Errorf("duración = %v", got)
	}
	for _, c := range [][2]string{{"x", "2026-01-01T00:00:00.000Z"}, {"2026-01-01T00:00:00.000Z", "y"}, {"2026-01-02T00:00:00.000Z", "2026-01-01T00:00:00.000Z"}} {
		if got := duration(c[0], c[1]); got != 0 {
			t.Errorf("%v: %v", c, got)
		}
	}
}

func TestSettingsOcultaLaClaveDeTMDB(t *testing.T) {
	cfg := *config.Default()
	cfg.Metadata.TMDBAPIKey = "clave-secreta-de-prueba"
	s := &Service{Config: cfg}
	got, err := s.Settings()
	if err != nil {
		t.Fatal(err)
	}
	md := got["metadata"].(map[string]any)
	if md["tmdb_api_key"] != hidden {
		t.Fatalf("tmdb_api_key = %v", md["tmdb_api_key"])
	}
	// La configuración original no se toca.
	if s.Config.Metadata.TMDBAPIKey != "clave-secreta-de-prueba" {
		t.Fatal("Settings modificó la configuración")
	}
	// Sin clave configurada no se inventa el aviso.
	s2 := &Service{Config: *config.Default()}
	got2, _ := s2.Settings()
	if v := got2["metadata"].(map[string]any)["tmdb_api_key"]; v != "" && v != nil {
		t.Fatalf("sin clave: %v", v)
	}
	// Tiene las secciones de server.yaml.
	for _, k := range []string{"server", "database", "paths", "transcoding", "sessions"} {
		if _, ok := got[k]; !ok {
			t.Errorf("falta la sección %s", k)
		}
	}
	if got["transcoding"].(map[string]any)["tone_mapping"] != "auto" {
		t.Errorf("transcoding.tone_mapping = %v", got["transcoding"])
	}
}

func writeLog(t *testing.T, dir string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "medianode.log"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLogsFiltraOrdenaYLimpia(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir,
		`{"level":"info","module":"http","time":"2026-01-01T00:00:01Z","message":"uno"}`,
		`{"level":"debug","module":"http","time":"2026-01-01T00:00:02Z","message":"dos"}`,
		`{"level":"warn","module":"scanner","time":"2026-01-01T00:00:03Z","message":"tres","ruta":"C:\\peliculas"}`,
		`{"level":"error","module":"transcoding","time":"2026-01-01T00:00:04Z","message":"cuatro","stderr":"fallo del códec"}`,
		`esto no es JSON`,
		`{"level":"info","time":"2026-01-01T00:00:05Z","message":"sin módulo"}`,
		`{"sin":"nivel"}`,
	)
	s := &Service{Config: config.Config{Paths: config.Paths{Logs: dir}}}

	all, err := s.Logs(100, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 5 || all[0].Message != "sin módulo" || all[4].Message != "uno" {
		t.Fatalf("las más recientes primero y sin las líneas inválidas: %+v", all)
	}
	warn, _ := s.Logs(100, "warn", "")
	if len(warn) != 2 || warn[0].Level != "error" || warn[1].Level != "warn" {
		t.Fatalf("nivel mínimo warn: %+v", warn)
	}
	if got, _ := s.Logs(100, "error", ""); len(got) != 1 || got[0].Module != "transcoding" {
		t.Fatalf("nivel mínimo error: %+v", got)
	}
	if got, _ := s.Logs(2, "", ""); len(got) != 2 || got[0].Message != "sin módulo" {
		t.Fatalf("límite de líneas: %+v", got)
	}
	if got, _ := s.Logs(100, "", "CÓDEC"); len(got) != 1 || got[0].Message != "cuatro" {
		t.Fatalf("búsqueda en campos, sin distinguir mayúsculas: %+v", got)
	}
	if got, _ := s.Logs(100, "", "scanner"); len(got) != 1 || got[0].Message != "tres" {
		t.Fatalf("búsqueda en el módulo: %+v", got)
	}
	if got, _ := s.Logs(100, "", "no-aparece"); len(got) != 0 {
		t.Fatalf("sin coincidencias: %+v", got)
	}
	if got, _ := s.Logs(0, "", ""); len(got) != 1 || got[0].Message != "sin módulo" {
		t.Fatalf("un límite menor que 1 se corrige a 1: %+v", got)
	}
	if got, _ := s.Logs(MaxLogLines*10, "", ""); len(got) != 5 {
		t.Fatalf("un límite enorme se acota al máximo: %d", len(got))
	}
}

func TestLogsSinArchivoDevuelveListaVacia(t *testing.T) {
	s := &Service{Config: config.Config{Paths: config.Paths{Logs: filepath.Join(t.TempDir(), "no-existe")}}}
	got, err := s.Logs(10, "", "")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
}

// Aunque el servidor no registre secretos, lo que sale al panel se limpia.
func TestLogsNoSacanSecretos(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir,
		`{"level":"info","time":"2026-01-01T00:00:01Z","message":"GET /stream/session/01abc/TOKEN-SECRETO/master.m3u8 200","url":"/stream/session/01abc/OTRO-TOKEN/file"}`,
		`{"level":"info","time":"2026-01-01T00:00:02Z","message":"login","password":"hunter2","access_token":"eyJhbGci","Authorization":"Bearer x","tmdb_api_key":"k","cookie":"c","file_hash":"h","usuario":"ana"}`,
	)
	s := &Service{Config: config.Config{Paths: config.Paths{Logs: dir}}}
	got, err := s.Logs(10, "", "")
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, e := range got {
		all.WriteString(e.Message + " ")
		for k, v := range e.Fields {
			all.WriteString(k + "=")
			if s, ok := v.(string); ok {
				all.WriteString(s)
			}
			all.WriteString(" ")
		}
	}
	text := all.String()
	for _, secret := range []string{"TOKEN-SECRETO", "OTRO-TOKEN", "hunter2", "eyJhbGci", "Bearer x"} {
		if strings.Contains(text, secret) {
			t.Errorf("el log sacó %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, "/stream/session/[oculto]") || !strings.Contains(text, "usuario=ana") {
		t.Errorf("limpió de más o de menos: %s", text)
	}
	// Un campo sensible conserva su nombre, para saber que existía.
	for _, e := range got {
		if e.Message == "login" && e.Fields["password"] != "[oculto]" {
			t.Errorf("password = %v", e.Fields["password"])
		}
	}
}

func TestReadTailEmpiezaEnUnaLineaEntera(t *testing.T) {
	path := filepath.Join(t.TempDir(), "l.log")
	var lines []string
	for i := 0; i < 50; i++ {
		lines = append(lines, `{"level":"info","time":"2026-01-01T00:00:00Z","message":"línea de relleno número `+strings.Repeat("x", 30)+`"}`)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := readTail(path, 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if !strings.HasPrefix(l, "{") || !strings.HasSuffix(l, "}") {
			t.Fatalf("línea cortada: %q", l)
		}
	}
	if len(data) > 500 || len(data) == 0 {
		t.Fatalf("leyó %d bytes", len(data))
	}
	whole, _ := readTail(path, 1<<20)
	if string(whole) != strings.Join(lines, "\n")+"\n" {
		t.Fatal("un archivo pequeño se lee entero")
	}
	if _, err := readTail(filepath.Join(t.TempDir(), "no-existe"), 10); !os.IsNotExist(err) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidLevel(t *testing.T) {
	for _, l := range []string{"debug", "info", "warn", "error"} {
		if !ValidLevel(l) {
			t.Errorf("%s es un nivel", l)
		}
	}
	for _, l := range []string{"", "ruido", "INFO"} {
		if ValidLevel(l) {
			t.Errorf("%q no es un nivel", l)
		}
	}
}

func status(checks []Check, id string) Check {
	for _, c := range checks {
		if c.ID == id {
			return c
		}
	}
	return Check{ID: id, Status: "no existe"}
}

func TestSystemCheckSinFFmpeg(t *testing.T) {
	cfg := *config.Default()
	cfg.Paths.Transcodes = t.TempDir()
	cfg.Database.Path = filepath.Join(t.TempDir(), "x.db")
	s := &Service{Config: cfg, DB: testutil.NewDB(t)}
	checks := s.SystemCheck(context.Background())
	for _, id := range []string{"ffmpeg", "ffprobe"} {
		if c := status(checks, id); c.Status != CheckFail {
			t.Errorf("%s sin instalar debe fallar: %+v", id, c)
		}
	}
	for _, id := range []string{"codecs", "hardware", "tone_mapping"} {
		if c := status(checks, id); c.Status != CheckSkipped {
			t.Errorf("%s sin FFmpeg se omite: %+v", id, c)
		}
	}
	if c := status(checks, "libraries"); c.Status != CheckSkipped {
		t.Errorf("sin bibliotecas: %+v", c)
	}
	if c := status(checks, "database"); c.Status != CheckOK {
		t.Errorf("la base de pruebas está íntegra: %+v", c)
	}
	if len(checks) != 12 {
		t.Errorf("son las 12 comprobaciones de §19.1, hay %d", len(checks))
	}
	seen := map[string]bool{}
	for _, c := range checks {
		if seen[c.ID] || c.Name == "" || c.Message == "" {
			t.Errorf("comprobación mal formada: %+v", c)
		}
		seen[c.ID] = true
		if c.Status != CheckOK && c.Status != CheckWarn && c.Status != CheckFail && c.Status != CheckSkipped {
			t.Errorf("estado desconocido: %+v", c)
		}
	}
}

func TestComprobacionesDeFFmpeg(t *testing.T) {
	s := &Service{FFmpeg: &ffmpeg.Capabilities{
		Version:  "9.0",
		Encoders: map[ffmpeg.Encoder]bool{ffmpeg.X264: true, ffmpeg.NVENC: true, ffmpeg.QSV: true},
		Working:  []ffmpeg.Encoder{ffmpeg.X264},
	}}
	if c := s.checkFFmpeg(); c.Status != CheckOK || !strings.Contains(c.Message, "9.0") {
		t.Errorf("%+v", c)
	}
	if c := s.checkCodecs(); c.Status != CheckOK {
		t.Errorf("%+v", c)
	}
	// NVENC y QSV compilados, ninguno funciona.
	if c := s.checkHardware(); c.Status != CheckWarn || !strings.Contains(c.Message, "nvenc") || !strings.Contains(c.Message, "qsv") {
		t.Errorf("hardware roto: %+v", c)
	}
	// Uno funciona y el otro no.
	s.FFmpeg.Working = append(s.FFmpeg.Working, ffmpeg.NVENC)
	if c := s.checkHardware(); c.Status != CheckOK || !strings.Contains(c.Message, "qsv") {
		t.Errorf("hardware parcial: %+v", c)
	}
	// Sin codificadores de hardware en la build.
	s.FFmpeg.Encoders = map[ffmpeg.Encoder]bool{ffmpeg.X264: true}
	if c := s.checkHardware(); c.Status != CheckOK || !strings.Contains(c.Message, "CPU") {
		t.Errorf("solo CPU: %+v", c)
	}
	s.FFmpeg.Encoders = map[ffmpeg.Encoder]bool{}
	if c := s.checkCodecs(); c.Status != CheckFail {
		t.Errorf("sin libx264: %+v", c)
	}
}

func TestComprobacionDelToneMapping(t *testing.T) {
	cases := []struct {
		name string
		caps ffmpeg.Capabilities
		want string
		text string
	}{
		{"no disponible", ffmpeg.Capabilities{}, CheckWarn, "No disponible"},
		{"lento", ffmpeg.Capabilities{ToneMapping: true, ToneMapSpeed: 0.32}, CheckWarn, "0.32"},
		{"rápido", ffmpeg.Capabilities{ToneMapping: true, ToneMapSpeed: 3.5}, CheckOK, "3.5"},
		{"sin medir", ffmpeg.Capabilities{ToneMapping: true}, CheckOK, "no se pudo medir"},
		{"justo a tiempo real", ffmpeg.Capabilities{ToneMapping: true, ToneMapSpeed: 1}, CheckOK, "1.0"},
	}
	for _, c := range cases {
		caps := c.caps
		got := (&Service{FFmpeg: &caps}).checkToneMapping()
		if got.Status != c.want || !strings.Contains(got.Message, c.text) {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

func TestComprobacionDeLasBibliotecas(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	good, gone := t.TempDir(), filepath.Join(t.TempDir(), "desaparecida")
	notDir := filepath.Join(t.TempDir(), "archivo.txt")
	if err := os.WriteFile(notDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i, p := range []string{good, gone, notDir} {
		id := string(rune('a' + i))
		if _, err := db.Write.Exec("INSERT INTO libraries (id, name, type) VALUES (?, ?, 'movies')", id, "Lib "+id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Write.Exec("INSERT INTO library_paths (id, library_id, path) VALUES (?, ?, ?)", "p"+id, id, p); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{DB: db}
	c := s.checkLibraries(ctx)
	if c.Status != CheckFail || !strings.Contains(c.Message, "no existe") || !strings.Contains(c.Message, "no es un directorio") ||
		strings.Contains(c.Message, "Lib a") {
		t.Fatalf("%+v", c)
	}
	// Solo la que está bien.
	if _, err := db.Write.Exec("DELETE FROM libraries WHERE id IN ('b', 'c')"); err != nil {
		t.Fatal(err)
	}
	if c := s.checkLibraries(ctx); c.Status != CheckOK || !strings.Contains(c.Message, "1 carpeta") {
		t.Fatalf("%+v", c)
	}
}

func TestComprobacionDelEspacioTemporal(t *testing.T) {
	cfg := *config.Default()
	cfg.Paths.Transcodes = t.TempDir()
	cfg.Transcoding.MaxDiskGB = 1
	s := &Service{Config: cfg}
	if c := s.checkTempSpace(); c.Status != CheckOK {
		t.Errorf("1 GiB de cuota cabe: %+v", c)
	}
	// Una cuota imposible de cumplir avisa, sin fallar.
	s.Config.Transcoding.MaxDiskGB = 1 << 30
	if c := s.checkTempSpace(); c.Status != CheckWarn || !strings.Contains(c.Message, "max_disk_gb") {
		t.Errorf("cuota enorme: %+v", c)
	}
	s.Config.Paths.Transcodes = filepath.Join(t.TempDir(), "no", "existe")
	if c := s.checkTempSpace(); c.Status != CheckWarn {
		t.Errorf("directorio inexistente: %+v", c)
	}
}

func TestComprobacionesQueNoAplican(t *testing.T) {
	s := &Service{Config: *config.Default()}
	for _, c := range []Check{s.checkDiscovery(), s.checkTLS(), s.checkRemote()} {
		if c.Status != CheckSkipped {
			t.Errorf("%+v", c)
		}
	}
	s.Config.Server.HTTPSEnabled, s.Config.Remote.Enabled = true, true
	for _, c := range []Check{s.checkTLS(), s.checkRemote()} {
		if c.Status != CheckWarn || !strings.Contains(c.Message, "todavía no") {
			t.Errorf("activado pero sin implementar: %+v", c)
		}
	}
}

func TestComprobacionDelPuerto(t *testing.T) {
	ln := listen(t)
	defer ln.Close()
	cfg := *config.Default()
	cfg.Server.BindAddress, cfg.Server.HTTPPort = "127.0.0.1", ln.port
	if c := (&Service{Config: cfg}).checkPort(context.Background()); c.Status != CheckOK {
		t.Errorf("%+v", c)
	}
	cfg.Server.BindAddress = "0.0.0.0"
	if c := (&Service{Config: cfg}).checkPort(context.Background()); c.Status != CheckOK || !strings.Contains(c.Message, "todas las interfaces") {
		t.Errorf("%+v", c)
	}
	ln.Close()
	if c := (&Service{Config: cfg}).checkPort(context.Background()); c.Status != CheckFail {
		t.Errorf("nadie escucha: %+v", c)
	}
}

func TestLocalAddressesSoloIPv4NoLoopback(t *testing.T) {
	old := interfaceAddrs
	t.Cleanup(func() { interfaceAddrs = old })
	parse := func(cidr string) net.Addr {
		ip, n, err := net.ParseCIDR(cidr)
		if err != nil {
			t.Fatal(err)
		}
		n.IP = ip
		return n
	}
	interfaceAddrs = func() ([]net.Addr, error) {
		return []net.Addr{
			parse("192.168.1.22/24"), parse("10.0.0.5/8"), parse("127.0.0.1/8"), parse("169.254.3.4/16"),
			parse("2001:db8::1/64"), parse("::1/128"), parse("fe80::1/64"), parse("8.8.4.4/32"),
		}, nil
	}
	got := localAddresses("0.0.0.0")
	want := []string{"10.0.0.5", "192.168.1.22", "8.8.4.4"}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("direcciones = %v, se esperaba %v (sin IPv6, bucle local ni enlace local)", got, want)
	}
	interfaceAddrs = func() ([]net.Addr, error) { return nil, errors.New("sin red") }
	if got := localAddresses(""); len(got) != 0 {
		t.Fatalf("sin red: %v", got)
	}
}

// El nivel mínimo no distingue mayúsculas.
func TestLogsNivelSinDistinguirMayusculas(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir,
		`{"level":"info","time":"2026-01-01T00:00:01Z","message":"uno"}`,
		`{"level":"error","time":"2026-01-01T00:00:02Z","message":"dos"}`,
	)
	s := &Service{Config: config.Config{Paths: config.Paths{Logs: dir}}}
	got, err := s.Logs(10, "ERROR", "")
	if err != nil || len(got) != 1 || got[0].Message != "dos" {
		t.Fatalf("%+v %v", got, err)
	}
}
