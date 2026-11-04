# 0009. DLNA y acceso remoto

**Estado:** aceptada; verificada con un punto de control propio (`scripts/netcheck`) contra el binario de Windows y contra Linux (Alpine); **sin televisores ni renderers reales**\
**Fecha:** 4 de noviembre de 2026\
**Fase:** 13

## Decisión

### Local o remoto: una sola función

`remote.Network.Classify` decide si una petición es local o remota (§14.4). Es la
única definición: el límite de intentos, las sesiones, el panel y DLNA la usan.

| Petición | Origen |
|----------|--------|
| De una dirección privada, de bucle o de enlace local, sin cabeceras de reenvío | local |
| De una dirección pública | remota |
| De una dirección local **con** `X-Forwarded-*`, `Forwarded` o `X-Real-IP`, si no es un proxy de `trusted_proxies` | **remota** |
| De un proxy de confianza, con `X-Forwarded-For` | la última dirección de la cabecera |
| De un proxy de confianza, sin cabecera o con una ilegible | **remota** |

Las dos filas en negrita son las que importan: un reverse proxy mal declarado, o
una cabecera falsa, no pueden hacer pasar tráfico de fuera por local, y la duda
se resuelve siempre hacia lo restrictivo. Una VPN (Tailscale, WireGuard) no es
«privada» para Go, y por eso hay `remote.local_networks`.

La IP **no es una identidad**: solo decide qué superficie se expone. Quien llega
de fuera sigue necesitando una cuenta, y el rol se comprueba igual.

### Puerta, administración y panel

- **Puerta** (`remote.Gate`, el primer middleware): con `remote.enabled` y
  `remote.direct_enabled` apagados, todo lo que no es local recibe
  `403 REMOTE_ACCESS_DISABLED`, también `/health` y el login. Es el valor por
  defecto: un servidor con el puerto reenviado por error no responde.
- **Administración** (`RequireLocalAdmin`): `/admin/*` y las operaciones que
  deciden qué carpetas del equipo se publican (crear, editar y borrar
  bibliotecas y sus rutas) responden `403 REMOTE_ADMIN_DISABLED` desde fuera,
  aunque el token sea del propietario, salvo `remote.admin_enabled`. Sin sesión,
  sigue siendo un `401`: no se confirma qué rutas existen.
- **Panel** (`GuardPanel`): `/admin`, `/users` y `/metadata` no se sirven a quien
  no puede usarlas, y `/server/info` informa de `admin_available` para que el panel
  oculte esas pantallas. La API ya estaba cerrada: esto evita descargar de más y
  mostrar pantallas que fallarían.
- **HSTS** solo en respuestas remotas por una conexión cifrada: en la red local
  fijaría `https` en un nombre que puede ser `http`.
- **Bitrate remoto**: `users.max_bitrate_remote` (que existía desde la Fase 3 y
  no se aplicaba) limita el bitrate cuando la petición es remota. Un límite más bajo
  pedido por el cliente manda. Se guarda en la petición de la sesión, así que un
  cambio de pista lo conserva.

### HTTPS

`server.https_enabled` abre un segundo puerto con el certificado y la clave **del
usuario** (`server.tls_cert_file`, `tls_key_file`): TLS 1.2 como mínimo, HTTP/2, y
el par se **recarga solo** si cambia en disco (comprobación cada minuto), de modo
que renovar con certbot no obliga a reiniciar. Un par a medio escribir no tumba
el servicio: sigue con el anterior y lo registra. Un par inválido falla al arrancar.

El certificado **generado por el servidor** para la LAN (§14.4, V5) no está: sigue
en la Fase 11, porque lo necesitan los clientes móviles para fijarlo al vincular.

`remote.direct_enabled` exige HTTPS propio **o** un reverse proxy declarado en
`trusted_proxies`; sin ninguno de los dos el servidor no arranca, porque las
contraseñas viajarían en claro. `remote.relay_enabled` tampoco arranca: el relay
solo se diseña si hay demanda (§14.3, §26.5).

- **Rechazos**: la puerta anota cada conexión remota rechazada en el registro, una vez
  por minuto y origen (el mapa de orígenes está acotado), con una pista si traía
  cabeceras de reenvío: quien puso un reverse proxy sin declararlo debe poder ver
  por qué recibe un 403. **Es un cambio de comportamiento**: antes de esta fase, un
  servidor detrás de un proxy sin declarar aceptaba a todos; ahora lo que trae
  `X-Forwarded-For` desde un origen que no es de `trusted_proxies` se trata como
  remoto y, sin `remote.direct_enabled`, se rechaza (§14.4).

### Comprobar la conectividad sin servicios de terceros

`GET /admin/remote` ejecuta comprobaciones (estado del acceso, administración,
dirección de escucha, certificado —vigencia, nombre y días que le quedan—, DNS de
`public_url` y una petición a `/health` de la dirección pública con verificación,
como la haría un cliente) y devuelve el estado de la prueba de alcance.

La prueba demuestra lo que las otras no pueden, que **se llega desde fuera**, sin
un servicio externo: `POST /admin/remote/probe` genera una URL de un solo uso
(256 bits, 10 minutos) y el administrador la abre desde otra red, con los datos
del móvil. El servidor anota si esa petición se clasificó como remota; si llegó
desde la red local responde que no prueba nada y no gasta el código. Un código
inventado, caducado o ya usado es un `404` igual.

La petición que el servidor se hace a sí mismo no es concluyente (el router puede
no reenviar la conexión propia), y el informe lo dice: un fallo ahí es un aviso, y
un certificado inválido o un proxy mal apuntado, un fallo.

### DLNA

Un subsistema separado del descubrimiento de MediaNode (§13.2), activado con
`discovery.dlna_enabled` (apagado por defecto) y por biblioteca con
`libraries.dlna_enabled`, que el panel pide confirmar con el aviso de que cualquier
equipo de la red local verá la biblioteca sin contraseña.

**SSDP.** Un socket UDP en el 1900 compartido con otros programas UPnP
(`SO_REUSEADDR`; también `SO_REUSEPORT` en Linux y macOS), unido al grupo
`239.255.255.250` en cada interfaz IPv4 privada con multidifusión. Anuncia
`alive` dos veces al empezar y cada 15 minutos, `byebye` al parar, y vuelve a
mirar las interfaces cada minuto. Responde a `M-SEARCH` tras un retraso al azar
dentro de `MX` (acotado a 5 s). Solo contesta a una dirección que sea **local y
pertenezca a una red a la que el equipo está conectado**, con la IP de *esa*
interfaz en `LOCATION`, y limita las búsquedas por origen (10 de golpe, una cada
2 s): UDP no se autentica y cada búsqueda cuesta un datagrama de respuesta.
Con `ssdp:all`, cada respuesta lleva su propio destino en `ST`. Un puerto ocupado
no impide arrancar: se avisa y el servidor sigue alcanzable escribiendo su
dirección en el televisor.

**HTTP**, en el mismo puerto y bajo `/dlna/`, **solo para la red local**: lo demás
es un `404`, como si no existiera, aunque el acceso remoto esté abierto.

- Descripción del dispositivo: `MediaServer:1` con `DMS-1.50`, `UDN` persistente
  (`server_settings`), `ContentDirectory:1` y `ConnectionManager:1`.
- `Browse`, `GetSearchCapabilities` (vacía), `GetSortCapabilities` (`dc:title`),
  `GetSystemUpdateID`; `GetProtocolInfo`, `GetCurrentConnectionIDs` y
  `GetCurrentConnectionInfo`. Errores UPnP con su código (401, 402, 501, 701, 706,
  710). El cuerpo SOAP se lee con el decodificador de Go, sin entidades externas,
  hasta 64 KiB y solo el primer elemento y sus hijos directos.
- **Árbol**: raíz → bibliotecas publicadas (por nombre) → elementos; series →
  temporadas → episodios (`1x02 Título`, por número). Solo se publica lo que se
  puede reproducir: un elemento sin archivo, con el archivo perdido o sin
  analizar no aparece, y el total coincide con lo listado. Paginación real (la
  base de datos), orden por título ascendente o descendente; cualquier otro
  criterio de orden se ignora en vez de rechazarse, porque rechazarlo dejaría la
  biblioteca vacía.
- **Entrega**: tres rutas, todas con un identificador validado antes de tocar la
  base de datos y con una única comprobación de acceso (que el archivo esté en
  una biblioteca publicada).

  | Ruta | Qué entrega |
  |------|-------------|
  | `/dlna/media/{archivo}/{nombre}` | El original, con `Range` (`DLNA.ORG_OP=01`) |
  | `/dlna/ts/{archivo}/{nombre}` | MPEG-TS progresivo, con salto por tiempo (`DLNA.ORG_OP=10`, `DLNA.ORG_CI=1`) |
  | `/dlna/art/{elemento}.jpg` | La carátula |

- **Qué se ofrece** (`dlna.mode`): `auto` entrega el original si un televisor
  genérico lo admite (`PresetFor("dlna")`: H.264 o MPEG-2, en MP4 o MPEG-TS, audio
  AAC, MP3 o AC-3 estéreo, hasta 1080p, 8 bits, sin HDR) y, si no, MPEG-TS;
  `direct` nunca recodifica; `transcode` siempre pasa por MPEG-TS, copiando lo que
  se pueda. Sin FFmpeg, o si hace falta algo que no puede hacer (tone mapping),
  se entrega el original: un televisor puede entenderlo, y no ofrecerlo no.
- **Subtítulos**: un televisor no elige pistas. Con `dlna.burn_subtitles: auto`, al
  recodificar se graba el subtítulo **forzado** (o el del idioma de los metadatos
  si el audio es de otro). Con `off`, ninguno. Un archivo que ya sería directo no se
  recodifica por un subtítulo que no se va a grabar.
- **MPEG-TS progresivo**: un único FFmpeg escribe en la respuesta (`pipe:1`, sin
  segmentos ni listas), con la misma cadena de filtros que HLS (tone mapping,
  subtítulos de texto e imagen, escalado). Un salto por `TimeSeekRange.dlna.org`
  reinicia FFmpeg en ese punto; fuera de la duración es un `406`. Reutiliza los
  cupos pesado y ligero: sin cupo, `503` con `Retry-After`. Si el codificador de
  hardware falla antes de escribir nada, reintenta con libx264.
- **`protocolInfo`**: `video/mp4`, `video/x-matroska`, `video/mpeg`… con
  `DLNA.ORG_OP`, `DLNA.ORG_CI` y las banderas habituales, y **sin `DLNA.ORG_PN`**:
  un identificador de perfil equivocado hace que un televisor estricto rechace el
  elemento, y sin él casi todos lo aceptan. Anunciar un perfil exige haberlo
  validado en un renderer.
- **Sesiones** `client_kind = 'dlna'`, sin usuario ni dispositivo, sin progreso ni
  historial. Todas las conexiones del mismo cliente al mismo archivo son una
  sesión (un renderer abre varias, con rangos). Un `HEAD` no abre sesión. Expiran por
  `dlna.idle_timeout_seconds` sin peticiones, **pero** se renuevan mientras haya una
  respuesta abierta: un televisor en pausa deja de leer y no debe perder la sesión.
  Si el administrador la cierra mientras el televisor está en pausa (la escritura
  está bloqueada), un plazo de escritura vencido libera la petición.
- **Eventos** (`SUBSCRIBE`): se acepta la suscripción con un `SID`, pero **no se envían
  eventos** y no se llama nunca a la URL de `CALLBACK`: el cliente que quiera
  novedades vuelve a navegar, y nadie puede hacer que el servidor conecte adonde
  quiera.

### Un defecto de la Fase 9, encontrado al probar DLNA

`decide` recogía un solo motivo de incompatibilidad del vídeo: si un HEVC HDR no
lo admitía el cliente por el códec (o por la profundidad de bits), el motivo
`hdr_unsupported` no llegaba a registrarse, `needsToneMap` daba falso y el vídeo se
recodificaba a H.264 **sin tone mapping**: colores lavados, sin error. Con el perfil
de DLNA (sin HEVC) pasaba siempre. Ahora `hdr_unsupported` se añade además del otro
motivo. El efecto visible: un HDR para un cliente que no puede convertirlo
(`transcoding.tone_mapping: off` o un FFmpeg sin filtros) se rechaza con
`PLAYBACK_UNSUPPORTED` en vez de reproducirse mal, y en DLNA se entrega el
original.

## Qué se comprobó

- Pruebas unitarias y de integración: clasificación (decenas de casos de origen, IPv4 e IPv6), puerta,
  administración, panel, sondeo, informe (con DNS, TLS y proxies simulados), SSDP
  (mensajes, filtros por origen, límite, retraso, anuncios, interfaces y un
  socket UDP real por bucle local), SOAP, DIDL-Lite, el árbol con series y
  paginación, el acceso por biblioteca, y FFmpeg real para copiar, recodificar,
  saltar, grabar subtítulos, cancelar, cupos y recuperación del codificador.
- Seguridad sobre el enrutador real: fuerza bruta por IP y por cuenta, CORS (un
  origen ajeno nunca recibe permiso; el configurado, solo con coincidencia
  exacta), rutas de administración con y sin sesión, cabeceras de reenvío falsas,
  TLS (versiones, HTTP/2, renovación y certificado roto).
- **`scripts/netcheck` contra el binario real, 47 de 47 en cada sistema:**
  - **Windows** (binario recién compilado, FFmpeg 9.0.2 de gyan.dev): el servidor
    comparte el puerto UDP 1900 con el servicio SSDP de Windows y responde a un
    `M-SEARCH` por multidifusión desde un socket real.
  - **Linux** (Alpine 3.22, FFmpeg 6.1.2, en el contenedor con la red del anfitrión de
    Docker Desktop): `SO_REUSEPORT`, multidifusión por tres interfaces y todo lo demás.

  Cubre el descubrimiento, la descripción, `Browse`, el original con rangos, el
  MPEG-TS sincronizado y con salto, las sesiones en el panel, la clasificación por
  un proxy simulado (cliente de fuera, de la LAN, sin cabecera, con cabecera falsa o
  ilegible), la administración cerrada, HSTS, CORS, la fuerza bruta por IP y por
  cuenta, la prueba de alcance, y TLS (certificado verificado, HTTP/2, sin TLS
  1.0 ni 1.1, nombre que no cubre). El CI la ejecuta en Ubuntu.
- **Pruebas de mutación** de lo nuevo (`go test -overlay`, sin tocar el código):
  ver el plan, Fase 13.

## Pendiente

- **Ningún renderer real.** La compatibilidad depende de cada fabricante (§13.2)
  y no se ha probado con televisores ni reproductores. La matriz de
  `docs/dlna-renderers.md` está vacía a propósito: no se anota como probado lo
  que no lo está. El criterio de salida (tres renderers de fabricantes distintos)
  **no se cumple**.
- Acceso remoto cifrado **desde fuera de la LAN de verdad**: se simuló con un proxy
  de confianza y con la prueba de alcance; no se probó con un dominio, un puerto
  reenviado y un teléfono con datos móviles.
- Eventos UPnP, `Search`, subtítulos externos por DLNA (`sec:CaptionInfoEx`),
  `DLNA.ORG_PN`, `PrepareForConnection` y `X_MS_MediaReceiverRegistrar` (Windows Media
  Player y Xbox lo piden).
- macOS: SSDP no se ha ejecutado. Linux: solo en el CI y en Alpine, sin multidifusión
  real entre equipos.
- Remote Relay (§14.3): no se diseña hasta que haya demanda (§26.5).
- El certificado propio generado para la LAN y mDNS siguen en la Fase 11.
