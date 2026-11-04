# 0007. Subtítulos, cambio de pista y HDR

**Estado:** aceptada; verificada con hls.js en Chromium, pendiente en Safari, AVPlayer y Media3\
**Fecha:** 4 de noviembre de 2026\
**Fase:** 9

## Decisión

### Subtítulos de texto → WebVTT

Una pista de texto (SRT, ASS/SSA, WebVTT, mov_text) interna del archivo, o un
subtítulo externo que no sea SRT ni WebVTT, se convierte con FFmpeg a WebVTT y
se guarda en `data/cache/subtitles/{file_id}/{track_id}.vtt`. La copia vale
mientras coincidan el tamaño y la fecha del archivo de origen. Un SRT externo
sigue convirtiéndose en el servidor sin FFmpeg.

- La extracción corre fuera de la petición que la pide, con un límite de dos
  a la vez, y las peticiones simultáneas de la misma pista comparten una.
  Si el cliente se va, la extracción termina y la siguiente la encuentra.
- Crear la sesión **no espera**: lanza la extracción y devuelve la URL.
  Pedir el subtítulo espera hasta 25 s; si no llega, `503` con `Retry-After`
  y la extracción sigue.
- Las pistas de imagen (PGS, VobSub, DVB) no se convierten: se graban.

### Subtítulos dentro de HLS

Para un cliente que solo declara `subtitle_delivery: ["hls"]`, el master lleva
`#EXT-X-MEDIA:TYPE=SUBTITLES` con `subs.m3u8`, una lista de un solo segmento
`subs.vtt` que dura lo que el vídeo. No lleva `X-TIMESTAMP-MAP`: la línea de
tiempo del vídeo empieza en cero.

### Grabar el subtítulo en la imagen (burn-in)

Fuerza recodificar el vídeo. La cadena de filtros se compone en un orden fijo:
tone mapping, subtítulos y, al final, el escalado, de modo que los subtítulos
no se tonemapean y se colocan sobre la resolución original.

- **Texto:** se convierte antes a ASS (conserva los estilos), se copia al
  directorio del trabajo como `sub.ass` y se aplica con `subtitles=sub.ass`.
  Un nombre relativo y sin caracteres especiales evita el escapado de
  `:` y `,` en el filtro. Tras un salto (`-ss`) los fotogramas empiezan en
  cero pero el subtítulo usa la línea de tiempo del archivo, así que se
  envuelve en `setpts=PTS+S/TB` … `setpts=PTS-STARTPTS`.
- **Imagen:** `-filter_complex` con `overlay` desde la pista del propio
  archivo. El vídeo sale del grafo (`-map [v]`).

### Cambio de pista en una sesión viva

`POST /playback/sessions/{id}/tracks` cambia el audio o los subtítulos.

- **HLS:** `Manager.Restream` cierra el flujo anterior, lo sustituye por otro
  con el plan nuevo y devuelve un `stream_url` con `?rev=N`. El cliente
  recarga la lista desde la posición actual. El cupo pertenece a la sesión y
  no se toca; si el cambio pasa de copiar a recodificar, se reserva el cupo
  pesado y, si no hay sitio, se devuelve el anterior y el flujo sigue como
  estaba (`503` con `Retry-After`).
- **Direct Play:** el archivo llega entero y el reproductor elige el audio, así
  que solo cambia la selección guardada y la URL del subtítulo. Si el cambio
  exigiera remux o transcodificar, `422` y hay que crear otra sesión.
- Un flujo cerrado lleva `closed = true`: una petición en vuelo que llega
  después ya no puede reiniciar FFmpeg sobre él.
- El estado de la sesión (petición y perfil) vive en memoria. Si el servidor
  se reinicia la sesión se cierra de todos modos y el cliente crea otra.

### Tone mapping HDR → SDR

Si el cliente no admite HDR y el origen es HDR10, HDR10+, HLG o Dolby Vision, el
vídeo se recodifica con `setparams` (BT.2020 y la transferencia del origen),
`zscale` a luz lineal, `tonemap=hable` y `zscale` a BT.709, en 8 bits. La
salida declara BT.709. Dolby Vision se trata por su capa base HDR10: los
perfiles sin capa base compatible (perfil 5) se verían con colores
equivocados.

- **Orden:** sin subtítulos grabados, el escalado va **antes** del tone
  mapping, que trabaja en coma flotante y cuesta por píxel: un 4K que se
  entrega en 1080p se tonemapea con la cuarta parte del trabajo.
- **Coste medido** en el equipo de desarrollo (Intel i5-2400, 4 núcleos), 720p,
  libx264 `veryfast`: el tone mapping completo tarda 9,1 s por cada 4 s de
  vídeo, es decir 0,44× tiempo real. Los hilos de los filtros apenas ayudan
  (7,8 s). Escalar a 480p antes lo deja en 4,5 s (0,9×).
- **Medición al arrancar:** `ffmpeg.Probe` comprueba que la cadena funciona y
  mide su velocidad (`tone_mapping_speed` en `/server/capabilities`).
- **Política (`transcoding.tone_mapping`, §10.8):** `auto` convierte siempre
  que FFmpeg pueda y avisa en el log si el equipo no llega a tiempo real;
  `realtime` solo convierte si llega; `off` no convierte nunca. Lo que no se
  convierte se rechaza con `422 PLAYBACK_UNSUPPORTED` y un motivo.

## Alternativas descartadas

- **`libplacebo` y `tonemap_opencl`:** necesitan GPU o OpenCL. Quedan como
  mejora cuando haya hardware donde probarlos.
- **Convertir las pistas de imagen con OCR:** fuera de alcance (§11.1).
- **Subtítulos de texto con `-vf subtitles=` sobre la ruta del archivo
  original:** obliga a escapar la ruta del archivo dentro del filtro.

## Verificación

Con el binario real, el escáner real y FFmpeg 9.0.2:

- 21 comprobaciones de extremo a extremo: el escáner reconoce SRT, ASS y HDR10
  internos, dos pistas de audio y dos subtítulos externos; conversión a
  WebVTT; burn-in de ASS; HDR10 → SDR con metadatos `bt709` y `yuv420p`.
- En Edge con hls.js 1.6.15: cambiar de audio en vivo (recarga y sigue), una
  pista de subtítulos dentro de HLS con sus 2 cues, burn-in y HDR.
- En las pruebas de rutas, el grabado se comprueba comparando fotogramas: con
  texto, la zona inferior difiere 7,2 (SRT) y 4,6 (ASS) de la referencia y la
  superior 0,3.

Pruebas de mutación: 37 mutantes, 35 muertos. Sobreviven dos equivalentes
(el atajo de caché de `Manager.Get`, que `run` repite, y el caso específico de
`DeadlineExceeded` del handler, cuyo resultado es el mismo que el de la rama por
defecto).

## Pendiente

- **El tone mapping por software no llega a tiempo real en el equipo de
  desarrollo** (0,32× medido al arrancar): en el navegador, un HDR de 720p
  empieza pero se corta (`fragLoadTimeOut`). Hace falta hardware más rápido o
  un camino por GPU.
- Grabar subtítulos de imagen (PGS, VobSub) está implementado y la cadena de
  filtros está probada, pero **no se ha ejecutado con una pista de imagen
  real**: ningún fixture la tiene y FFmpeg no genera una desde texto.
- Safari (HLS nativo), AVPlayer y Media3.
- Subtítulos dentro de HLS con vídeo fMP4 (el servidor los entrega, pero la
  prueba en navegador se hizo con vídeo copiado en MPEG-TS).
- Sin avisar al cliente cuando una extracción lenta termina: el cliente
  espera la respuesta o reintenta.
