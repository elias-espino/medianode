package dlna

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// maxSOAPBody acota la petición de control: un Browse real pesa unos cientos de
// bytes.
const maxSOAPBody = 64 << 10

// Códigos de error de UPnP (UDA 1.0 §3.2.2 y ContentDirectory:1 §2.7.2).
const (
	errInvalidAction = 401
	errInvalidArgs   = 402
	errActionFailed  = 501
	errNoSuchObject  = 701
)

// upnpError es un fallo de una acción, con su código de UPnP.
type upnpError struct {
	code int
	desc string
}

func (e *upnpError) Error() string { return fmt.Sprintf("UPnP %d: %s", e.code, e.desc) }

// call es una acción SOAP recibida: su nombre, el servicio y los argumentos.
type call struct {
	service string
	action  string
	args    map[string]string
}

// parseSOAP lee una petición de control. Solo interpreta el primer elemento
// del cuerpo y sus hijos directos: es todo lo que usan las acciones de
// ContentDirectory y ConnectionManager, y evita construir un árbol con lo que
// envíe un cliente de la red.
func parseSOAP(body io.Reader) (*call, error) {
	dec := xml.NewDecoder(io.LimitReader(body, maxSOAPBody))
	dec.Strict = true
	inBody := false
	var c *call
	var arg string
	var text strings.Builder
	depth := 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case !inBody && t.Name.Local == "Body":
				inBody = true
			case inBody && c == nil:
				c = &call{service: t.Name.Space, action: t.Name.Local, args: map[string]string{}}
				depth = 1
			case c != nil:
				depth++
				if depth == 2 {
					arg = t.Name.Local
					text.Reset()
				}
			}
		case xml.CharData:
			if c != nil && depth == 2 {
				text.Write(t)
			}
		case xml.EndElement:
			if c == nil {
				continue
			}
			if depth == 2 && arg != "" {
				c.args[arg] = text.String()
				arg = ""
			}
			depth--
			if depth == 0 {
				return c, nil
			}
		}
	}
	if c == nil {
		return nil, errors.New("no hay ninguna acción en el cuerpo SOAP")
	}
	return c, nil
}

type pair struct{ name, value string }

// soapResponse construye la respuesta de una acción.
func soapResponse(service, action string, out []pair) []byte {
	var b strings.Builder
	b.WriteString(xmlHeader)
	b.WriteString(`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body>`)
	b.WriteString(`<u:` + action + `Response xmlns:u="` + service + `">`)
	for _, p := range out {
		b.WriteString(`<` + p.name + `>` + esc(p.value) + `</` + p.name + `>`)
	}
	b.WriteString(`</u:` + action + `Response></s:Body></s:Envelope>`)
	return []byte(b.String())
}

// soapFault construye el error de una acción (UDA 1.0 §3.2.2).
func soapFault(e *upnpError) []byte {
	var b strings.Builder
	b.WriteString(xmlHeader)
	b.WriteString(`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><s:Fault>`)
	b.WriteString(`<faultcode>s:Client</faultcode><faultstring>UPnPError</faultstring><detail>`)
	fmt.Fprintf(&b, `<UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>%d</errorCode><errorDescription>%s</errorDescription></UPnPError>`, e.code, esc(e.desc))
	b.WriteString(`</detail></s:Fault></s:Body></s:Envelope>`)
	return []byte(b.String())
}

func writeXML(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	w.Header().Set("EXT", "")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// soapActionName extrae el nombre de la acción de la cabecera SOAPACTION, que
// va entre comillas: "urn:...:service:ContentDirectory:1#Browse".
func soapActionName(h string) (service, action string) {
	h = strings.Trim(strings.TrimSpace(h), `"`)
	service, action, _ = strings.Cut(h, "#")
	return service, action
}
