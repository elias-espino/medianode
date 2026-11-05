// Command netcheck comprueba la Fase 13 contra un servidor en marcha: DLNA con
// un punto de control de verdad (SSDP por multidifusión, descripción, Browse,
// entrega con rangos y MPEG-TS) y el acceso remoto (clasificación local o
// remota, administración cerrada, cabeceras de reenvío falsas, fuerza bruta,
// CORS, HSTS y TLS).
//
// El servidor debe arrancarse con:
//
//	discovery.dlna_enabled: true
//	remote: enabled, direct_enabled y public_url
//	server.trusted_proxies: [127.0.0.1]   (esta herramienta hace de reverse proxy)
//	server.https_enabled con el certificado de -gen-cert (opcional)
//
// Uso:
//
//	netcheck -gen-cert carpeta                      escribe cert.pem y key.pem de localhost
//	netcheck -url http://127.0.0.1:8420 -setup-code XXXX -media-dir D -fixtures testdata/fixtures
package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type obj = map[string]any

type checker struct {
	base    string
	client  *http.Client
	failed  int
	skipped int
	passed  int
}

func (c *checker) check(id, name string, ok bool, detail ...any) {
	mark := "OK  "
	if !ok {
		mark = "FALLA"
		c.failed++
	} else {
		c.passed++
	}
	d := ""
	if len(detail) > 0 {
		d = " — " + fmt.Sprint(detail...)
	}
	fmt.Printf("%-5s %-4s %s%s\n", mark, id, name, d)
}

func (c *checker) skip(id, name, why string) {
	c.skipped++
	fmt.Printf("OMITE %-4s %s — %s\n", id, name, why)
}

func main() {
	genCert := flag.String("gen-cert", "", "escribe cert.pem y key.pem de localhost en esta carpeta y termina")
	base := flag.String("url", "http://127.0.0.1:8420", "dirección HTTP del servidor")
	tlsURL := flag.String("tls-url", "", "dirección HTTPS del servidor, si lo hay")
	tlsCA := flag.String("tls-ca", "", "certificado en el que se confía para -tls-url")
	code := flag.String("setup-code", "", "código inicial del primer arranque")
	pass := flag.String("password", "Prueba-red-1234", "contraseña de la cuenta propietaria")
	mediaDir := flag.String("media-dir", "", "carpeta de la biblioteca, vista desde este equipo")
	mediaPath := flag.String("media-path", "", "la misma carpeta, vista desde el servidor (por defecto, igual)")
	fixtures := flag.String("fixtures", filepath.Join("testdata", "fixtures"), "vídeos de prueba")
	publicURL := flag.String("public-url", "https://media.example.com", "remote.public_url del servidor")
	noMulticast := flag.Bool("no-multicast", false, "omite la búsqueda SSDP por multidifusión")
	flag.Parse()

	if *genCert != "" {
		if err := writeCert(*genCert); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	c := &checker{base: strings.TrimRight(*base, "/"), client: &http.Client{Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if *mediaPath == "" {
		*mediaPath = *mediaDir
	}
	if err := c.run(*code, *pass, *mediaDir, *mediaPath, *fixtures, *tlsURL, *tlsCA, *publicURL, *noMulticast); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Printf("\n%d correctas, %d fallos, %d omitidas\n", c.passed, c.failed, c.skipped)
	if c.failed > 0 {
		os.Exit(1)
	}
}

func writeCert(dir string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "localhost"},
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(7 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "cert.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "key.pem"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
}

// do hace una petición. via simula un reverse proxy: añade X-Forwarded-For (y
// X-Forwarded-Proto si https), porque el servidor confía en 127.0.0.1.
func (c *checker) do(method, path string, body any, token string, headers map[string]string) (int, http.Header, []byte) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		return 0, nil, nil
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, nil, []byte(err.Error())
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, resp.Header, data
}

func via(ip string, extra ...string) map[string]string {
	h := map[string]string{"X-Forwarded-For": ip}
	for i := 0; i+1 < len(extra); i += 2 {
		h[extra[i]] = extra[i+1]
	}
	return h
}

func errCode(b []byte) string {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(b, &e)
	return e.Error.Code
}

func asObj(b []byte) obj {
	var o obj
	_ = json.Unmarshal(b, &o)
	return o
}

const (
	lanIP    = "192.168.1.77"
	publicIP = "203.0.113.9"
)

func (c *checker) run(code, pass, mediaDir, mediaPath, fixtures, tlsURL, tlsCA, publicURL string, noMulticast bool) error {
	// --- Cuenta propietaria y biblioteca ---
	if code != "" {
		st, _, b := c.do("POST", "/api/v1/auth/setup", obj{"code": code, "username": "propietario", "password": pass}, "", via(lanIP))
		if st != http.StatusCreated {
			return fmt.Errorf("crear el propietario: %d %s", st, b)
		}
	}
	st, _, b := c.do("POST", "/api/v1/auth/login", obj{"username": "propietario", "password": pass, "device_name": "netcheck", "platform": "android"}, "", via(lanIP))
	if st != http.StatusOK {
		return fmt.Errorf("entrar: %d %s", st, b)
	}
	token, _ := asObj(b)["access_token"].(string)

	// --- Remote Direct: clasificación y administración ---
	st, _, b = c.do("GET", "/api/v1/server/info", nil, "", via(publicIP))
	c.check("R1", "lo de fuera (por el proxy) entra, porque remote.direct_enabled lo permite", st == 200, st)
	info := asObj(b)
	c.check("R2", "server/info anuncia que desde fuera no hay administración", info["admin_available"] == false, info["admin_available"])
	_, _, b = c.do("GET", "/api/v1/server/info", nil, "", via(lanIP))
	c.check("R3", "desde la red local sí hay administración", asObj(b)["admin_available"] == true)

	remoteToken := ""
	st, _, b = c.do("POST", "/api/v1/auth/login", obj{"username": "propietario", "password": pass, "device_name": "fuera", "platform": "android"}, "", via(publicIP))
	if st == http.StatusOK {
		remoteToken, _ = asObj(b)["access_token"].(string)
	}
	c.check("R4", "el propietario puede entrar desde fuera", remoteToken != "", st)

	blocked := 0
	paths := []string{"/admin/system", "/admin/metrics", "/admin/sessions", "/admin/network", "/admin/storage", "/admin/logs", "/admin/settings",
		"/admin/system-check", "/admin/transcodes", "/admin/users", "/admin/remote"}
	for _, p := range paths {
		st, _, b = c.do("GET", "/api/v1"+p, nil, remoteToken, via(publicIP))
		if st == 403 && errCode(b) == "REMOTE_ADMIN_DISABLED" {
			blocked++
		}
	}
	c.check("R5", "ninguna ruta de administración responde desde fuera, ni con el token del propietario", blocked == len(paths), fmt.Sprintf("%d de %d", blocked, len(paths)))
	st, _, b = c.do("POST", "/api/v1/libraries", obj{"name": "x", "type": "movies", "paths": []string{"/"}}, remoteToken, via(publicIP))
	c.check("R6", "crear una biblioteca (publicar carpetas) tampoco desde fuera", st == 403 && errCode(b) == "REMOTE_ADMIN_DISABLED", st)
	st, _, _ = c.do("GET", "/api/v1/admin/system", nil, token, via(lanIP))
	c.check("R7", "los mismos endpoints funcionan desde la red local", st == 200, st)
	st, _, b = c.do("GET", "/api/v1/admin/system", nil, "", via(publicIP))
	c.check("R8", "sin sesión es un 401: no se confirma la ruta", st == 401, st, errCode(b))

	st, _, _ = c.do("GET", "/api/v1/admin/system", nil, remoteToken, via(lanIP+", "+publicIP))
	c.check("R9", "añadir una IP local a la izquierda de X-Forwarded-For no engaña al servidor", st == 403, st)
	st, _, _ = c.do("GET", "/api/v1/admin/system", nil, remoteToken, nil)
	c.check("R10", "un proxy de confianza que no dice de dónde viene el cliente: se trata como remoto", st == 403, st)
	st, _, _ = c.do("GET", "/api/v1/admin/system", nil, remoteToken, via("garbage"))
	c.check("R11", "X-Forwarded-For ilegible: remoto", st == 403, st)

	_, h, _ := c.do("GET", "/api/v1/server/info", nil, "", via(publicIP, "X-Forwarded-Proto", "https"))
	c.check("R12", "HSTS en una respuesta remota por HTTPS", h.Get("Strict-Transport-Security") != "", h.Get("Strict-Transport-Security"))
	_, h, _ = c.do("GET", "/api/v1/server/info", nil, "", via(publicIP))
	c.check("R13", "sin HSTS por una conexión sin cifrar", h.Get("Strict-Transport-Security") == "")
	_, h, _ = c.do("GET", "/api/v1/server/info", nil, "", via(lanIP, "X-Forwarded-Proto", "https"))
	c.check("R14", "sin HSTS en la red local", h.Get("Strict-Transport-Security") == "")

	// CORS.
	leak := false
	for _, origin := range []string{"https://evil.example", "null", "*", "http://127.0.0.1:8420.evil.example"} {
		_, h, _ := c.do("GET", "/api/v1/server/info", nil, "", via(publicIP, "Origin", origin))
		if h.Get("Access-Control-Allow-Origin") != "" || h.Get("Access-Control-Allow-Credentials") != "" {
			leak = true
		}
		st, h, _ := c.do("OPTIONS", "/api/v1/auth/login", nil, "", via(publicIP, "Origin", origin, "Access-Control-Request-Method", "POST"))
		if h.Get("Access-Control-Allow-Origin") != "" || st == http.StatusNoContent {
			leak = true
		}
	}
	c.check("R15", "CORS: ningún origen ajeno recibe permiso, ni preflight", !leak)

	// Fuerza bruta contra el login remoto: por IP y por cuenta.
	limited := false
	for i := 0; i < 40 && !limited; i++ {
		st, _, _ := c.do("POST", "/api/v1/auth/login", obj{"username": fmt.Sprintf("intruso%d", i), "password": "mala", "device_name": "x", "platform": "android"}, "", via("198.51.100.200"))
		limited = st == http.StatusTooManyRequests
	}
	c.check("R16", "fuerza bruta desde una IP: el límite por IP corta", limited)
	limited = false
	for i := 0; i < 30 && !limited; i++ {
		st, _, _ := c.do("POST", "/api/v1/auth/login", obj{"username": "propietario", "password": "mala", "device_name": "x", "platform": "android"}, "", via(fmt.Sprintf("198.51.100.%d", i+1)))
		limited = st == http.StatusTooManyRequests
	}
	c.check("R17", "fuerza bruta desde muchas IP contra una cuenta: el límite por cuenta corta", limited)
	// Para no dejar la cuenta bloqueada el resto de la prueba.
	time.Sleep(61 * time.Second)

	// Prueba de alcance desde fuera.
	st, _, b = c.do("POST", "/api/v1/admin/remote/probe", nil, token, via(lanIP))
	probe := asObj(b)
	purl, _ := probe["url"].(string)
	c.check("R18", "se genera la URL de la prueba de alcance", st == 200 && strings.HasPrefix(purl, publicURL+"/api/v1/remote/probe/"), purl)
	if purl != "" {
		path := strings.TrimPrefix(purl, publicURL)
		st, _, b = c.do("GET", path, nil, "", via(lanIP))
		c.check("R19", "abrirla desde la red local no prueba nada y no la gasta", st == 200 && strings.Contains(string(b), "red local"), st)
		st, _, b = c.do("GET", path, nil, "", via(publicIP))
		c.check("R20", "abrirla desde fuera la da por buena", st == 200 && strings.Contains(string(b), "accesible desde fuera"), st)
		st, _, _ = c.do("GET", path, nil, "", via(publicIP))
		c.check("R21", "es de un solo uso", st == 404, st)
		_, _, b = c.do("GET", "/api/v1/admin/remote", nil, token, via(lanIP))
		rep := asObj(b)
		pr, _ := rep["probe"].(map[string]any)
		c.check("R22", "el informe refleja que es alcanzable desde fuera", pr["state"] == "reachable" && pr["from"] == publicIP, pr)
	}

	// --- TLS ---
	if tlsURL != "" {
		c.checkTLS(tlsURL, tlsCA)
	} else {
		c.skip("T1", "TLS", "sin -tls-url")
	}

	// --- DLNA ---
	if mediaDir == "" {
		c.skip("D1", "DLNA", "sin -media-dir")
		return nil
	}
	return c.dlna(token, mediaDir, mediaPath, fixtures, noMulticast)
}

func (c *checker) checkTLS(tlsURL, caFile string) {
	pool := x509.NewCertPool()
	if caFile != "" {
		if pem, err := os.ReadFile(caFile); err == nil {
			pool.AppendCertsFromPEM(pem)
		}
	}
	u, err := url.Parse(tlsURL)
	if err != nil {
		c.check("T1", "TLS", false, err)
		return
	}
	cl := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "localhost"}, ForceAttemptHTTP2: true}}
	resp, err := cl.Get(tlsURL + "/health")
	ok := err == nil && resp.StatusCode == 200
	proto := ""
	if err == nil {
		proto = resp.Proto
		resp.Body.Close()
	}
	c.check("T1", "HTTPS con el certificado del usuario, verificado", ok, proto, err)
	c.check("T2", "negocia HTTP/2", proto == "HTTP/2.0", proto)
	conn, err := tls.Dial("tcp", u.Host, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11})
	if err == nil {
		_ = conn.Close()
	}
	c.check("T3", "rechaza TLS 1.0 y 1.1", err != nil)
	conn, err = tls.Dial("tcp", u.Host, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12})
	v := uint16(0)
	if err == nil {
		v = conn.ConnectionState().Version
		_ = conn.Close()
	}
	c.check("T4", "acepta TLS 1.2 o superior", err == nil && v >= tls.VersionTLS12)
	bad := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "otro.example"}}}
	badResp, err := bad.Get(tlsURL + "/health")
	if err == nil {
		_ = badResp.Body.Close()
	}
	c.check("T5", "un nombre que el certificado no cubre no se acepta", err != nil)
}

// --- DLNA ---

var reLocation = regexp.MustCompile(`(?i)\r\nLOCATION:\s*(\S+)`)

func (c *checker) dlna(token, mediaDir, mediaPath, fixtures string, noMulticast bool) error {
	for _, f := range []struct{ src, dst string }{{"mp4_h264_aac.mp4", "Pelicula Directa.mp4"}, {"mkv_h264_aac.mkv", "Pelicula MKV.mkv"}} {
		data, err := os.ReadFile(filepath.Join(fixtures, f.src))
		if err != nil {
			return fmt.Errorf("falta el fixture %s: %w", f.src, err)
		}
		if err := os.WriteFile(filepath.Join(mediaDir, f.dst), data, 0o644); err != nil {
			return err
		}
	}
	// Un subtítulo externo junto a la película directa (el escáner lo asocia por el nombre).
	if data, err := os.ReadFile(filepath.Join(fixtures, "mp4_h264_aac.es.srt")); err == nil {
		if err := os.WriteFile(filepath.Join(mediaDir, "Pelicula Directa.es.srt"), data, 0o644); err != nil {
			return err
		}
	}
	st, _, b := c.do("POST", "/api/v1/libraries", obj{"name": "Cine DLNA", "type": "movies", "paths": []string{mediaPath}}, token, via(lanIP))
	if st != http.StatusCreated {
		return fmt.Errorf("crear la biblioteca: %d %s", st, b)
	}
	lib, _ := asObj(b)["id"].(string)
	st, _, b = c.do("PATCH", "/api/v1/libraries/"+lib, obj{"dlna_enabled": true}, token, via(lanIP))
	c.check("D1", "marcar la biblioteca para DLNA", st == 200 && asObj(b)["dlna_enabled"] == true, st)
	// Una biblioteca nueva no se escanea sola: se pide y se espera a que termine.
	st, _, b = c.do("POST", "/api/v1/libraries/"+lib+"/scan", nil, token, via(lanIP))
	if st != http.StatusAccepted && st != http.StatusOK && st != http.StatusCreated {
		return fmt.Errorf("escanear: %d %s", st, b)
	}
	deadline := time.Now().Add(120 * time.Second)
	scanned := false
	for time.Now().Before(deadline) {
		_, _, b = c.do("GET", "/api/v1/libraries/"+lib+"/scan", nil, token, via(lanIP))
		if s := asObj(b)["status"]; s == "completed" || s == "failed" {
			scanned = s == "completed"
			break
		}
		time.Sleep(time.Second)
	}
	c.check("D1b", "el escaneo de la biblioteca termina", scanned)
	// El análisis de los archivos puede ir un poco por detrás del escaneo.
	for time.Now().Before(deadline) {
		_, _, b = c.do("GET", "/api/v1/libraries/"+lib+"/items", nil, token, via(lanIP))
		if items, _ := asObj(b)["items"].([]any); len(items) >= 2 {
			break
		}
		time.Sleep(time.Second)
	}

	// Descubrimiento por SSDP, con un socket UDP de verdad.
	location, lanFallback := "", ""
	if noMulticast {
		c.skip("D2", "SSDP por multidifusión", "-no-multicast")
	} else {
		resps, tried := ssdpSearch(3 * time.Second)
		lanFallback = firstOf(tried)
		for _, r := range resps {
			if m := reLocation.FindStringSubmatch(r); m != nil && strings.Contains(m[1], ":"+portOf(c.base)+"/dlna/device.xml") {
				location = m[1]
			}
		}
		if len(resps) == 0 {
			c.skip("D2", "SSDP por multidifusión", "ninguna respuesta; se buscó desde "+strings.Join(tried, ", "))
		} else {
			c.check("D2", "M-SEARCH por multidifusión descubre el servidor", location != "", fmt.Sprintf("%d respuestas; %s", len(resps), location))
		}
	}
	if location == "" {
		// Sin SSDP, por la dirección de la red local: por el bucle local, el servidor
		// (que aquí confía en 127.0.0.1 como proxy) lo trataría como remoto.
		location = c.base + "/dlna/device.xml"
		if lanFallback != "" {
			location = "http://" + net.JoinHostPort(lanFallback, portOf(c.base)) + "/dlna/device.xml"
		}
	}
	// El resto va por la dirección que anunció el servidor.
	lu, err := url.Parse(location)
	if err != nil {
		return err
	}
	root := lu.Scheme + "://" + lu.Host

	get := func(path string, headers map[string]string) (int, http.Header, []byte) {
		req, _ := http.NewRequest("GET", root+path, nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := c.client.Do(req)
		if err != nil {
			return 0, nil, nil
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		return resp.StatusCode, resp.Header, data
	}
	st, h, b := get("/dlna/device.xml", nil)
	c.check("D3", "descripción del dispositivo (MediaServer:1)", st == 200 && strings.Contains(string(b), "urn:schemas-upnp-org:device:MediaServer:1") &&
		strings.HasPrefix(h.Get("Content-Type"), "text/xml"), st)
	for _, p := range []string{"/dlna/cds.xml", "/dlna/cms.xml"} {
		st, _, b = get(p, nil)
		c.check("D4", "descripción del servicio "+p, st == 200 && strings.Contains(string(b), "<scpd"), st)
	}

	soap := func(path, service, action, args string) (int, string) {
		body := `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><u:` +
			action + ` xmlns:u="` + service + `">` + args + `</u:` + action + `></s:Body></s:Envelope>`
		req, _ := http.NewRequest("POST", root+path, strings.NewReader(body))
		req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
		req.Header.Set("SOAPACTION", `"`+service+`#`+action+`"`)
		resp, err := c.client.Do(req)
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		return resp.StatusCode, string(data)
	}
	const cds = "urn:schemas-upnp-org:service:ContentDirectory:1"
	browse := func(id, flag string) (int, string) {
		return soap("/dlna/cds/control", cds, "Browse", "<ObjectID>"+id+"</ObjectID><BrowseFlag>"+flag+"</BrowseFlag><Filter>*</Filter><StartingIndex>0</StartingIndex><RequestedCount>0</RequestedCount><SortCriteria></SortCriteria>")
	}
	st, body := browse("0", "BrowseDirectChildren")
	c.check("D5", "Browse de la raíz lista la biblioteca publicada", st == 200 && strings.Contains(body, "Cine DLNA"), st)
	st, body = browse("lib:"+lib, "BrowseDirectChildren")
	unesc := strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&amp;", "&", "&#34;", `"`).Replace(body)
	reRes := regexp.MustCompile(`<res[^>]*protocolInfo="([^"]*)">([^<]*)</res>`)
	var ress [][]string
	for _, r := range reRes.FindAllStringSubmatch(unesc, -1) {
		if !strings.Contains(r[1], "text/srt") { // El subtítulo externo es otro <res>, no una película.
			ress = append(ress, r)
		}
	}
	c.check("D6", "Browse de la biblioteca da dos películas con su <res>", st == 200 && len(ress) == 2, len(ress))
	var direct, ts string
	for _, r := range ress {
		switch {
		case strings.Contains(r[2], "/dlna/media/"):
			direct = r[2]
		case strings.Contains(r[2], "/dlna/ts/"):
			ts = r[2]
		}
	}
	c.check("D7", "el MP4 compatible va directo y el MKV por MPEG-TS", direct != "" && ts != "", fmt.Sprintf("directo=%v ts=%v", direct != "", ts != ""))
	c.check("D8", "protocolInfo con DLNA.ORG_OP y banderas, sin perfil inventado", len(ress) > 0 &&
		strings.Contains(ress[0][1], "DLNA.ORG_OP=") && strings.Contains(ress[0][1], "DLNA.ORG_FLAGS=") && !strings.Contains(unesc, "DLNA.ORG_PN"))

	if direct != "" {
		du, _ := url.Parse(direct)
		st, h, b = get(du.Path, map[string]string{"Range": "bytes=0-99"})
		c.check("D9", "entrega directa con rangos (206)", st == 206 && len(b) == 100 && h.Get("Content-Range") != "", st, len(b))
		c.check("D10", "cabeceras DLNA: transferMode y contentFeatures", h.Get("transferMode.dlna.org") == "Streaming" && h.Get("contentFeatures.dlna.org") != "")
	}
	if ts != "" {
		tu, _ := url.Parse(ts)
		req, _ := http.NewRequest("GET", root+tu.Path, nil)
		resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
		okTS := false
		if err == nil {
			buf := make([]byte, 188*50)
			if _, err := io.ReadFull(resp.Body, buf); err == nil {
				okTS = resp.StatusCode == 200 && resp.Header.Get("Content-Type") == "video/mpeg"
				for i := 0; i < len(buf); i += 188 {
					okTS = okTS && buf[i] == 0x47
				}
			}
			resp.Body.Close()
		}
		c.check("D11", "flujo MPEG-TS sincronizado (50 paquetes)", okTS, err)
		req, _ = http.NewRequest("GET", root+tu.Path, nil)
		req.Header.Set("TimeSeekRange.dlna.org", "npt=1-")
		resp, err = (&http.Client{Timeout: 60 * time.Second}).Do(req)
		okSeek := err == nil && resp.StatusCode == 200 && strings.HasPrefix(resp.Header.Get("TimeSeekRange.dlna.org"), "npt=1.000-")
		if resp != nil {
			resp.Body.Close()
		}
		c.check("D12", "salto por tiempo (TimeSeekRange.dlna.org)", okSeek, err)
	}

	// Las sesiones DLNA no tienen usuario y las ve el administrador.
	_, _, b = c.do("GET", "/api/v1/admin/sessions", nil, token, via(lanIP))
	var sessions []obj
	_ = json.Unmarshal(b, &sessions)
	dl := 0
	for _, s := range sessions {
		if s["client_kind"] == "dlna" && s["user"] == "" {
			dl++
		}
	}
	c.check("D13", "las sesiones DLNA figuran en el panel, sin usuario", dl >= 1, dl)

	c.searchAndSubtitles(root, browse, soap, get, lib, unesc)
	events := c.subscribe(root, lu.Hostname())
	defer events.close()

	// Fuera de la red local, DLNA no existe.
	for _, p := range []string{"/dlna/device.xml", "/dlna/cds.xml"} {
		st, _, _ := c.do("GET", p, nil, "", via(publicIP))
		c.check("D14", "desde fuera, "+p+" no existe (404)", st == 404, st)
	}
	if direct != "" {
		du, _ := url.Parse(direct)
		st, _, _ := c.do("GET", du.Path, nil, "", via(publicIP))
		c.check("D15", "desde fuera, el medio tampoco se entrega", st == 404, st)
	}
	// Sin la marca, la biblioteca desaparece al momento.
	c.do("PATCH", "/api/v1/libraries/"+lib, obj{"dlna_enabled": false}, token, via(lanIP))
	_, body = browse("0", "BrowseDirectChildren")
	c.check("D16", "quitar la marca oculta la biblioteca de inmediato", !strings.Contains(body, "Cine DLNA"))
	if direct != "" {
		du, _ := url.Parse(direct)
		st, _, _ := get(du.Path, nil)
		c.check("D17", "y sus medios dejan de entregarse", st == 404, st)
	}
	events.expectChange(c)
	return nil
}

func portOf(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return "8420"
	}
	return u.Port()
}

// ssdpSearch hace un M-SEARCH por multidifusión por cada interfaz IPv4 con
// multidifusión y devuelve las respuestas recibidas.
func ssdpSearch(wait time.Duration) (out []string, tried []string) {
	group := &net.UDPAddr{IP: net.ParseIP("239.255.255.250"), Port: 1900}
	msg := "M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: 1\r\nST: urn:schemas-upnp-org:device:MediaServer:1\r\n\r\n"
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagMulticast == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil || ipn.IP.IsLinkLocalUnicast() {
				continue
			}
			tried = append(tried, ipn.IP.String())
			conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ipn.IP})
			if err != nil {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), wait)
			_, _ = conn.WriteToUDP([]byte(msg), group)
			_ = conn.SetReadDeadline(time.Now().Add(wait))
			buf := make([]byte, 2048)
			for ctx.Err() == nil {
				n, _, err := conn.ReadFromUDP(buf)
				if err != nil {
					break
				}
				out = append(out, string(buf[:n]))
			}
			cancel()
			_ = conn.Close()
		}
	}
	return out, tried
}

// firstOf devuelve el primer elemento, o vacío.
func firstOf(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}
