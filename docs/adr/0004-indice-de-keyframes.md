# 0004. Índice de keyframes para Direct Stream

**Estado:** aceptada; la reproducción con índice se verificó en la Fase 8\
**Fecha:** 2 de noviembre de 2026\
**Validación:** V4

## Decisión

Extraer keyframes leyendo paquetes, sin decodificar, en segundo plano. La
creación de una sesión no calcula el índice: lo pide y espera como máximo
8 s (`indexWait`). Si no llega, la sesión usa la playlist `EVENT`.

```text
ffprobe -v error -select_streams v:0 -show_entries packet=pts_time,flags -of csv=p=0 <archivo>
```

## Resultados

Con un MKV de 554 MB en disco local, ejecutado dos veces:

| Método | Primera lectura | Segunda lectura |
|--------|-----------------|-----------------|
| `-skip_frame nokey` | 2,46 s (226 MB/s) | 1,93 s (287 MB/s) |
| Paquetes | 1,40 s (397 MB/s) | 1,08 s (514 MB/s) |

Ambos métodos encontraron los mismos 75 keyframes. El archivo acababa de
escribirse, por lo que estas cifras se benefician de la caché del sistema.

Por extrapolación, no por medición, 50 GB tardarían unos 2 minutos en un
disco local rápido, y unos 8 minutos sobre una red de 1 Gb/s.

## Consecuencias

Es viable, pero demasiado lento para hacer esperar al usuario. Hasta que el
índice exista, `direct_stream` usa una playlist `EVENT`, con seek limitado a
lo ya generado. El índice se guarda en caché por archivo.

## Actualización de la Fase 8

- Reproducir `-c:v copy` con la playlist sintética generada desde el índice
  funciona en hls.js 1.6.15 (Edge): arranca, salta a 60 s y vuelve sin error
  fatal. Solo apareció `bufferSeekOverHole`, que hls.js resuelve solo.
- La primera versión devolvía una playlist `EVENT` **vacía** al primer
  archivo sin índice, porque el cálculo es asíncrono; los reproductores la
  rechazan. Ahora la sesión espera al índice hasta 8 s y, si aun así falta,
  `Media()` espera al primer segmento antes de responder.
- Un archivo pequeño se indexa en menos de un segundo, así que la espera
  casi nunca se nota. Con uno enorme se agotan los 8 s y esa primera sesión
  funciona con seek limitado; las siguientes ya tienen el índice.

## Pendiente

No se midió con un archivo real de 50 GB en un NAS.
