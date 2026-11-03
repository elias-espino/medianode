# 0004. Índice de keyframes para Direct Stream

**Estado:** aceptada la extracción; pendiente la reproducción\
**Fecha:** 2 de noviembre de 2026\
**Validación:** V4

## Decisión

Extraer keyframes leyendo paquetes, sin decodificar, en segundo plano. Nunca
durante la creación de una sesión.

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

## Pendiente

No se probó todavía reproducir `-c:v copy` con una playlist generada desde el
índice, ni se midió con un archivo real de 50 GB en un NAS.
