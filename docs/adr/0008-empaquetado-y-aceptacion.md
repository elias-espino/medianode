# 0008. Empaquetado y aceptación del MVP

**Estado:** aceptada; verificada con el binario de Windows y con Docker (Linux); sin probar en macOS ni con un systemd real\
**Fecha:** 4 de noviembre de 2026\
**Fase:** 10

## Decisión

### Formas de instalar

| Forma | Contenido | FFmpeg |
|-------|-----------|--------|
| Archivo por sistema (`.zip` en Windows, `.tar.gz` en Linux y macOS), `amd64` y `arm64` | `medianode-server` con el panel incrustado, README, licencia, configuración de ejemplo, el documento de licencias y, en Linux, `packaging/` | **No incluido**: lo instala quien lo usa |
| Imagen de Docker | Alpine 3.22, el binario, usuario `10001` sin privilegios | `ffmpeg` de Alpine (6.1.2), instalado al construir |
| systemd | Unidad con usuario propio y endurecimiento | Del sistema |

No se empaqueta FFmpeg porque la build que MediaNode necesita (`libx264`) es GPL y
distribuirla trae obligaciones de código fuente y texto de licencia
(`docs/licencias-ffmpeg.md`). MediaNode lo ejecuta como un programa aparte, así
que su licencia (MIT) no cambia.

### Herramienta de publicación en Go (`scripts/release`)

Compila los seis destinos sin CGO, empaqueta y escribe `SHA256SUMS`. Está en Go y
no en un script de shell porque así corre igual en Windows, Linux y macOS, no
depende de `zip` ni de `tar` del sistema, y se puede probar.

- Los archivos son **reproducibles**: orden fijo, fecha fija (`2026-01-01`) y
  permisos explícitos. Dos construcciones del mismo código dan la misma suma.
- Se niega a construir si falta `web/dist/index.html`: sin él el binario arranca
  pero sin panel, y nadie lo notaría hasta abrir el navegador.
- La versión se valida (`^[A-Za-z0-9][A-Za-z0-9._+-]{0,40}$`) porque va a nombres
  de archivo y a `-ldflags`.
- `release.yml` la ejecuta al subir una etiqueta `v*`. **No se ha ejecutado en
  GitHub**: no se subió ninguna etiqueta.

### Imagen de Docker

Construcción en tres etapas (panel con Node, servidor con Go, imagen final de
Alpine). Detalles que importan:

- El `Dockerfile` **falla** si `ffprobe` o `libx264` faltan, o si
  `ffmpeg -version` contiene `--enable-nonfree`.
- `WORKDIR /medianode` y sin `--config`: lee `/medianode/config/server.yaml` si
  existe y, si no, usa los valores por defecto con los datos en `/medianode/data`.
- Una carpeta de datos del equipo montada en `/medianode` tiene que pertenecer al
  usuario `10001`, o hay que fijar `user:` en Compose.
- `network_mode: host` en el ejemplo, para el descubrimiento de la fase 13; solo
  funciona en Linux. En Docker Desktop hay que publicar el puerto.
- Tamaño: 218 MB. Construcción: unos 5 minutos en frío.

### Perfil de capacidades del navegador

El panel ya no envía un perfil fijo. Declara lo que ha comprobado con
`canPlayType` y `MediaCapabilities`, y **no declara lo que no sabe que funciona**:

- No anuncia Matroska, aunque Chrome y Edge contesten «quizá»: sin garantías con
  el salto y las pistas, es mejor entregarlo por HLS.
- Declara HDR solo con pantalla HDR **y** decodificación de HEVC de 10 bits con
  PQ. Si declarase HDR sin poder decodificarlo, un vídeo de 10 bits pasaría sin
  tone mapping, con colores lavados. Sin HDR declara una lista vacía explícita,
  y el servidor hace el tone mapping.
- Anuncia HLS solo si hay Media Source o HLS nativo.

### Reanudar es cosa del cliente

El servidor no elige la posición inicial: la envía el cliente en
`start_position_ms` (§12.4). El panel no lo hacía —abría siempre desde el
principio— y los 14 criterios lo encontraron. Ahora, al abrir un elemento con
progreso de 10 s o más, el reproductor empieza ahí y ofrece «Empezar desde el
principio». Tras una sesión caducada, reanuda solo en la última posición, con un
tope de tres reanudaciones por minuto para que un servidor que cierra las
sesiones al momento no provoque un bucle.

### Tipos de la API generados

`web/src/api/schema.d.ts` se genera de `docs/openapi.yaml` con `openapi-typescript`
(`npm run gen:api`) y el CI falla si no está al día. **Solo son tipos**: el cliente
HTTP (`api.ts`) sigue escrito a mano. `npm audit` marca 3 avisos moderados, todos
en las dependencias de esa herramienta de desarrollo (`minimatch` dentro de
`@redocly/openapi-core`); no se distribuye ni procesa nada externo.

## Aceptación del MVP (§23.5)

`scripts/acceptance` recorre los 14 criterios por la API contra un servidor en
marcha. Se ejecutó contra:

1. **El binario de Windows** del `.zip` generado: suma comprobada, extraído en una
   carpeta nueva y arrancado con un `PATH` sin Go, Node ni Python, y sin archivo de
   configuración. **30 de 30.**
2. **La imagen de Docker** con un volumen nuevo y las bibliotecas montadas de
   solo lectura. **30 de 30.**

Cubre: configuración inicial con código de un solo uso; biblioteca y escaneo;
FFprobe; catálogo y búsqueda; permisos; Direct Play con `Range`; Direct Stream;
transcodificación; subtítulo WebVTT y cambio de audio en vivo; progreso;
**dos clientes a la vez** (Direct Play y transcodificación), con pausa
independiente; **reinicio durante una reproducción** (en Windows, matando el
proceso; en Docker, con `docker restart`) con catálogo y progreso intactos; y logs
sin secretos con el diagnóstico sin fallos.

Además:

- La suite completa de Go pasa en Linux (Alpine, FFmpeg **6.1.2**), con
  FFmpeg real, no solo con el 9.0.2 de desarrollo.
- Un `SIGKILL` al contenedor con una transcodificación en marcha: al arrancar
  quedan 0 directorios de trabajo, la sesión figura como expirada y los cupos
  vuelven a cero.
- El `.tar.gz` de Linux se verificó, se extrajo y arrancó en Alpine.

### Un hallazgo sobre el tone mapping

La misma máquina mide cosas muy distintas según la build: el tone mapping de un
HDR de 720p corre a **0,32× el tiempo real con la build de gyan.dev en Windows** y a
**1,5–2,0× con la de Alpine** dentro de WSL2. El diagnóstico y `tone_mapping_speed`
lo miden en cada equipo, así que el aviso es de esa instalación y no de la CPU.

## Pendiente

- Firefox (no está instalado) y Safari (HLS nativo). La reproducción por HLS con
  hls.js se probó en Edge y en Chrome 156 (sin interfaz), y el panel, en el navegador
  integrado.
- macOS: sin ejecutar nada, ni el binario ni la supervisión de FFmpeg.
- La unidad de systemd no se ha probado en un Linux real (`systemd-analyze verify`
  y un arranque).
- `release.yml` sin ejecutar en GitHub.
- Instalación «máquina limpia» real (sin las herramientas de desarrollo del equipo
  de trabajo): se simuló con una carpeta nueva y un `PATH` mínimo, pero es el mismo
  Windows.
- Imagen publicada en un registro, con el texto de la GPL y el fuente de FFmpeg.
- Instaladores `.msi`, `.deb`, `.rpm`, `.pkg` y servicio de Windows (§21.1 y §21.2).
- Los ajustes del panel son de solo lectura: no hay `PATCH /admin/settings` ni
  copias de seguridad desde el panel (`/admin/backups`).
