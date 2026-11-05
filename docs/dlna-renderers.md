# DLNA: renderers probados

**Estado: ningún renderer real probado.** MediaNode se ha comprobado con un punto
de control escrito para ello (`scripts/netcheck`), que descubre el servidor por SSDP,
lee su descripción, navega el árbol, pide el original con rangos y el flujo MPEG-TS
y salta por tiempo. Eso demuestra que el protocolo se cumple; **no demuestra que un
televisor concreto lo acepte**. Cada fabricante interpreta UPnP y DLNA a su
manera (§13.2), y por eso esta tabla se rellena solo con lo que alguien ha visto
funcionar en el aparato.

El criterio de salida de la Fase 13 pide tres renderers de fabricantes distintos.
**Hoy son cero.**

## Qué ofrece el servidor

Lo que un televisor recibe depende de `dlna.mode` (`auto`, `direct` o `transcode`) y,
en `auto`, de este perfil genérico (`PresetFor("dlna")`):

| | Se entrega tal cual si… | Si no |
|---|---|---|
| Contenedor | MP4 o MPEG-TS | MPEG-TS (se copia el vídeo si se puede) |
| Vídeo | H.264 o MPEG-2, hasta 1080p, 8 bits, sin HDR | Se recodifica a H.264 (HDR → SDR con tone mapping) |
| Audio | AAC, MP3 o AC-3, estéreo | Se recodifica a AAC estéreo |
| Subtítulos | no hay (ningún renderer elige pistas) | Con `dlna.burn_subtitles: auto`, el forzado grabado en la imagen |

Los `<res>` llevan `protocolInfo` con `DLNA.ORG_OP`, `DLNA.ORG_CI` y las banderas
habituales, y **sin `DLNA.ORG_PN`**: un perfil mal declarado hace que un televisor
estricto rechace el elemento.

## Cómo probar un renderer

1. En `server.yaml`: `discovery.dlna_enabled: true`. En el panel, **Bibliotecas →
   Publicar por DLNA** (pide confirmar el aviso).
2. Abre en el firewall el **UDP 1900** (SSDP) y el puerto HTTP (8420) para la red
   local. En Docker, el descubrimiento necesita `network_mode: host` (solo Linux):
   la red puente no recibe multidifusión.
3. En el televisor, busca servidores multimedia o fuentes DLNA. Debe aparecer el
   nombre de `server.name`.
4. Anota en el panel (**Administración → Sesiones**) qué sesión abrió, en qué modo
   (`direct_play`, `direct_stream` o `transcode`) y si el televisor llegó a saltar.
5. Con `dlna.mode: direct` y `transcode` por separado, prueba un MP4, un MKV con
   audio AC-3 y uno con HEVC.

## Matriz

Una fila por modelo y versión de software. «Descubre» es que aparece en el
televisor; «Navega», que lista bibliotecas y elementos; «Directo» y «MPEG-TS», que
reproduce; «Salto», que avanzar y retroceder funciona.

| Fabricante | Modelo | Software | Descubre | Navega | Directo | MPEG-TS | Salto | Notas |
|------------|--------|----------|:--------:|:------:|:-------:|:-------:|:-----:|-------|
| *(ninguno)* | | | | | | | | |

## Limitaciones conocidas de esta versión

- **Eventos UPnP**: el cambio de catálogo se avisa cada 5 s a quien se suscribió, pero
  el `CALLBACK` solo se acepta hacia la IP de quien suscribe (un televisor detrás de un
  puente o con otra interfaz de red para eventos no recibirá avisos). No se probó con
  un televisor.
- **`Search`**: solo `dc:title` y `upnp:class`; un renderer que busque por otra propiedad
  (`upnp:artist`, `dc:date`) recibe el error 708.
- **Subtítulos externos**: solo SRT, con `sec:CaptionInfoEx` (Samsung) y un `<res>`
  `text/srt`; no hay equivalente para LG o Sony más allá de ese `<res>`. En MPEG-TS van
  grabados.
- Sin **`X_MS_MediaReceiverRegistrar`**, que piden Windows Media Player y Xbox.
- El flujo MPEG-TS va en trozos (HTTP/1.1 sin `Content-Length`) y solo admite saltar
  por tiempo, no por bytes.
- El tone mapping por software puede no llegar a tiempo real en equipos modestos
  (ver `docs/adr/0007`): en el equipo de desarrollo con la build de FFmpeg de Windows
  va a un tercio de la velocidad.
- macOS: SSDP no se ha ejecutado.
