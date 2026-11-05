package dlna

import (
	"fmt"
	"strconv"
	"strings"
)

// Clases UPnP de los objetos que se publican.
const (
	classRoot      = "object.container.storageFolder"
	classMovie     = "object.item.videoItem.movie"
	classEpisode   = "object.item.videoItem"
	classContainer = "object.container.storageFolder"
)

// Banderas de DLNA.ORG_FLAGS: modo de transferencia en flujo, de fondo y
// estado de conexión parado, DLNA 1.5. Es el valor que anuncian casi todos los
// servidores.
const dlnaFlags = "01700000000000000000000000000000"

// resource es un <res>: una URL con sus atributos.
type resource struct {
	fileID       string // Solo en las entregas directas: de ahí salen los subtítulos externos.
	url          string
	protocolInfo string
	size         int64 // 0: desconocido.
	durationMS   int64
	width        int64
	height       int64
	bitrate      int64 // bytes por segundo, como pide DIDL-Lite.
	channels     int64
}

// object es un contenedor o un elemento del árbol que se publica.
type object struct {
	id, parent, title string
	container         bool
	childCount        int
	class             string
	date              string
	desc              string
	art               string
	caption           string // URL del subtítulo externo (sec:CaptionInfoEx).
	res               []resource
}

// protocolInfo compone el cuarto campo con las operaciones y las banderas.
//
// op es DLNA.ORG_OP: «a» admite saltar por tiempo y «b» por bytes. No se
// anuncia ningún DLNA.ORG_PN: un identificador de perfil equivocado hace que un
// televisor estricto rechace el elemento, y sin él casi todos lo aceptan.
func protocolInfo(mime, op string, converted bool) string {
	ci := "0"
	if converted {
		ci = "1"
	}
	return fmt.Sprintf("http-get:*:%s:DLNA.ORG_OP=%s;DLNA.ORG_CI=%s;DLNA.ORG_FLAGS=%s", mime, op, ci, dlnaFlags)
}

// formatDuration da H:MM:SS.mmm, el formato de res@duration.
func formatDuration(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	return fmt.Sprintf("%d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

// didl serializa objetos como DIDL-Lite.
func didl(objs []object) string {
	var b strings.Builder
	b.WriteString(`<DIDL-Lite xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/" ` +
		`xmlns:dc="http://purl.org/dc/elements/1.1/" ` +
		`xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/" ` +
		`xmlns:dlna="urn:schemas-dlna-org:metadata-1-0/" ` +
		`xmlns:sec="http://www.sec.co.kr/">`)
	for _, o := range objs {
		tag := "item"
		if o.container {
			tag = "container"
		}
		fmt.Fprintf(&b, `<%s id="%s" parentID="%s" restricted="1"`, tag, esc(o.id), esc(o.parent))
		if o.container {
			fmt.Fprintf(&b, ` childCount="%d"`, o.childCount)
		}
		b.WriteString(`>`)
		b.WriteString(`<dc:title>` + esc(o.title) + `</dc:title>`)
		b.WriteString(`<upnp:class>` + o.class + `</upnp:class>`)
		if o.date != "" {
			b.WriteString(`<dc:date>` + esc(o.date) + `</dc:date>`)
		}
		if o.desc != "" {
			b.WriteString(`<dc:description>` + esc(o.desc) + `</dc:description>`)
		}
		for _, r := range o.res {
			b.WriteString(`<res`)
			if r.size > 0 {
				b.WriteString(` size="` + strconv.FormatInt(r.size, 10) + `"`)
			}
			if r.durationMS > 0 {
				b.WriteString(` duration="` + formatDuration(r.durationMS) + `"`)
			}
			if r.width > 0 && r.height > 0 {
				fmt.Fprintf(&b, ` resolution="%dx%d"`, r.width, r.height)
			}
			if r.bitrate > 0 {
				b.WriteString(` bitrate="` + strconv.FormatInt(r.bitrate, 10) + `"`)
			}
			if r.channels > 0 {
				b.WriteString(` nrAudioChannels="` + strconv.FormatInt(r.channels, 10) + `"`)
			}
			b.WriteString(` protocolInfo="` + esc(r.protocolInfo) + `">` + esc(r.url) + `</res>`)
		}
		if o.caption != "" {
			b.WriteString(`<sec:CaptionInfoEx sec:type="srt">` + esc(o.caption) + `</sec:CaptionInfoEx>`)
		}
		if o.art != "" {
			b.WriteString(`<upnp:albumArtURI>` + esc(o.art) + `</upnp:albumArtURI>`)
		}
		b.WriteString(`</` + tag + `>`)
	}
	b.WriteString(`</DIDL-Lite>`)
	return b.String()
}

// fileSlug es el nombre que acompaña a la URL de un medio. Hay renderers que
// deciden el formato por la extensión de la URL: solo caracteres seguros.
func fileSlug(title, ext string) string {
	var b strings.Builder
	for _, r := range title {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ' || r == '.':
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "_") {
				b.WriteByte('_')
			}
		}
		if b.Len() >= 60 {
			break
		}
	}
	name := strings.Trim(b.String(), "_")
	if name == "" {
		name = "video"
	}
	return name + "." + ext
}
