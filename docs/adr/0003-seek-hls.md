# 0003. Seek con playlist sintética y reinicio de FFmpeg

**Estado:** aceptada y verificada con hls.js 1.6 en Edge (Chromium); pendiente en Safari, AVPlayer y Media3\
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

- ~~`init.mp4` cambia en cada reinicio.~~ Resuelto en la Fase 8: ver
  «Actualización de la Fase 8» más abajo. Con `frag_discont` el init es
  idéntico.
- La prueba usó un archivo sintético y `libx264`. Falta repetirla con
  codificadores por hardware, cuyos parámetros pueden cambiar.
- La precarga de hls.js puede pedir segmentos fuera de la ventana y provocar
  reinicios innecesarios. Hay que ajustar `seek_restart_segments`.
- No se validó la protección contra FFmpeg huérfano.

## Actualización de la Fase 8

La prueba de arriba usó un `init.mp4` por trabajo y daba por bueno que hls.js
decodificara con el primero. Al llevarlo al servidor real apareció el fallo
que ese riesgo anticipaba: el muxer HLS de FFmpeg empieza `tfdt` en cero en
cada trabajo y guarda el desfase en la lista de edición (`elst`) del `init`.
Un reproductor que use el `init` del primer trabajo, el único que declara la
playlist, coloca los segmentos de un reinicio al principio de la línea de
tiempo.

**Decisión:** pasar `-hls_segment_options movflags=+frag_discont`. Así `tfdt`
es absoluto y el `init` sale idéntico en cada reinicio, de modo que no hace
falta una `#EXT-X-MAP` por trabajo ni `#EXT-X-DISCONTINUITY`. Los cortes
siguen en una rejilla fija gracias a `-force_key_frames`. La decisión completa
está en el ADR 0006.

Verificación con el binario real, hls.js 1.6.15 y Edge sin interfaz:

| Prueba | Resultado |
|--------|-----------|
| Recodificar a 240p (`max_bitrate` bajo), 120 s: salto a 80 s y vuelta a 2 s | Reproduce, sin errores de hls.js |
| Recodificar clips de 4 s (HEVC y AC3) | Reproduce hasta el final |

Sigue sin probarse en Safari (HLS nativo), AVPlayer ni Media3. Los
codificadores por hardware no están probados: en el equipo de desarrollo solo
funciona `libx264`.
