package admin

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"medianode/internal/ffmpeg"
	"medianode/internal/remote"
)

// Estados de una comprobación.
const (
	CheckOK      = "ok"
	CheckWarn    = "warn"
	CheckFail    = "fail"
	CheckSkipped = "skipped"
)

// Check es el resultado de una comprobación del diagnóstico (§19.1).
type Check struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// minTempBytes es el espacio libre por debajo del cual el servidor no puede
// transcodificar de forma fiable.
const minTempBytes = 1 << 30

// SystemCheck ejecuta las comprobaciones de §19.1, en este orden.
func (s *Service) SystemCheck(ctx context.Context) []Check {
	return []Check{
		s.checkFFmpeg(),
		s.checkFFprobe(ctx),
		s.checkCodecs(),
		s.checkHardware(),
		s.checkToneMapping(),
		s.checkLibraries(ctx),
		s.checkTempSpace(),
		s.checkDatabase(ctx),
		s.checkPort(ctx),
		s.checkDiscovery(ctx),
		s.checkTLS(),
		s.checkRemote(),
	}
}

func check(id, name, status, format string, args ...any) Check {
	return Check{ID: id, Name: name, Status: status, Message: fmt.Sprintf(format, args...)}
}

func (s *Service) checkFFmpeg() Check {
	const id, name = "ffmpeg", "FFmpeg"
	if s.FFmpeg == nil {
		return check(id, name, CheckFail, "No está instalado o no funciona: solo hay Direct Play. Instálalo junto al servidor o en el PATH.")
	}
	return check(id, name, CheckOK, "Versión %s.", s.FFmpeg.Version)
}

func (s *Service) checkFFprobe(ctx context.Context) Check {
	const id, name = "ffprobe", "FFprobe"
	if s.FFprobePath == "" {
		return check(id, name, CheckFail, "No está instalado: los escaneos fallarán hasta instalarlo y reiniciar el servidor.")
	}
	if v := s.ffprobeVersion(ctx); v != "" {
		return check(id, name, CheckOK, "Versión %s.", v)
	}
	return check(id, name, CheckWarn, "Está en %s pero no se pudo leer su versión.", s.FFprobePath)
}

func (s *Service) checkCodecs() Check {
	const id, name = "codecs", "Códecs de vídeo"
	switch {
	case s.FFmpeg == nil:
		return check(id, name, CheckSkipped, "Sin FFmpeg.")
	case !s.FFmpeg.Encoders[ffmpeg.X264]:
		return check(id, name, CheckFail, "Esta build de FFmpeg no incluye libx264: no se puede transcodificar.")
	}
	return check(id, name, CheckOK, "libx264 disponible.")
}

func (s *Service) checkHardware() Check {
	const id, name = "hardware", "Aceleración por hardware"
	if s.FFmpeg == nil {
		return check(id, name, CheckSkipped, "Sin FFmpeg.")
	}
	working := map[ffmpeg.Encoder]bool{}
	for _, e := range s.FFmpeg.Working {
		working[e] = true
	}
	var compiled, broken []string
	for _, hw := range ffmpeg.Hardware {
		if !s.FFmpeg.Encoders[hw.Encoder] {
			continue
		}
		compiled = append(compiled, hw.Name)
		if !working[hw.Encoder] {
			broken = append(broken, hw.Name)
		}
	}
	switch {
	case len(compiled) == 0:
		return check(id, name, CheckOK, "Esta build no trae codificadores por hardware: se codifica en CPU.")
	case len(broken) == len(compiled):
		return check(id, name, CheckWarn, "FFmpeg trae %s, pero ninguno inicia en este equipo (¿falta la GPU o el controlador?): se codifica en CPU.", strings.Join(compiled, ", "))
	case len(broken) > 0:
		return check(id, name, CheckOK, "En uso: %s. No inician: %s.", s.encoderName(), strings.Join(broken, ", "))
	}
	return check(id, name, CheckOK, "En uso: %s.", s.encoderName())
}

func (s *Service) encoderName() string {
	if s.Transcoder != nil {
		return string(s.Transcoder.Encoder())
	}
	return string(ffmpeg.X264)
}

func (s *Service) checkToneMapping() Check {
	const id, name = "tone_mapping", "Conversión de HDR a SDR"
	switch c := s.FFmpeg; {
	case c == nil:
		return check(id, name, CheckSkipped, "Sin FFmpeg.")
	case !c.ToneMapping:
		return check(id, name, CheckWarn, "No disponible (FFmpeg sin zscale/tonemap, o desactivado con transcoding.tone_mapping): un HDR se rechaza en un cliente SDR.")
	case c.ToneMapSpeed > 0 && c.ToneMapSpeed < 1:
		return check(id, name, CheckWarn, "Funciona, pero este equipo lo hace a %.2f× el tiempo real: la imagen se cortaría. Con transcoding.tone_mapping: realtime se rechaza en su lugar.", c.ToneMapSpeed)
	case c.ToneMapSpeed > 0:
		return check(id, name, CheckOK, "Funciona a %.1f× el tiempo real (720p, por software).", c.ToneMapSpeed)
	}
	return check(id, name, CheckOK, "Funciona; no se pudo medir su velocidad.")
}

func (s *Service) checkLibraries(ctx context.Context) Check {
	const id, name = "libraries", "Permisos de las bibliotecas"
	rows, err := s.DB.Read.QueryContext(ctx, `SELECT l.name, lp.path FROM library_paths lp JOIN libraries l ON l.id = lp.library_id ORDER BY l.name`)
	if err != nil {
		return check(id, name, CheckFail, "No se pudieron leer las bibliotecas: %v", err)
	}
	defer rows.Close()
	var total int
	var bad, mapped []string
	for rows.Next() {
		var lib, path string
		if rows.Scan(&lib, &path) != nil {
			continue
		}
		total++
		if err := readable(path); err != nil {
			bad = append(bad, fmt.Sprintf("%s (%s): %s", lib, path, reason(err)))
		} else if networkDrive(path) {
			mapped = append(mapped, fmt.Sprintf("%s (%s)", lib, path))
		}
	}
	if err := rows.Err(); err != nil {
		return check(id, name, CheckFail, "No se pudieron leer las bibliotecas: %v", err)
	}
	switch {
	case total == 0:
		return check(id, name, CheckSkipped, "Todavía no hay carpetas de bibliotecas.")
	case len(bad) > 0:
		return check(id, name, CheckFail, "No se pueden leer: %s.", strings.Join(bad, "; "))
	case len(mapped) > 0:
		// §21.2: las letras de unidad de red son de cada sesión de usuario y un
		// servicio de Windows no las ve.
		return check(id, name, CheckWarn, "Están en una unidad de red con letra: %s. Si MediaNode corre como servicio de Windows no las verá; registra la biblioteca con su ruta UNC (\\\\servidor\\recurso).", strings.Join(mapped, "; "))
	}
	return check(id, name, CheckOK, "%d carpeta(s) legibles.", total)
}

// networkDrive es isMappedNetworkDrive; las pruebas lo sustituyen.
var networkDrive = isMappedNetworkDrive

// readable comprueba que el servidor puede entrar en el directorio y listarlo.
func readable(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("no es un directorio")
	}
	_, err = f.ReadDir(1)
	if err != nil && err.Error() != "EOF" {
		return err
	}
	return nil
}

func reason(err error) string {
	switch {
	case os.IsNotExist(err):
		return "no existe"
	case os.IsPermission(err):
		return "sin permiso de lectura"
	}
	return err.Error()
}

func (s *Service) checkTempSpace() Check {
	const id, name = "temp_space", "Espacio temporal"
	dir := s.Config.Paths.Transcodes
	free, _, err := diskSpace(dir)
	if err != nil {
		return check(id, name, CheckWarn, "No se pudo medir el espacio de %s.", dir)
	}
	want := uint64(s.Config.Transcoding.MaxDiskGB) << 30
	switch {
	case free < minTempBytes:
		return check(id, name, CheckFail, "Quedan %s libres en %s: no basta para transcodificar.", human(free), dir)
	case want > 0 && free < want:
		return check(id, name, CheckWarn, "Quedan %s libres en %s y transcoding.max_disk_gb permite %s.", human(free), dir, human(want))
	}
	return check(id, name, CheckOK, "%s libres en %s.", human(free), dir)
}

func (s *Service) checkDatabase(ctx context.Context) Check {
	const id, name = "database", "SQLite"
	var res string
	if err := s.DB.Read.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&res); err != nil {
		return check(id, name, CheckFail, "No se pudo comprobar la base de datos: %v", err)
	}
	if res != "ok" {
		return check(id, name, CheckFail, "La base de datos está dañada: %s.", res)
	}
	var mode string
	_ = s.DB.Read.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode)
	if !strings.EqualFold(mode, s.Config.Database.JournalMode) {
		return check(id, name, CheckWarn, "Íntegra, pero el modo de journal es %q y la configuración pide %q.", mode, s.Config.Database.JournalMode)
	}
	return check(id, name, CheckOK, "Íntegra, modo %s, %s.", strings.ToUpper(mode), human(uint64(s.dbSize())))
}

func (s *Service) checkPort(ctx context.Context) Check {
	const id, name = "port", "Puerto HTTP"
	c := s.Config.Server
	host := c.BindAddress
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	addr := net.JoinHostPort(host, strconv.Itoa(c.HTTPPort))
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return check(id, name, CheckFail, "El servidor no responde en %s: %v", addr, err)
	}
	_ = conn.Close()
	if c.BindAddress == "" || c.BindAddress == "0.0.0.0" || c.BindAddress == "::" {
		return check(id, name, CheckOK, "Escuchando en el puerto %d, en todas las interfaces.", c.HTTPPort)
	}
	return check(id, name, CheckOK, "Escuchando en %s.", addr)
}

func (s *Service) checkDiscovery(ctx context.Context) Check {
	const id, name = "discovery", "DLNA y descubrimiento"
	if !s.Config.Discovery.DLNAEnabled {
		return check(id, name, CheckSkipped, "DLNA está desactivado (discovery.dlna_enabled). El descubrimiento mDNS de MediaNode llega con los clientes móviles.")
	}
	libs, err := s.DB.R.ListDLNALibraries(ctx)
	switch {
	case err != nil:
		return check(id, name, CheckFail, "No se pudo leer las bibliotecas publicadas: %v", err)
	case len(libs) == 0:
		return check(id, name, CheckWarn, "DLNA está activo, pero ninguna biblioteca se publica (dlna_enabled): los televisores verán un servidor vacío.")
	}
	return check(id, name, CheckOK, "DLNA activo con %d biblioteca(s) publicada(s). Cualquier equipo de la red local las ve sin contraseña.", len(libs))
}

func (s *Service) checkTLS() Check {
	const id, name = "tls", "Certificado TLS"
	c := s.Config.Server
	if !c.HTTPSEnabled {
		return check(id, name, CheckSkipped, "HTTPS no está activado: el servidor solo escucha en HTTP, sin cifrar.")
	}
	host := ""
	if u, err := url.Parse(s.Config.Remote.PublicURL); err == nil {
		host = u.Hostname()
	}
	r := remote.CheckCertificate(c.TLSCertFile, host, time.Now())
	return check(id, name, r.Status, "%s", r.Detail)
}

func (s *Service) checkRemote() Check {
	const id, name = "remote", "Acceso remoto"
	c := s.Config.Remote
	switch {
	case !c.Enabled || !c.DirectEnabled:
		return check(id, name, CheckSkipped, "Desactivado: el servidor solo responde en la red local.")
	case c.AdminEnabled:
		return check(id, name, CheckWarn, "Activo, con la administración abierta a Internet (remote.admin_enabled). Desactívala salvo que la necesites.")
	case c.PublicURL == "":
		return check(id, name, CheckWarn, "Activo, pero falta remote.public_url: sin ella no se puede comprobar el alcance desde fuera.")
	}
	return check(id, name, CheckOK, "Activo en %s, con la administración bloqueada. La conectividad se comprueba en la pestaña Red.", c.PublicURL)
}

// human formatea bytes en la unidad binaria que corresponda.
func human(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}
