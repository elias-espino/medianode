# 0006. HLS: fMP4 al recodificar, MPEG-TS al copiar

**Estado:** aceptada; verificada con hls.js en Chromium, pendiente en Safari, AVPlayer y Media3\
**Fecha:** 4 de noviembre de 2026\
**Fase:** 8

## Decisión

El contenedor de los segmentos depende de lo que se haga con el vídeo.

| Vídeo | Contenedor | Muxer de FFmpeg | Playlist |
|-------|------------|-----------------|----------|
| Recodificar | fMP4 (`init.mp4` + `.m4s`) | `hls` | VOD sintética sobre una rejilla fija |
| Copiar | MPEG-TS (`.ts`) | `segment` | VOD sintética desde el índice de keyframes; `EVENT` si no hay índice |

### Recodificar

- `-force_key_frames expr:gte(t,n_forced*N)` fuerza un keyframe en cada
  frontera, así que la rejilla es fija y la playlist se puede generar sin
  esperar a FFmpeg.
- `-hls_segment_options movflags=+frag_discont` hace que `tfdt` sea absoluto y
  que el `init.mp4` sea idéntico en todos los reinicios. Sin esta opción, el
  desfase queda en la lista de edición del `init` de cada trabajo y los
  segmentos posteriores a un salto se colocarían al principio de la línea de
  tiempo (ADR 0003).
- `-output_ts_offset` conserva la línea de tiempo del archivo.
- El audio que el cliente no admite se recodifica a AAC.

### Copiar

El muxer `hls` no sirve para copiar: corta en el primer keyframe posterior a
cada múltiplo de la duración, y esa rejilla cambia con cada reinicio. Se usa
el muxer `segment` con `-segment_times` explícitos, calculados del índice de
keyframes (ADR 0004).

- Los tiempos van en **microsegundos**. Redondear a milisegundos puede dejar
  el salto antes del keyframe y arrancar en el anterior.
- El salto (`-ss`) se hace 500 µs después del keyframe de inicio, y cada
  corte, 1 000 µs antes de su keyframe y relativo al salto. Así un reinicio
  produce los mismos segmentos que la pasada completa.
- Se eligió MPEG-TS porque el muxer `segment` lo escribe sin estado entre
  segmentos y admite cortes arbitrarios; fMP4 exigiría `init` y fragmentos
  alineados.
- Sin índice no se conocen los cortes: se usa `-segment_time` y una playlist
  `EVENT` que lista lo ya generado. `Media()` espera al primer segmento para
  no devolver una playlist vacía.

### Qué contenedor admite cada códec

`playback.hlsContainers` recodifica lo que el contenedor de salida no puede
llevar, aunque el cliente lo admita:

- MPEG-TS: vídeo `h264` o `hevc`; audio `aac`, `ac3`, `eac3` o `mp3`.
- fMP4: además `opus`, `flac` y `alac` de audio.

Un vídeo que no cabe en MPEG-TS (por ejemplo VP9) pasa a recodificarse, y con
ello el audio se evalúa contra la lista de fMP4.

### Perfil web

El preset `web` declara solo `http`. Un cliente que reproduzca HLS (hls.js)
debe declarar `protocols: ["http", "hls"]`; el panel lo hace. Sin ello, un
archivo que no se reproduce directamente se rechaza con `PLAYBACK_UNSUPPORTED`.

## Admisión y procesos

- Dos cupos: `max_concurrent_jobs` (recodificar vídeo) y
  `max_concurrent_light_jobs` (copiar). El cupo pertenece a la sesión, se
  reserva antes de crear la sesión y se libera de forma idempotente.
- Sin cupo, `503` con `Retry-After: 5` y código `TRANSCODE_CAPACITY_EXCEEDED`;
  si FFmpeg no arranca, `TRANSCODE_INITIALIZATION_FAILED`.
- Windows: Job Object con `KILL_ON_JOB_CLOSE`. Linux: `Pdeathsig`. macOS: sin
  supervisión; si el servidor muere de golpe, FFmpeg puede quedar vivo.
  Es una limitación conocida y está documentada.
- `transcoding.max_disk_gb` (20 por defecto) acota `data/transcodes`.

## Verificación

Con el binario real (`medianode-server`) y fixtures de `testdata/fixtures`:

- 27 comprobaciones de extremo a extremo: sesión, master, playlist, init y
  segmentos, `ffprobe` de lo servido, salto a un segmento lejano y vuelta,
  503 con `Retry-After`, ausencia de FFmpeg tras cerrar las sesiones, y FFmpeg
  muerto tras matar el servidor con `taskkill /F`.
- Reproducción en Edge sin interfaz con hls.js 1.6.15: Direct Stream
  (120 s, salto a 60 s y vuelta), recodificación (240p, salto a 80 s y vuelta)
  y los clips con AC3 y HEVC.

## Pendiente

- Safari (HLS nativo), AVPlayer y Media3.
- Codificadores por hardware: en el equipo de desarrollo solo funciona
  `libx264`.
- Supervisión de FFmpeg en macOS.
- Mapeo de tonos HDR y subtítulos grabados: Fase 9.
