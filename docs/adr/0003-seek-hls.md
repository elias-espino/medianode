# 0003. Seek con playlist sintética y reinicio de FFmpeg

**Estado:** aceptada para hls.js; pendiente en Safari, AVPlayer y Media3\
**Fecha:** 2 de noviembre de 2026\
**Validación:** V3

## Decisión

Usar playlist VOD sintética, reiniciar FFmpeg en saltos lejanos y añadir
`-output_ts_offset <start>` al comando.

## Resultados

Con FFmpeg 9.0.2 y un archivo de 120 s:

| Opción para conservar la línea de tiempo | Primer PTS del segmento 10 |
|------------------------------------------|---------------------------|
| Ninguna (comando original del plan) | 0,08 s: **incorrecto** |
| `-output_ts_offset 60` | 60,00 s: correcto |
| `-copyts` | Sin paquetes legibles: descartada |

Prueba de extremo a extremo en hls.js 1.5.17, con la lógica de reinicio del
plan y una sola `#EXT-X-MAP`:

| Salto | Tiempo hasta reproducir | Reinicia FFmpeg | Avanza sin error |
|-------|-------------------------|-----------------|------------------|
| 3 s a 90 s | 950 ms | Sí | Sí |
| 90 s a 30 s | 345 ms | No, zona generada | Sí |
| 30 s a 100 s | 175 ms | No, zona generada | Sí |
| 100 s a 92 s | 61 ms | No, zona generada | Sí |

Durante la prueba se precargaron segmentos adelantados, que provocaron otros
dos reinicios (desde 60 s y 114 s). FFmpeg no registró errores.

## Riesgos abiertos

- `init.mp4` cambia en cada reinicio, aunque mantiene los mismos parámetros
  de códec. hls.js decodifica con el primero, pero eso no garantiza el mismo
  comportamiento en AVPlayer o Media3. Si falla, hay que declarar otra
  `#EXT-X-MAP` con `#EXT-X-DISCONTINUITY`.
- La prueba usó un archivo sintético y `libx264`. Falta repetirla con
  codificadores por hardware, cuyos parámetros pueden cambiar.
- La precarga de hls.js puede pedir segmentos fuera de la ventana y provocar
  reinicios innecesarios. Hay que ajustar `seek_restart_segments`.
- No se validó la protección contra FFmpeg huérfano.
