# MediaNode

[English](README.md) · **Español**

Servidor multimedia autoalojado, escrito en Go. Le indicas tus carpetas de
películas y series, y las indexa, identifica qué son y las reproduce en un panel
web o en cualquier cliente que use su API REST. Un único binario, SQLite para
guardar los datos y ningún servicio externo obligatorio.

> **Estado: alfa.** Funciona de principio a fin en Windows y en Docker (Linux), pero
> es software joven. Cuenta con asperezas, cambios que rompen compatibilidad y funciones que
> faltan (mira [Qué funciona hoy](#qué-funciona-hoy)). No lo expongas a
> Internet.

## Qué funciona hoy

- **Bibliotecas.** De películas y de series, con una o varias carpetas. Un
  escaneo lee cada archivo con FFprobe, detecta archivos movidos y renombrados
  sin volver a leerlos, y sigue vigilando las carpetas por si cambian.
- **Identificación por el nombre del archivo.** `Película (1999).mkv`,
  `Serie.S01E02.mkv`, `1x03`, archivos con varios episodios y numeración suelta
  como `Serie 01 - Título.mp4`. Funciona sin Internet.
- **Metadatos de TMDB (opcional).** Títulos, sinopsis, géneros, reparto,
  pósters, fondos y clasificación por edades, con búsqueda, identificación y
  bloqueo manuales. Sin clave de API el catálogo sigue funcionando con los
  nombres de archivo.
- **Catálogo y búsqueda.** Navegación paginada por cursor, búsqueda que ignora
  acentos, temporadas y episodios, archivos, pistas (códec, HDR, idiomas) y
  subtítulos.
- **Usuarios y permisos.** Propietario, administradores, usuarios y perfiles
  restringidos por edad, con permisos `view`, `scan` y `manage` por biblioteca.
  Tokens de acceso con refresco rotatorio, dispositivos vinculados y códigos de
  vinculación para televisores.
- **Sesiones de reproducción.** Progreso, continuar viendo, historial,
  reanudación tras una caída y eventos en tiempo real por WebSocket.
- **Direct Play.** Se entrega el archivo original con `Range` de HTTP, así que
  se puede saltar a cualquier punto. Un motor de decisión ya calcula cuándo un
  cliente necesitaría remux o transcodificación.
- **Direct Stream y transcodificación por HLS.** Un archivo que el navegador no
  puede reproducir tal cual se remuxa (el vídeo se copia en segmentos MPEG-TS) o
  se recodifica a H.264/AAC (segmentos fMP4) mientras lo ves, con salto libre.
  Los trabajos simultáneos tienen límite (`transcoding.max_concurrent_jobs`), el
  servidor responde `503` con `Retry-After` cuando está lleno, y FFmpeg no
  sobrevive al servidor en Windows ni en Linux.
- **Subtítulos y audio.** Los subtítulos de texto, internos o externos (SRT,
  ASS, WebVTT), se convierten a WebVTT, se entregan junto al vídeo o dentro de
  HLS, o se graban en la imagen cuando el cliente no puede mostrarlos. El audio
  envolvente se mezcla a estéreo para los clientes que solo lo admiten. Se puede
  cambiar de audio y de subtítulos mientras se reproduce, desde la misma
  posición.
- **HDR a SDR.** Un vídeo HDR se convierte para los clientes que no pueden
  mostrarlo. Por software es caro: el servidor mide su velocidad al arrancar y
  `transcoding.tone_mapping` (`auto`, `realtime`, `off`) decide qué hacer si el
  equipo no llega.
- **Panel web.** Un panel en React incrustado en el binario: asistente de primer
  arranque, bibliotecas, usuarios, metadatos, búsqueda y un reproductor (Direct
  Play y HLS con hls.js) con selectores de audio y subtítulos.

**Aún no está:** clientes nativos para móvil y televisor, DLNA, descubrimiento en
la red local, acceso remoto, TLS e instaladores. El HLS se ha verificado en
Chromium con hls.js; los reproductores de Safari, iOS y Android no se han
probado, y solo se ha probado la codificación por software (libx264): los
codificadores por hardware se detectan pero no están verificados. Grabar
subtítulos de imagen (PGS, VobSub) está implementado pero no se ha ejecutado con
una pista real. En el equipo de desarrollo (un i5 antiguo de 4 núcleos), el tone
mapping de HDR por software va a un tercio del tiempo real y no se reproduce con
fluidez. En macOS, FFmpeg puede quedar vivo si el servidor muere de golpe.

## Instalación

No hacen falta herramientas de desarrollo. Elige una (los detalles, en
[`packaging/`](packaging)):

- **Binario.** Descarga el archivo de tu sistema (Windows, Linux o macOS, `amd64` o
  `arm64`), compruébalo con `SHA256SUMS`, descomprímelo y ejecuta
  `medianode-server`. Instala tú [FFmpeg](https://ffmpeg.org/download.html): es un
  programa aparte y no se incluye (ver
  [`docs/licencias-ffmpeg.md`](docs/licencias-ffmpeg.md)).
- **Docker.** `docker compose up -d --build`, tras editar las rutas de
  [`docker-compose.yml`](docker-compose.yml). La imagen trae FFmpeg.
- **systemd.** Una unidad endurecida y una configuración para un servicio de Linux.

Después abre <http://127.0.0.1:8420>, escribe el código inicial de un solo uso que
imprime el log y crea la cuenta propietaria.

`scripts/acceptance` comprueba los 14 criterios del MVP contra un servidor en
marcha: pasan en un binario de Windows limpio y en la imagen de Docker. La
reproducción por HLS se comprobó en Edge y Chrome. Aún sin probar: macOS, un
systemd real, Firefox y Safari.

## Compilar desde el código

### Requisitos

- [Go](https://go.dev/dl/) 1.27 o superior
- [Node.js](https://nodejs.org/) 20 o superior, solo para compilar el panel web
- [FFmpeg](https://ffmpeg.org/download.html) con `ffprobe`, en el `PATH` o junto
  al binario del servidor. `ffprobe` es imprescindible para escanear.

El servidor se compila sin CGO, así que no hace falta un compilador de C.

### Inicio rápido

```bash
git clone https://github.com/elias-espino/medianode.git
cd medianode

# Compila el panel web y el servidor (el panel va incrustado en el binario).
make build

# O a mano:
#   cd web && npm ci && npm run build && cd ..
#   go build -o bin/ ./cmd/...
```

Crea un archivo de configuración (todo tiene valores por defecto, así que es
opcional):

```bash
mkdir -p config
cp config/server.example.yaml config/server.yaml
```

Arranca el servidor:

```bash
./bin/medianode-server --config config/server.yaml
```

En el primer arranque el log muestra un **código de configuración de un solo
uso**:

```
servidor sin configurar: usa este código en /auth/setup
```

Abre <http://127.0.0.1:8420>, introduce ese código y crea la cuenta del
propietario. Después añade una biblioteca y apúntala a una carpeta del equipo
donde corre el servidor. Los datos, los logs y las copias de seguridad se
guardan junto al archivo de configuración.

### Configuración

Las rutas relativas parten de la raíz de instalación, la carpeta que contiene
`config/`. Cualquier clave admite una variable de entorno: `server.http_port` es
`MEDIANODE_SERVER_HTTP_PORT`. La lista completa está en
[`config/server.example.yaml`](config/server.example.yaml).

| Opción | Por defecto | Qué hace |
|---|---|---|
| `server.bind_address` | `0.0.0.0` | Interfaz donde escucha. Usa `127.0.0.1` para solo este equipo |
| `server.http_port` | `8420` | Puerto HTTP |
| `metadata.tmdb_api_key` | vacía | Clave de TMDB. Mejor `MEDIANODE_METADATA_TMDB_API_KEY` |
| `metadata.language` | `es-ES` | Idioma de los metadatos y país de la clasificación por edades |
| `scanner.workers` | `2` | Archivos que se analizan en paralelo |
| `scanner.missing_file_retention_days` | `30` | Días que se conserva un archivo desaparecido antes de purgarlo |
| `sessions.completed_threshold_percent` | `90` | Progreso a partir del cual algo cuenta como visto |

### Metadatos de TMDB

Consigue una clave de API gratuita en <https://www.themoviedb.org/settings/api>.
Cada instalación usa la suya. No la subas a ningún repositorio:

```bash
# Linux y macOS
export MEDIANODE_METADATA_TMDB_API_KEY="tu-clave"
# Windows PowerShell
$env:MEDIANODE_METADATA_TMDB_API_KEY = "tu-clave"
```

Si un título no se reconoce solo, abre **Metadatos** en el panel e identifícalo a
mano.

### Usarlo desde otro dispositivo

Pon `server.bind_address: "0.0.0.0"` y permite el puerto en el firewall. Después
abre `http://<ip-del-servidor>:8420` desde un dispositivo de la misma red.

Todavía no hay TLS, así que las contraseñas viajan sin cifrar. Úsalo solo en una
red doméstica de confianza y nunca redirijas el puerto desde tu router.

## Desarrollo

```bash
make test          # go test ./...
go test -short ./...   # sin las pruebas lentas (escaneo de 10 000 archivos, FFmpeg real)
make generate      # sqlc generate, tras editar queries/ o migrations/
make lint          # golangci-lint
make cross         # compila para Windows, Linux y macOS (amd64, arm64)

cd web && npm run dev   # panel con recarga en caliente, con /api hacia :8420
```

Las pruebas que necesitan FFmpeg o los vídeos de muestra se saltan solas si
faltan. `go run ./scripts/gen-fixtures` genera los vídeos.

El contrato de la API es [`docs/openapi.yaml`](docs/openapi.yaml), y una prueba
falla si una ruta no está documentada ahí.

### Estructura del proyecto

```
cmd/medianode-server   punto de entrada del servidor
cmd/medianode-cli      esqueleto de una futura herramienta de línea de comandos
internal/
  scanner  probe       indexación, vigilancia de archivos, FFprobe
  metadata images      TMDB, la cola de enriquecimiento y la caché de imágenes
  readmodel            consultas del catálogo y la búsqueda
  playback protocol    decisión de reproducción y perfiles de cliente
  sessions events      sesiones, progreso y el bus de eventos
  ffmpeg transcoding   comandos de FFmpeg, HLS y trabajos
  auth authorization   identidad y permisos
  api                  handlers HTTP, rutas, WebSocket
  database             SQLite, migraciones y código de sqlc
migrations/            migraciones de goose
queries/               SQL para sqlc
web/                   panel en React + TypeScript
docs/                  OpenAPI, documento de arquitectura, decisiones
```

## Documentación

El proyecto se construye por fases, y cada una se verifica antes de empezar la
siguiente.

- [`plan_de_trabajo.md`](plan_de_trabajo.md): el plan de trabajo, con lo que se
  decidió, se verificó y quedó pendiente en cada fase
- [`docs/documento_maestro_de_arquitectura.md`](docs/documento_maestro_de_arquitectura.md):
  el documento de arquitectura y diseño
- [`docs/adr/`](docs/adr): registros de decisiones de arquitectura
- [`packaging/`](packaging): instalar como binario, con Docker o con systemd
- [`docs/licencias-ffmpeg.md`](docs/licencias-ffmpeg.md): qué implica la licencia de
  FFmpeg al distribuir MediaNode

El plan y el documento de arquitectura están escritos en español.

## Contribuir

Es una alfa temprana y el diseño todavía se mueve, así que abre antes una issue
para comentar un cambio y después envía el pull request.

## Licencia

[MIT](LICENSE) © 2026 Elias Espino.

Este producto usa TMDB y las APIs de TMDB, pero no está respaldado, certificado
ni aprobado por TMDB. MediaNode ejecuta FFmpeg como un programa aparte: revisa la
licencia de la compilación de FFmpeg que instales.
