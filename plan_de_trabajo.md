# MediaNode — Plan de trabajo

**Base:** [`docs/documento_maestro_de_arquitectura.md`](docs/documento_maestro_de_arquitectura.md), versión 1.8\
**Fecha:** 4 de noviembre de 2026\
**Estado del repositorio:** alfa. Fases 0 a 8 completas y verificadas (con las salvedades de la Fase 8); la 9 (subtítulos, audio y HDR) es la siguiente

Este plan convierte la arquitectura en trabajo ordenado, desde el diseño
de tablas hasta la distribución. El DDL completo vive en la sección 6.2
del documento maestro y aquí no se repite: cada fase indica qué tablas
migra y qué funciones debe entregar.

Las firmas Go son contratos de diseño, no código compilado. Cada función
recibe `context.Context` cuando hace I/O.

------------------------------------------------------------------------

## 1. Vista general

| Fase | Nombre | Entrega principal | Tamaño | Depende de |
|------|--------|-------------------|--------|------------|
| 0 | Preparación y validaciones | Entorno, CI y riesgos técnicos resueltos | M | — |
| 1 | Esqueleto del servidor | Binario con config, logs y apagado controlado | S | 0 |
| 2 | Base de datos | SQLite, migraciones, SQLC y backup | M | 1 |
| 3 | Identidad y permisos | Login, tokens, dispositivos y autorización | L | 2 |
| 4 | Bibliotecas y escaneo | Indexación confiable con FFprobe | L | 3 |
| 5 | Catálogo y metadatos | API de lectura, búsqueda y panel inicial | L | 4 |
| 6 | Sesiones y progreso | Máquina de estados, heartbeat y reconciliación | M | 5 |
| 7 | Decisión y Direct Play | Playback Manager y entrega por Range | M | 6 |
| 8 | Transcodificación y HLS | Direct Stream, FFmpeg, seek y límites | XL | 7 |
| 9 | Subtítulos y HDR | Pistas, conversión, burn-in y tone mapping | L | 8 |
| 10 | Cliente web y empaquetado | **MVP completo** | L | 9 |
| 11 | Clientes móviles | Android, iOS, mDNS y vinculación | L | 10 |
| 12 | Clientes de TV | Android TV, tvOS, Tizen y webOS | XL | 11 |
| 13 | Red y compatibilidad | DLNA, Remote Direct y relay opcional | L | 10 |
| 14 | Producto completo | Instaladores, backups, perfiles y actualizaciones | L | 12, 13 |

Los tamaños son relativos entre fases. No son estimaciones de tiempo,
porque dependen del tamaño del equipo.

### Hitos

| Hito | Al terminar | Qué se puede demostrar |
|------|-------------|------------------------|
| H1 | Fase 5 | Una biblioteca real indexada y navegable |
| H2 | Fase 9 | Cualquier archivo de la matriz se reproduce con seek y subtítulos |
| H3 | Fase 10 | MVP: se instala sin herramientas de desarrollo y se usa desde el navegador |
| H4 | Fase 12 | Ecosistema de clientes |
| H5 | Fase 13 | Acceso remoto y DLNA |
| H6 | Fase 14 | Versión 1.0 preparada para distribuir |

### Equivalencia con el documento maestro

| Fase del plan | Roadmap §24 | Pasos §27 |
|---------------|-------------|-----------|
| 0 a 5 | Fase 1 | 1 a 8 |
| 6 a 9 | Fase 2 | 9 a 14 |
| 10 | Fases 2 y 3 | 15 |
| 11 | Fase 3 | 16 |
| 12 | Fases 3 y 4 | 17 |
| 13 | Fase 4 | 18 |
| 14 | Fase 5 | 19 |

------------------------------------------------------------------------

## 2. Reglas comunes

Se aplican a todas las fases.

**Definición de terminado:**

- `go test -race ./...`, `golangci-lint` y `sqlc diff` pasan en CI.
- `CGO_ENABLED=0 go build` funciona para Windows, Linux y macOS.
- Cada endpoint nuevo figura en `docs/openapi.yaml`.
- Los errores usan códigos estables de §22.3 y no revelan rutas.
- Los logs no contienen contraseñas, tokens ni URLs de `/stream`.

**Dependencias Go:**

| Uso | Paquete |
|-----|---------|
| Router | `github.com/go-chi/chi/v5` |
| SQLite | `modernc.org/sqlite` |
| Migraciones | `github.com/pressly/goose/v3` |
| SQL tipado | `sqlc` (herramienta de generación) |
| Logs | `github.com/rs/zerolog` |
| Rotación de logs | `gopkg.in/natefinch/lumberjack.v2` |
| Configuración | `gopkg.in/yaml.v3` |
| Argon2id | `golang.org/x/crypto/argon2` |
| Límites | `golang.org/x/time/rate` |
| JWT | `github.com/golang-jwt/jwt/v5` |
| WebSocket | `github.com/coder/websocket` |
| Watchers | `github.com/fsnotify/fsnotify` |
| OpenAPI | `github.com/oapi-codegen/oapi-codegen` |
| Tipo de contenido | Biblioteca estándar y tabla propia de MIME |

**Enumeraciones:** se declaran una sola vez en `internal/media` como
constantes tipadas. La base guarda el texto.

| Campo | Valores |
|-------|---------|
| `users.role` | `owner`, `admin`, `user`, `restricted` |
| `user_permissions.permission` | `view`, `scan`, `manage` |
| `libraries.type` | `movies`, `tv` |
| `media_items.type` | `movie`, `series`, `season`, `episode` |
| `media_items.metadata_status` | `pending`, `matched`, `unmatched`, `failed` |
| `media_files.probe_status` | `pending`, `done`, `failed` |
| `media_streams.stream_type` | `video`, `audio`, `subtitle`, `attachment`, `data` |
| `scan_jobs.status` | `running`, `completed`, `failed`, `interrupted` |
| `playback_sessions.status` | `created`, `initializing`, `playing`, `paused`, `stopping`, `completed`, `stopped`, `failed`, `expired` |
| `playback_sessions.client_kind` | `app`, `dlna` |
| `playback_sessions.playback_mode` | `direct_play`, `direct_stream`, `transcode` |
| `transcode_jobs.status` | `starting`, `running`, `stopped`, `completed`, `failed` |
| `transcode_jobs.job_kind` | `video`, `audio`, `remux` |

`Owner` y `admin` acceden a todas las bibliotecas sin filas en
`user_permissions`. Las consultas reciben ese dato como `is_admin`.

**Códigos de error:** cada código de §22.3 entra con la fase que lo
produce.

| Fase | Códigos |
|------|---------|
| 3 | `AUTH_INVALID_CREDENTIALS`, `AUTH_DEVICE_REVOKED`, `AUTH_REQUIRED`, `AUTH_TOKEN_EXPIRED`, `AUTH_SETUP_COMPLETED`, `AUTH_PAIRING_INVALID`, `USERNAME_TAKEN`, `VALIDATION_ERROR`, `RATE_LIMITED`, `FORBIDDEN`, `CSRF_INVALID` |
| 4 | `PERMISSION_LIBRARY_DENIED`, `LIBRARY_PATH_UNAVAILABLE`, `SCAN_FAILED` |
| 5 | `MEDIA_NOT_FOUND` |
| 6 | `SESSION_EXPIRED`, `SERVER_RESTART` |
| 7 | `MEDIA_FILE_UNAVAILABLE`, `PLAYBACK_UNSUPPORTED` |
| 8 | `TRANSCODE_CAPACITY_EXCEEDED`, `TRANSCODE_INITIALIZATION_FAILED` |
| 9 | `SUBTITLE_UNSUPPORTED` |

------------------------------------------------------------------------

## Fase 0 — Preparación y validaciones técnicas

**Objetivo:** preparar el entorno y resolver, con pruebas pequeñas, las
decisiones que cambiarían el diseño si salieran mal.

### Entorno

- [x] Instalar Go 1.27.3, FFmpeg 9.0.2, `sqlc` 1.31.1 y `goose` 3.29.0.
- [x] Crear el repositorio git y `go.mod` con el módulo `medianode`.
- [x] Crear la estructura de §5 con `.gitkeep`.
- [x] Configurar `.golangci.yml`, `sqlc.yaml` y un `Makefile` con
      `generate`, `test`, `lint`, `build` y `fixtures`.
- [x] Crear CI con matriz de Linux, Windows y macOS: tests con `-race`,
      lint, `sqlc diff` y compilación cruzada con `CGO_ENABLED=0`.
- [x] Crear `scripts/gen-fixtures`, que genera con `lavfi` archivos
      diminutos de la matriz §23.3. No se versionan binarios pesados.
- [ ] Publicar el repositorio remoto y confirmar CI en verde.
- [ ] Instalar `make`, `golangci-lint` y un compilador de C en el equipo
      de desarrollo. Sin este último, `-race` solo corre en CI.

### Validaciones

Cada una produce una nota en `docs/adr/` con la decisión tomada.

| Id | Pregunta | Prueba | Si falla |
|----|----------|--------|----------|
| V1 | ¿`modernc.org/sqlite` rinde lo suficiente? | Comparar con `mattn/go-sqlite3`: inserción por lotes de 100 000 filas y listados paginados con escritor activo | Cambiar a CGO y resolver la compilación cruzada con `zig cc` |
| V2 | ¿SQLC acepta `CREATE VIRTUAL TABLE ... fts5`? | Generar código con la migración real | Excluir `media_search` de SQLC y escribir esas consultas con `database/sql` |
| V3 | ¿Funciona la playlist sintética con reinicios? | Reproducir en hls.js, Safari, AVPlayer y Media3, saltando a mitad de película | Valorar `-copyts`, otro `#EXT-X-MAP` por trabajo o segmentos `.ts` |
| V4 | ¿Se puede hacer seek con video copiado? | Extraer keyframes con `ffprobe -skip_frame nokey` y medir el tiempo en un archivo de 50 GB | Usar playlist `EVENT` sin seek lejano para `direct_stream` |
| V5 | ¿Sirve el certificado propio en LAN? | Fijarlo en Android, iOS y navegador | Dejar HTTPS como opción y usar HTTP con aviso en LAN |

### Resultados

| Id | Estado | Decisión | Falta |
|----|--------|----------|-------|
| V1 | Parcial | Usar `modernc.org/sqlite` ([ADR 0001](docs/adr/0001-driver-sqlite.md)) | Comparar con CGO y medir en hardware modesto |
| V2 | Cerrada | `SearchItems` fuera de SQLC ([ADR 0002](docs/adr/0002-sqlc-y-fts5.md)) | — |
| V3 | Parcial | Añadir `-output_ts_offset` ([ADR 0003](docs/adr/0003-seek-hls.md)) | Safari, AVPlayer, Media3 y codificadores por hardware |
| V4 | Parcial | Índice por paquetes en segundo plano ([ADR 0004](docs/adr/0004-indice-de-keyframes.md)) | Reproducir video copiado y medir en un NAS |
| V5 | Pendiente | — | Requiere dispositivos reales |

**Criterio de salida:** CI en verde con un `main.go` vacío en los tres
sistemas, y V1 a V4 decididas. V5 puede esperar hasta la Fase 11. Lo que
falta en V3 y V4 necesita hardware Apple y Android, y se cierra antes de
terminar la Fase 8.

------------------------------------------------------------------------

## Fase 1 — Esqueleto del servidor

**Objetivo:** un binario que arranca, se configura, registra logs y se
apaga sin perder trabajo.

### Paquetes y funciones

```go
// internal/config
type Config struct { Server, Database, Paths, Scanner, Transcoding, Sessions, Discovery, Remote }
func Load(path string) (*Config, error)        // YAML + defaults + variables MEDIANODE_*
func (c *Config) Validate() error              // rutas absolutas, puertos, límites

// internal/lifecycle
type Component interface {
    Name() string
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
}
type Manager struct{ /* ... */ }
func (m *Manager) Register(c Component)
func (m *Manager) Run(ctx context.Context) error // arranca en orden y para en orden inverso

// internal/monitoring
func NewLogger(cfg LogConfig) (zerolog.Logger, io.Closer, error) // JSON a archivo con rotación y a consola
func WithRequestID(next http.Handler) http.Handler

// internal/api
func WriteJSON(w http.ResponseWriter, status int, v any)
func WriteError(w http.ResponseWriter, r *http.Request, err *core.APIError)

// internal/api/routes
func NewRouter(deps Deps) http.Handler

// internal/core
type APIError struct { Status int; Code, Message string; Details map[string]any }
```

### Formato de error

```json
{ "error": { "code": "MEDIA_NOT_FOUND", "message": "...", "request_id": "..." } }
```

### Tareas

- [x] `cmd/medianode-server/main.go`: flags `--config` y `--version`.
- [x] `Config.Load`, con la precedencia flag, entorno, archivo, default.
- [x] `lifecycle.Manager` con captura de `SIGINT` y `SIGTERM`, y con un
      plazo máximo de apagado.
- [x] Middleware: request ID, recover, logs de acceso con tokens
      redactados, límite de tamaño del cuerpo y timeouts de lectura.
- [x] `GET /health` sin autenticación, solo para supervisión.
- [x] `GET /api/v1/server/version` y `GET /api/v1/server/info`.
- [x] Inyectar la versión con `-ldflags`.

### Pruebas

- [x] Configuración inválida produce un error claro y no arranca.
- [x] Un componente que tarda más que el plazo no bloquea el apagado.
- [x] El panic en un handler devuelve 500 estructurado y no cae el proceso.

### Decisiones tomadas al implementar

- **Enrutador:** `NewRouter` vive en `internal/api/routes`, porque en
  `internal/api` crearía un ciclo de imports con `handlers`.
- **Rotación de logs:** se añade `gopkg.in/natefinch/lumberjack.v2`.
- **Configuración nueva:** `logging.level` y
  `server.shutdown_timeout_seconds`, que §20.2 aún no incluye.
- **HTTPS:** el default es `false` y `https_enabled: true` se rechaza con
  un error claro hasta la Fase 11. Así, nadie cree tener TLS sin tenerlo.
  §20.2 describe el valor final, que es `true`.
- **Rutas relativas:** parten de la carpeta que contiene `config/`, no del
  directorio actual, porque un servicio puede arrancar desde cualquier sitio.
- **Códigos de error:** se añaden `INTERNAL_ERROR`, `NOT_FOUND` y
  `METHOD_NOT_ALLOWED`, genéricos y ausentes en §22.3.
- **Timeouts:** no hay `WriteTimeout`, porque el streaming mantiene las
  respuestas abiertas durante horas.

**Criterio de salida:** el binario arranca en los tres sistemas, responde
`/health` y sale con código 0 ante `SIGTERM`.

**Estado:** cumplido en Windows, incluida la salida con código 0 ante
`CTRL_BREAK`, que Go entrega como interrupción. En Linux y macOS compila y
pasa `go vet`, pero `SIGTERM` real solo se comprobará en CI.

------------------------------------------------------------------------

## Fase 2 — Base de datos

**Objetivo:** capa de datos segura para concurrencia, con migraciones
versionadas.

### Diseño de conexiones

Un pool escritor de una sola conexión y un pool lector aplican la regla
de «un escritor lógico» de §6.3.

```go
// internal/database
type DB struct { Write, Read *sql.DB; Q, R *sqlc.Queries } // Q escribe, R lee

func Open(ctx context.Context, cfg config.Database) (*DB, error)
func (db *DB) InTx(ctx context.Context, fn func(q *sqlc.Queries) error) error
func (db *DB) Migrate(ctx context.Context, backupDir string) error
func (db *DB) Backup(ctx context.Context, dst string) error
func (db *DB) Close() error
```

DSN del escritor. El lector no lleva `journal_mode` ni `_txlock`, y añade
`query_only`. La ruta va escapada, porque forma parte de una URI:

```text
file:medianode.db?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)
&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_txlock=immediate
```

- `Write.SetMaxOpenConns(1)`.
- `Read.SetMaxOpenConns(runtime.NumCPU())`, con un mínimo de 4.
- `_txlock=immediate` evita que una transacción falle al pasar de lectura
  a escritura, un caso que `busy_timeout` no cubre.
- `Backup` usa `VACUUM INTO`, que da una copia consistente con WAL activo.

### Migraciones

Se embeben con `embed.FS` y se aplican con `goose.NewProvider`. Cada
fase crea solo sus tablas, en orden de dependencia:

| Migración | Fase | Tablas e índices |
|-----------|------|------------------|
| `00001_core.sql` | 2 | `server_settings` |
| `00002_identity.sql` | 3 | `users`, `user_preferences`, `devices`, `refresh_tokens`, `pairing_codes` |
| `00003_libraries.sql` | 4 | `libraries`, `library_paths`, `user_permissions`, `scan_jobs`, `idx_scan_library_status`, `idx_scan_one_running` |
| `00004_catalog.sql` | 4 | `media_items` (con `match_key`), `media_external_ids`, `genres`, `media_item_genres`, `people`, `media_item_people`, `media_images` e índices de catálogo |
| `00005_files.sql` | 4 | `media_files` (con `duration_ms` y `bitrate`), `media_file_items`, `media_streams`, `subtitles` e índices |
| `00006_search.sql` | 5 | `media_search` |
| `00007_playback.sql` | 6 | `playback_progress`, `playback_sessions`, `playback_history`, `transcode_jobs` e índices |

`user_permissions` se crea en la Fase 4 porque depende de `libraries`.

### Tareas

- [x] `database.Open` con ambos pools y comprobación de `PRAGMA
      foreign_keys`, `journal_mode` y versión de SQLite.
- [x] Comprobar que el directorio de datos es local. Si es una ruta de
      red, rechazar el arranque (§2, principio 6).
- [x] `Migrate`: si hay migraciones pendientes, hacer backup, migrar y
      detener el arranque con un error claro si algo falla.
- [x] `00001_core.sql`, primera consulta SQLC y `internal/storage` para
      crear los directorios de datos.
- [x] Helpers: `NewID()` con UUID v7, `Now()` con el formato único de
      §6.3 y funciones para convertir tiempos.

UUID v7 se elige porque ordena por creación e inserta mejor en índices.

### Pruebas

- [x] 50 goroutines escriben a la vez sin `SQLITE_BUSY`.
- [x] Un lector no se bloquea durante una escritura larga.
- [x] Una migración que falla se revierte y conserva el backup.
- [x] Las claves foráneas se cumplen en todas las conexiones del pool.

### Decisiones tomadas al implementar

- **`Q` y `R`:** `DB` expone una consulta por pool. Con un solo `Q`, cada
  lectura pasaría por la única conexión de escritura.
- **`InTx`:** dentro de una transacción solo se usa el `q` recibido. Usar
  `db.Q` se bloquearía esperando a esa misma conexión.
- **Migraciones parciales:** goose aplica cada migración en su propia
  transacción. Si falla la tercera de cinco, esa se revierte, las dos
  anteriores se conservan, y el siguiente arranque continúa desde ahí.
- **Base nueva:** sin datos previos no hay backup.
- **Base más reciente que el binario:** se rechaza con `ErrNewerDatabase`.
- **Embebido:** `migrations/embed.go` expone las migraciones al binario.

### Verificación por mutación

Se rompió a propósito cada protección para comprobar que una prueba falla.
Esto reveló que nada comprobaba el pool de una conexión ni el rechazo de
rutas de red, y se añadieron pruebas. Sigue sin cubrirse que `Backup`
llame a `VerifyBackup`: la función está probada por separado, pero provocar
una copia dañada desde `VACUUM INTO` no es práctico.

**Criterio de salida:** base creada desde cero en los tres sistemas, con
backup previo verificado al abrir la copia.

**Estado:** cumplido en Windows. En Linux y macOS compila y pasa `go vet`,
pero solo se ejecutará en CI. La detección de sistemas de red en esas dos
plataformas no se ha probado con un montaje real.

------------------------------------------------------------------------

## Fase 3 — Identidad y permisos

**Objetivo:** usuarios, dispositivos, tokens y autorización. A partir de
aquí, ningún endpoint nuevo queda sin control de acceso.

### Preferencias de usuario

§11.2 pide preferencias de usuario. Se guardan en esta tabla de §6.2:

```sql
CREATE TABLE user_preferences (
    user_id TEXT PRIMARY KEY,
    audio_language TEXT,
    subtitle_language TEXT,
    subtitle_mode TEXT NOT NULL DEFAULT 'default',
    max_bitrate_remote INTEGER,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
```

### Funciones

```go
// internal/auth
func HashPassword(pw string) (string, error)     // Argon2id, formato PHC
func VerifyPassword(hash, pw string) (bool, error)

func (s *Service) Setup(ctx, code, username, password string) (*User, error)
func (s *Service) Login(ctx, username, password string, dev DeviceInfo) (*TokenPair, error)
func (s *Service) Refresh(ctx, refreshToken string) (*TokenPair, error)
func (s *Service) Logout(ctx, deviceID string) error
func (s *Service) CreatePairingCode(ctx) (*PairingCode, error)
func (s *Service) ClaimPairingCode(ctx, code string, user *User, dev DeviceInfo) (*TokenPair, error)

// internal/users
func (s *Service) Create(ctx, actor Actor, p CreateParams) (*User, error)
func (s *Service) Update(ctx, actor Actor, id string, p UpdateParams) (*User, error)
func (s *Service) Delete(ctx, actor Actor, id string) error // cancela sus sesiones
func (s *Service) Preferences(ctx, userID string) (*Preferences, error)

// internal/devices
func (s *Service) Revoke(ctx, actor Actor, deviceID string) error
func (s *Service) IsRevoked(deviceID string) bool // caché en memoria invalidada al revocar

// internal/authorization
type Actor struct { UserID, DeviceID string; Role Role }
func (a *Authorizer) CanLibrary(ctx, actor Actor, libraryID string, p Permission) error
func (a *Authorizer) CanItem(ctx, actor Actor, itemID string, p Permission) error
func (a *Authorizer) CanFile(ctx, actor Actor, fileID string) error
func (a *Authorizer) RequireRole(actor Actor, min Role) error

// internal/api/middleware
func Authenticate(s *auth.Service) func(http.Handler) http.Handler
func RequireCSRF(next http.Handler) http.Handler
func RateLimit(l *Limiter) func(http.Handler) http.Handler
```

### Parámetros

| Elemento | Valor | Motivo |
|----------|-------|--------|
| Argon2id | 64 MiB, 3 iteraciones, 2 hilos | Ajustable con benchmark en hardware modesto |
| Access token | JWT HS256, 15 min | Claims `sub`, `did`, `role` |
| Refresh token | 32 bytes aleatorios, 90 días | Se guarda solo su SHA-256 |
| Secreto JWT | 32 bytes en `server_settings` | Generado en el primer arranque |
| Código inicial | 128 bits, un solo uso | Se guarda su hash en `server_settings` |
| Código de vinculación | 6 caracteres, 5 min, un solo uso | Con límite estricto de intentos |
| Rate limit de login | 5 intentos por minuto y por cuenta | Y límite por IP |

**Rotación:** al refrescar, el token anterior recibe `rotated_at`. Si
llega un token ya rotado, fuera de una ventana de 30 s para reintentos de
red, se considera robado y se revocan todos los del dispositivo.

**Transporte:** según §15.4. La cookie web lleva `HttpOnly` y
`SameSite=Strict`, con `Secure` si hay TLS. CSRF usa doble envío en la
cabecera `X-MediaNode-CSRF`.

### Endpoints

Del documento: `/auth/setup`, `/auth/login`, `/auth/refresh`,
`/auth/logout`, `/auth/me`, `/auth/pairing`, `/auth/pairing/claim`,
`/auth/pairing/token`, `/users/me/password`, `/devices`,
`/devices/{id}` (GET, PATCH, DELETE).

También, para el panel Users y las preferencias:

| Método | Ruta | Propósito |
|--------|------|-----------|
| GET, POST | `/admin/users` | Listar y crear usuarios |
| GET, PATCH, DELETE | `/admin/users/{id}` | Gestionar usuario |
| GET, PUT | `/users/me/preferences` | Preferencias propias |

### Tareas

- [x] Migración `00002_identity.sql` y consultas SQLC.
- [x] Generar el código inicial y mostrarlo en consola y logs.
- [x] Login, refresh, logout y revocación, cada uno en una transacción.
- [x] Middleware de autenticación con Bearer y cookie.
- [x] CORS restrictivo: solo los orígenes de `server.cors_origins`.
- [x] Autorización con roles. `CanLibrary`, `CanItem` y `CanFile` se
      escriben en la Fase 4, con las tablas que consultan.
- [x] Registrar intentos de acceso, con éxito y sin él.
- [x] `docs/openapi.yaml`, que faltaba desde la Fase 1. Una prueba exige
      que coincida con las rutas montadas.

### Pruebas

- [x] Reutilizar un refresh token rotado revoca el dispositivo.
- [x] Un dispositivo revocado deja de funcionar en la siguiente petición,
      sin esperar a que caduque su access token.
- [x] `/auth/setup` falla sin código y falla siempre una vez creado el
      Owner.
- [x] Un usuario no puede revocar dispositivos ajenos.
- [x] Login sin usuario existente tarda lo mismo que con contraseña
      errónea: 0,222 s frente a 0,227 s en el binario real.
- [x] Una petición con cookie sin cabecera CSRF se rechaza.
- [x] Toda ruta fuera de una lista explícita de públicas responde 401 sin
      sesión, y toda ruta `/admin/` responde 403 a un `user`.

### Decisiones tomadas al implementar

- **Vinculación en tres pasos:** el diseño anterior devolvía los tokens a
  quien reclamaba el código, no a la TV que lo mostraba, y la TV no tenía
  forma de recogerlos. Ahora la TV pide el código y recibe un
  `poll_token`; un usuario con sesión lo reclama en
  `/auth/pairing/claim`, y la TV recoge sus tokens una sola vez en
  `/auth/pairing/token`. `pairing_codes` guarda los datos del dispositivo
  hasta entonces.
- **Estado leído en cada petición:** `Authenticate` consulta el
  dispositivo y el usuario en la base, en lugar de la caché `IsRevoked`
  prevista. Así revocar, desactivar, borrar o cambiar un rol surte efecto
  en la siguiente petición. Cuesta 0,1 ms por petición, JWT incluido. Por
  eso el rol ya no va en el token: habría dos fuentes de verdad.
- **Robo confirmado aunque falle la llamada:** la revocación por token
  reutilizado se confirma antes de devolver el error. Hacerlo dentro de un
  `InTx` que devuelve error la habría revertido.
- **Reintento dentro de la ventana:** emite un par nuevo y revoca el
  sucesor que no llegó, para que cada dispositivo tenga una sola cadena.
  La ventana cuenta desde la primera rotación y no se renueva.
- **Semáforo de Argon2id:** cada cálculo reserva 64 MiB. Sin límite de
  concurrencia, una avalancha de logins con cuentas distintas agota la
  memoria aunque cada cuenta tenga su cupo. Ningún hash se calcula dentro
  de una transacción, porque bloquearía al único escritor.
- **Límites:** 5 intentos por minuto y cuenta, sin distinguir mayúsculas;
  20 por IP en login y 10 en setup y vinculación. Reclamar un código tiene
  además un tope global de 30 por minuto, porque son 30 bits y repartir
  intentos entre cuentas no debe sumar. Las claves inactivas se barren.
- **IP del cliente:** `X-Forwarded-For` solo cuenta desde
  `trusted_proxies`, y se recorre desde el final.
- **Rangos:** owner > admin > user > restricted. Solo se gestiona a quien
  tiene rango inferior, lo que impide crear un segundo Owner, actuar sobre
  otro admin o borrarse a uno mismo. Un índice único parcial garantiza
  además un solo Owner en la base. Nadie cambia su propio rol ni se
  desactiva.
- **Contraseña propia:** se añade `PUT /users/me/password`, que exige la
  actual y revoca los demás dispositivos. Sin él, un usuario no podía
  cambiar la contraseña que le asignó un administrador. Un restablecimiento
  por administrador revoca todos los dispositivos del usuario.
- **Dispositivos:** un login con el mismo `device_identifier` reutiliza la
  fila activa y retira sus tokens. Una fila revocada nunca se reactiva.
  Un dispositivo ajeno responde 404, para no revelar que existe.
- **Web:** `platform: "web"` recibe cookies en lugar de tokens. La de
  refresh se limita a `/api/v1/auth`. Renovar por cookie también exige
  CSRF.
- **`/server/info`** incluye `setup_required` para que el cliente sepa si
  debe mostrar la creación del Owner.
- **Código inicial:** solo se guarda su hash, así que cada arranque sin
  Owner genera uno nuevo. Es el único secreto que va al log, porque un
  servicio no tiene consola.
- **`subtitle_mode`:** `default`, `forced`, `always` u `off`.

### Verificación por mutación

Se aplicaron 79 mutaciones mediante `go test -overlay`, sin tocar el
código fuente. Tras corregir las que no compilaban o estaban mal
planteadas, todas se detectan. El proceso reveló dos huecos en las
pruebas, ya cubiertos: una cookie CSRF vacía con cabecera vacía, y un
`user` que veía dispositivos de un `restricted`. También mostró que la
comprobación «no borrarse a uno mismo» era código muerto, y se eliminó.

**Criterio de salida:** flujo completo de setup, login, refresh, logout y
vinculación por API.

**Estado:** cumplido en Windows, con el binario real y Argon2id real.

**Pendiente o sin verificar:**

- `-race`, Linux y macOS, solo en CI.
- La recuperación de la cuenta del Owner de §15.2 no tiene mecanismo
  todavía. Un Owner que olvide la contraseña no puede entrar.
- Borrar o desactivar un usuario debe cancelar sus sesiones de
  reproducción cuando existan, en la Fase 6.
- `GET /admin/settings`, en la Fase 14, no debe exponer
  `auth.jwt_secret` ni `auth.setup_code_hash`.
- El cliente web debe enviar `{}` o cuerpo vacío en el refresh y copiar
  la cookie `mn_csrf` a la cabecera.

------------------------------------------------------------------------

## Fase 4 — Bibliotecas y escaneo

**Objetivo:** indexar bibliotecas reales sin perder datos ante discos
desconectados, archivos movidos o escaneos interrumpidos.

### Endpoints

Del documento: `/libraries` (GET, POST), `/libraries/{id}` (GET, PATCH,
DELETE), `/libraries/{id}/scan` (GET, POST).

También, para gestionar rutas y permisos:

| Método | Ruta | Propósito |
|--------|------|-----------|
| POST | `/libraries/{id}/paths` | Añadir ruta |
| DELETE | `/libraries/{id}/paths/{pathId}` | Quitar ruta |
| GET, PUT | `/admin/users/{id}/permissions` | Permisos por biblioteca |

El último llega en esta fase porque `user_permissions` depende de
`libraries`.

```go
// internal/libraries
func (s *Service) Create(ctx, actor Actor, p CreateParams) (*Library, error)
func (s *Service) Update(ctx, actor Actor, id string, p UpdateParams) (*Library, error)
func (s *Service) Delete(ctx, actor Actor, id string) error // cancela escaneo y sesiones
func (s *Service) AddPath(ctx, actor Actor, id, path string) (*LibraryPath, error)
func (s *Service) RemovePath(ctx, actor Actor, id, pathID string) error
```

### Resolución segura de rutas

```go
// internal/storage
func OpenMedia(ctx, lp LibraryPath, relativePath string) (*os.File, error)
func ValidateLibraryRoot(path string) error
func IsWithinRoot(root, target string, followSymlinks bool) (bool, error)
```

`OpenMedia` usa `os.OpenRoot`, que impide salir de la raíz incluso con
symlinks. Si el administrador autoriza symlinks externos (§7.4), se
valida cada destino con `IsWithinRoot` contra las raíces permitidas. Es
el único punto del servidor que abre medios. Las rutas de un cliente
nunca llegan aquí: solo se usan identificadores.

### Pipeline del escáner

```go
// internal/scanner
type Scanner struct{ /* ... */ }
func (s *Scanner) Run(ctx, libraryID string, trigger Trigger) (jobID string, err error)
func (s *Scanner) walk(ctx, lp LibraryPath, out chan<- FoundFile) error
func (s *Scanner) diff(found FoundFile, known map[string]KnownFile) ChangeKind
func (s *Scanner) persist(ctx, batch []ProbedFile) error  // transacción por lote
func (s *Scanner) reconcileMissing(ctx, lp LibraryPath, unseen []KnownFile) error
func (s *Scanner) detectMoves(ctx, added, missing []File) ([]Move, error)
func (s *Scanner) purgeExpired(ctx) error

// internal/scanner/watch.go
func (w *Watcher) Start(ctx) error // eventos con debounce hacia Run parcial

// internal/probe
func Run(ctx, path string, timeout time.Duration) (*ProbeResult, error)
func Parse(jsonOut []byte) (*ProbeResult, error)

// internal/scanner/identify.go
func ParseMovie(relPath string) MovieGuess
func ParseEpisode(relPath string) EpisodeGuess
```

Etapas, conectadas por canales con buffer:

```text
walk ─► diff ─► probe (N workers) ─► persist por lotes ─► identify ─► library.updated
```

1. **Inicio:** rechazar si ya hay un `scan_jobs` en `running` para esa
   biblioteca. Comprobar cada raíz. Si no está accesible, marcar
   `is_available = 0` y omitirla.
2. **Carga:** leer en memoria `relative_path → (id, size, mtime)`.
3. **Recorrido:** `filepath.WalkDir` con filtro de extensiones, sin
   ocultos y con límite de profundidad.
4. **Diferencia:** si tamaño y `mtime` coinciden, se omite. Los restantes
   pasan a FFprobe.
5. **Persistencia:** lotes de 200 archivos, cada uno en una transacción
   corta. Nunca una transacción durante todo el escaneo.
6. **Ausentes:** lo que queda en el mapa y no se vio es `missing_since`.
   Solo si la raíz sigue accesible al final del recorrido.
7. **Movimientos:** coinciden si tienen el mismo tamaño y la misma huella
   parcial, `SHA-256(tamaño + primer MiB + último MiB)`.
8. **Identificación:** crear o reutilizar `MediaItem` y enlazar en
   `media_file_items`, siempre sin red.
9. **Cierre:** actualizar `last_scan_at`, cerrar `scan_jobs` y emitir
   `library.updated`.

Comando de análisis, sin shell:

```text
ffprobe -v error -print_format json -show_format -show_streams -show_chapters <ruta>
```

### Identificación local

| Patrón | Resultado |
|--------|-----------|
| `S(\d{1,2})E(\d{1,3})(?:-?E(\d{1,3}))?` | Temporada, episodio y fin del rango |
| `(?:^\|[^0-9])(\d{1,2})x(\d{2,3})(?:[^0-9]\|$)` | Temporada y episodio; no coincide con `1080x720` |
| `^(.+?)[ ._(\[]((?:19\|20)\d{2})` | Título y año de película |
| `Season[ ._]?(\d+)` o `Temporada[ ._]?(\d+)` | Temporada desde la carpeta |

Si ningún patrón coincide, el título es el nombre del archivo sin
extensión. En todos los casos se normalizan puntos, guiones bajos y
espacios sobrantes, porque el patrón deja restos como `Matrix `.

Las películas se agrupan por `(library_id, título normalizado, año)`, de
modo que dos archivos son dos versiones del mismo elemento. Un rango
`S01E01-E02` genera dos episodios y dos filas en `media_file_items`.

### Limpieza por retención

```sql
-- name: DeleteOrphanLeafItems :exec
DELETE FROM media_items
WHERE type IN ('movie', 'episode')
  AND NOT EXISTS (
    SELECT 1 FROM media_file_items l WHERE l.media_item_id = media_items.id
  );

-- name: DeleteEmptyParents :exec
DELETE FROM media_items
WHERE media_items.type = ?1
  AND NOT EXISTS (
    SELECT 1 FROM media_items c WHERE c.parent_id = media_items.id
  );
```

`DeleteEmptyParents` se ejecuta primero con `season` y después con
`series`.

### Tareas

- [x] Migraciones `00003` a `00005` y consultas SQLC.
- [x] CRUD de bibliotecas y rutas, con `ValidateLibraryRoot`.
- [x] Completar `Authorizer` por biblioteca, elemento y archivo.
- [x] Antes de `DELETE /libraries/{id}`, detener su escaneo.
- [x] Escáner por etapas con cancelación por `context`.
- [x] `probe.Run` y `Parse`, incluidos color y HDR en `media_streams`.
- [x] Detectar subtítulos externos junto al video (`pelicula.es.srt`) y
      guardarlos en `subtitles`.
- [x] Watcher con debounce de 30 s y espera a que el tamaño sea estable,
      para no analizar copias a medias.
- [x] Reconciliación periódica con `reconcile_interval_minutes`.
- [x] `purgeExpired` con `missing_file_retention_days`, como `Purge`.

### Pruebas

- [x] Desconectar el disco no marca ningún archivo ausente, tampoco si el
      punto de montaje queda vacío o una carpeta deja de poder leerse.
- [x] Renombrar un archivo conserva su `id` y su elemento, sin FFprobe.
      El progreso cuelga del elemento; su tabla llega en la Fase 6.
- [x] Un archivo borrado aparece como ausente y se purga tras la retención.
- [x] Interrumpir el escaneo y repetirlo no duplica filas.
- [x] Un archivo corrupto queda en `failed` y el escaneo continúa.
- [x] Dos bibliotecas pueden compartir ruta.
- [x] Un symlink que sale de la raíz se ignora. En Windows se prueba con
      una unión; el symlink solo se prueba en CI, por falta de privilegio.
- [x] Eliminar una biblioteca ya escaneada funciona, también a mitad de
      un escaneo.
- [x] Tabla de nombres reales para los parsers: 116 casos.

### Decisiones tomadas al implementar

- **Huella al indexar:** §7.3 decía que `file_hash` se calcula solo para
  los candidatos a movimiento. No funciona: cuando un archivo falta ya no
  se puede leer. Se calcula al indexar cada archivo nuevo o modificado,
  2 MiB por archivo y una sola vez. Un archivo nuevo con la huella de uno
  ausente se mueve sin pasar por FFprobe.
- **Dos fases por escaneo:** primero se recorren todas las raíces y se
  marcan las ausencias; después se analiza lo nuevo con varios
  trabajadores y se guarda por lotes de 200. Con un recorrido en
  streaming, un archivo movido entre dos raíces de la misma biblioteca
  aparecería antes de quedar ausente y se duplicaría. En memoria solo
  viven rutas, tamaños y fechas.
- **`match_key`:** agrupar por título normalizado se rompería en la
  Fase 5, cuando los metadatos cambien `title`. La clave local se guarda
  aparte y no cambia. Un índice único parcial impide duplicados.
- **`duration_ms` y `bitrate` en `media_files`:** son del archivo, y la
  decisión de reproducción de la Fase 7 los necesita por versión.
- **Punto de montaje vacío:** una raíz accesible pero vacía, con archivos
  conocidos, se trata como no disponible. Es lo que se ve cuando el disco
  no está montado. A cambio, borrar todo el contenido de una raíz no marca
  ausencias: hay que quitar la ruta.
- **Carpetas ilegibles:** sus archivos y subtítulos se conservan. Antes,
  un error de permisos los habría hecho pasar por borrados.
- **FFprobe ausente:** el escaneo falla con `SCAN_FAILED` sin marcar nada
  como `failed`. Si lo hiciera, esos archivos no se reintentarían. El
  servidor arranca igual, pero instalar FFprobe después exige reiniciar.
- **Prefijo `file:`:** FFprobe recibe `file:<ruta>`, para que un nombre
  como `concat:...` no se interprete como protocolo.
- **Carátulas:** una pista de video con `attached_pic` se guarda como
  `attachment`, para que nunca se elija para reproducir.
- **Idioma `und`** se guarda como nulo, porque significa desconocido.
- **Escaneo único:** además del control en memoria, un índice único
  parcial en `scan_jobs` impide dos escaneos en curso de una biblioteca.
  Al arrancar, los que quedaron en `running` pasan a `interrupted`.
- **`Exclusive`:** eliminar una biblioteca o quitar una ruta cancela su
  escaneo, espera a que termine e impide otro mientras dura la operación.
- **Quitar una ruta** borra sus archivos sin retención, y con ellos los
  elementos que se quedan vacíos. Es una decisión explícita del
  administrador.
- **Rutas:** absolutas, existentes, legibles, sin solaparse con otra de
  la misma biblioteca ni con los directorios del servidor. Dos
  bibliotecas sí pueden compartir una.
- **Permisos:** `manage` incluye `scan`, y `scan` incluye `view`. Quien
  no ve una biblioteca recibe 404; quien la ve pero no tiene el permiso,
  `PERMISSION_LIBRARY_DENIED`. Crear, eliminar y tocar rutas es solo de
  administradores; `manage` permite editar nombre y activación. Las rutas
  físicas solo se muestran a administradores. Owner y admin no admiten
  permisos explícitos. Se añade `GET /admin/users/{id}/permissions` para
  que el panel pueda mostrarlos.
- **Usuario restringido:** sin `max_age_rating` solo ve contenido para
  todos los públicos. Un episodio usa la clasificación de su temporada o
  su serie. Como `min_age` llega con los metadatos de la Fase 5, hasta
  entonces un restringido no ve ningún elemento.
- **Watcher:** vigila cada carpeta, porque fsnotify no es recursivo en
  todos los sistemas, y añade las que se crean. Si se agota el límite de
  vigilancias, deja de añadir y lo cubre la reconciliación. Si se
  desborda el búfer de eventos, programa el escaneo de todas las
  bibliotecas vigiladas.
- **Eventos:** `library.scan.started`, `library.scan.completed` y
  `library.updated` se emiten por una función del escáner que de momento
  solo registra. El bus llega en la Fase 6.
- **Fallo de `sqlc`:** un comentario con tildes en `queries/` desplaza
  los offsets y genera consultas truncadas, por ejemplo `LIMIT` sin su
  `1`. Las consultas no llevan comentarios con caracteres no ASCII, y una
  prueba comprueba que cada consulta generada termina como su fuente.

### Verificación por mutación

Se aplicaron 80 mutaciones en dos rondas, cada una contra los paquetes que
la cubren. Tras reescribir las que no compilaban, se detectan todas salvo
cinco: dos equivalentes, la desconexión del disco a mitad del recorrido,
que no se puede provocar en una prueba, y el symlink externo y el prefijo
`file:`, que solo se pueden probar en CI. El proceso destapó lo siguiente, ya corregido:

- **Límite de salida de FFprobe roto:** `limitedBuffer` incrustaba
  `bytes.Buffer`, cuyo `ReadFrom` quedaba promocionado. `io.Copy`, que
  usa `exec`, lo llamaba y se saltaba el límite de 16 MiB.
- **Prueba de `sqlc` engañosa:** buscaba la última línea de cada
  consulta en todo el código generado, y `LIMIT 1` de otra consulta
  ocultaba el truncado. Ahora compara cada consulta con su propia
  constante, y se comprobó truncando el archivo de verdad.
- **Código muerto:** la comprobación de carpeta ilegible al borrar
  subtítulos nunca decidía nada, porque un subtítulo comparte carpeta con
  su video. Se eliminó.
- **Huecos en las pruebas:** tamaño y fecha cambiados por separado, un año
  anterior al real dentro del título («Death Race 2000 (1975)»), un plazo
  de FFprobe vencido antes de arrancar y un usuario `view` que intenta
  editar.

**Criterio de salida:** escanear 10 000 archivos sintéticos con
consumo de memoria estable, y repetir el escaneo sin cambios en segundos
sin lanzar FFprobe.

**Estado:** cumplido en Windows. El reescaneo de 10 000 archivos tarda
0,53 s sin lanzar FFprobe, y el heap pasa de 1552 a 1568 KiB tras cuatro
escaneos más. El primer escaneo, con un FFprobe simulado, tarda 40 s,
casi todo abriendo archivos. Con el binario real y FFprobe real se probó
el flujo completo por API: crear bibliotecas, escanear, reescanear sin
análisis y detectar un movimiento con el vigilante a los 31 s. También
se migró la base real de la Fase 3 a la versión 5, con backup previo.

**Pendiente o sin verificar:**

- `-race`, Linux y macOS, solo en CI. Ahí se ejecutarán también las
  pruebas de symlink y la del prefijo `file:`, que en Windows no se
  pueden provocar.
- Los symlinks externos autorizados por configuración de §7.4 no existen
  todavía: se ignoran siempre.
- El watcher sobre SMB, NFS o discos extraíbles y el límite de inotify no
  se han probado; la reconciliación los cubre.
- HDR10+ solo se declara por fotograma, así que se guarda como HDR10.
- Eliminar una biblioteca debe cancelar sus sesiones de reproducción en
  la Fase 6.
- `GET /libraries/{id}/items` llega con el catálogo, en la Fase 5.

------------------------------------------------------------------------

## Fase 5 — Catálogo, metadatos y panel inicial

**Objetivo:** una biblioteca navegable y enriquecida, útil aunque no haya
Internet. Marca el **hito H1**.

### Endpoints

Del documento: `/libraries/{id}/items`, `/media/{id}`,
`/media/{id}/files`, `/media/{id}/streams`, `/media/{id}/subtitles`,
`/media/{id}/images/{type}`, `/search`, `/server/status`,
`/server/capabilities`.

También, para §8.3 y la navegación de series:

| Método | Ruta | Propósito |
|--------|------|-----------|
| GET | `/media/{id}/children` | Temporadas o episodios |
| GET | `/media/{id}/matches` | Buscar coincidencias |
| POST | `/media/{id}/identify` | Elegir proveedor e identificador |
| POST | `/media/{id}/refresh` | Forzar actualización |
| PATCH | `/media/{id}/metadata` | Editar y bloquear metadatos |

### Consultas de lectura

Viven en `internal/readmodel`. La paginación por cursor no degrada con
bibliotecas grandes. El permiso de la biblioteca se comprueba antes.
SQLC no admite `MATCH` sobre FTS5, por lo que `SearchItems` se escribe con
`database/sql` (ADR 0002).

```sql
-- name: ListLibraryItems :many
SELECT mi.id, mi.type, mi.title, mi.year, mi.sort_title
FROM media_items mi
WHERE mi.library_id = ?1
  AND mi.parent_id IS NULL
  AND (mi.sort_title, mi.id) > (?2, ?3)
ORDER BY mi.sort_title, mi.id
LIMIT ?4;

-- name: SearchItems :many
SELECT mi.id, mi.type, mi.title, mi.year
FROM media_search ms
JOIN media_items mi ON mi.id = ms.media_item_id
WHERE media_search MATCH ?1
  AND (?2 = 1 OR (l.enabled = 1 AND EXISTS (
    SELECT 1 FROM user_permissions up
    WHERE up.user_id = ?3
      AND up.library_id = mi.library_id
  )))
  AND (?4 = 0 OR COALESCE(mi.min_age, p.min_age, g.min_age, -1) BETWEEN 0 AND ?5)
ORDER BY bm25(media_search), mi.id
LIMIT ?6;
```

La versión anterior exigía `permission = 'view'`, y habría ocultado los
resultados a quien solo tiene `scan` o `manage`, que incluyen ver. Tampoco
filtraba las bibliotecas desactivadas ni la edad. La consulta real está en
`internal/readmodel/search.go`; `l`, `p` y `g` son la biblioteca, el padre
y el abuelo del elemento.

### Metadatos

```go
// internal/metadata
type Provider interface {
    Name() string
    Search(ctx, q Query) ([]Match, error)
    Details(ctx, externalID string, kind ItemKind) (*Details, error)
}

type Enricher struct{ /* ... */ }
func (e *Enricher) Start(ctx) error                    // worker con límite de peticiones
func (e *Enricher) Enqueue(itemIDs ...string)
func (e *Enricher) resumePending(ctx) error            // desde metadata_status
func (e *Enricher) apply(ctx, id string, d *Details) error // respeta metadata_locked

// internal/images
func (m *Manager) Fetch(ctx, itemID string, t ImageType, url string) error
func (m *Manager) Open(ctx, itemID string, t ImageType, w int) (*ImageFile, error)
func (m *Manager) Evict(ctx) error
```

Reglas:

- Antes de empezar, cerrar la validación de §26.4: proveedor, licencia,
  cuotas y uso de imágenes. El primer adaptador depende de ello.
- Si el proveedor falla, reintentar con espera creciente y dejar el
  elemento en `failed`, sin afectar al catálogo.
- `sort_title` se genera con `media.SortTitle(title, lang)`.
- `media_search` se actualiza en la misma transacción que el título.
- Las imágenes se guardan en `data/images/{item_id}/{type}.{ext}` y
  nunca se descargan al abrir un detalle.

### Panel inicial

Proyecto `web/` con Vite, React y TypeScript, servido desde `web/dist`
con `go:embed`.

- [x] Asistente de primer arranque: código inicial, Owner y biblioteca.
- [x] Pantallas Libraries, Users y Metadata básicas.
- [x] Progreso de escaneo, primero consultando REST.

### Tareas

- [x] Migración `00006_search.sql`.
- [x] Endpoints de lectura con ETag en imágenes.
- [x] Adaptador del proveedor elegido y `Enricher`.
- [x] Identificación manual completa de §8.3.
- [x] Encabezados `Cache-Control` para imágenes autenticadas: `private`.

### Pruebas

- Sin Internet, el catálogo se indexa y navega con títulos del archivo.
- Tras una caída, los `pending` se retoman.
- `metadata_locked` impide la sobrescritura.
- La búsqueda de `pelicula` encuentra `Película`.
- Un usuario no ve en listados ni búsquedas bibliotecas sin permiso, y no
  puede obtener imágenes por `id` directo.

**Criterio de salida:** catálogo de películas y series navegable desde el
panel, con usuarios limitados a sus bibliotecas.

### Decisiones tomadas al implementar

- **Proveedor:** TMDB, según el ADR 0005. El servidor no trae clave: cada
  instalación configura la suya. Sin clave, el catálogo usa los títulos de
  los archivos y se puede editar a mano.
- **Cola:** es la propia base. Son los elementos principales en `pending`
  y sin bloquear, así que sobrevive a los reinicios. Los estados son
  `pending`, `done`, `unmatched` y `failed`:
  - Un fallo pasajero deja el elemento en `pending` y pausa la cola de
    1 minuto a 1 hora. Cuentan como pasajeros la falta de red, un 5xx, un
    429 y una clave rechazada.
  - Sin coincidencia segura, el elemento pasa a `unmatched`.
  - Un 404 del proveedor o una respuesta inválida lo pasa a `failed`.
  - Los dos últimos solo se reintentan con «Forzar actualización».
- **Coincidencia automática:** con año local se acepta un resultado con el
  año a ±1 que sea el primero o tenga el mismo título; sin año, uno con el
  mismo título. Un título igual con otro año es una nueva versión y se
  rechaza.
- **Renovación a los 150 días:** TMDB prohíbe guardar su contenido más de
  6 meses. Lo que no está bloqueado vuelve a la cola, con sus imágenes.
- **Imágenes:** se descargan desde la cola, nunca al abrir un detalle, con
  un límite de 10 MiB por imagen y el tipo comprobado por el contenido. No
  se redimensionan, y el parámetro `w` de `images.Manager.Open` se quitó:
  redimensionar crearía derivados, que TMDB prohíbe. Una imagen que da 404
  se olvida; un 5xx detiene la tanda.
  - `Evict` borra lo que ninguna fila referencia, y además personas y
    géneros huérfanos.
  - No hay límite de tamaño de caché: borrar imágenes vigentes obligaría a
    descargarlas al abrir.
- **Búsqueda:** el índice se mantiene con disparadores, también en los
  borrados en cascada. `media_search_rows` evita recorrer el índice en
  cada borrado: borrar 10 000 elementos tarda 0,47 s.
  - El texto del usuario se reduce a letras y dígitos, y cada palabra va
    entre comillas y como prefijo. Así no puede inyectar sintaxis FTS5.
  - Se indexan películas, series y episodios, y un episodio se devuelve con
    el título de su serie.
- **Permisos de metadatos:** buscar coincidencias, identificar, actualizar
  y editar exigen `manage` sobre la biblioteca. Editar bloquea el elemento
  salvo que la petición diga `locked`, y desbloquear no cambia los datos.
  Identificar o actualizar un elemento bloqueado responde `METADATA_LOCKED`
  (409).
- **Clasificaciones:** se toma la de la región de `metadata.language` y, si
  falta, la de Estados Unidos. Se guarda como `ES:13`. PG y TV-PG cuentan
  como 10 años, para que un perfil infantil no los vea. Episodios y
  temporadas no guardan edad propia: heredan la de la serie.
- **`sort_title`:** se genera con `media.SortTitle`, sin artículo inicial,
  acentos ni mayúsculas. Los artículos ingleses se quitan siempre. La
  migración vacía los títulos antiguos y el servidor los rellena al
  arrancar, antes de atender peticiones.
- **API añadida:**
  - `?metadata_status=` en `/libraries/{id}/items`, para la pantalla
    Metadata.
  - `/server/status` exige sesión, y su detalle es solo para
    administradores.
  - `/server/capabilities` es pública.
- **Panel:** React sin enrutador externo, servido con CSP `default-src
  'self'`. Las rutas sin archivo devuelven `index.html` y los recursos con
  hash se cachean un año.
  - Sin `web/dist` compilado, el binario arranca sin panel.
  - `make build` compila el panel antes, y la CI lo incrusta en los
    binarios cruzados.
- **Corrección del plan:** la consulta `SearchItems` original exigía
  `permission = 'view'` (ver arriba).

### Verificación por mutación

Se aplicaron 52 mutaciones distintas en tres rondas, cada una contra los
paquetes que la cubren. Se detectan todas salvo una equivalente: quitar
`filepath.IsLocal` en `images.Manager.Open`, porque `os.OpenRoot` ya impide
salir del directorio. El proceso destapó estos huecos en las pruebas, ya
cubiertos:

- **Pausa:** ninguna prueba comprobaba que, con el proveedor caído, un
  aviso no adelantara el reintento. Si fallara, cada escaneo terminado
  volvería a consultar al proveedor.
- **Identificar un elemento bloqueado:** el candado de la base seguía
  respondiendo `METADATA_LOCKED`, pero ninguna prueba comprobaba que no
  se consultara al proveedor.
- **Clasificación de cines:** el fixture solo tenía una clasificación no
  vacía, así que preferir el estreno en cines no se comprobaba.
- **Conexión caída al descargar una imagen:** se trataba como pasajera,
  pero ninguna prueba lo provocaba.
- **`Start`:** no se comprobaba que rellenara los títulos ordenables antes
  de servir.

La consulta de las condiciones de TMDB destapó además un fallo de diseño:
metadatos e imágenes no caducaban nunca, y TMDB prohíbe guardarlos más de
seis meses. Se añadió la renovación a los 150 días.

Borrar una biblioteca grande se midió: con 10 000 elementos tarda 0,47 s,
gracias a `media_search_rows`.

**Estado:** cumplido en Windows.

- **Panel con el binario real y FFprobe real**, desde una instalación
  nueva:
  - Asistente de primer arranque con el código del log, y dos bibliotecas.
  - Escaneo con progreso, catálogo por título ordenable y detalle con
    pistas.
  - Edición y bloqueo de metadatos.
  - Usuario restringido de 13 años con permiso solo sobre Películas.
  - Búsqueda sin acentos y pantalla Metadata.
- **Lo que ve ese usuario**, comprobado por la API: solo la película
  clasificada para 13 años; 404 en el resto y en Series.
- **Migración:** la base de la Fase 4 pasó a la versión 6 con su backup
  previo.

**Pendiente o sin verificar:**

- **TMDB real:** ninguna prueba ha llegado al servicio. El adaptador se
  probó con respuestas escritas según la forma documentada de la API, no
  capturadas; capturarlas exige una clave.
- **Enriquecimiento con el binario real:** la descarga y el servicio de
  imágenes solo se probaron en pruebas automáticas, porque sin clave no
  hay proveedor.
- **Logotipo de TMDB:** sus condiciones lo exigen, pero no se ha
  descargado el oficial. El panel solo muestra el aviso de texto.
- **Renovación de sesión:** al probarla en la Fase 6 resultó que no
  funcionaba. Se corrigió y se comprobó en el navegador tras caducar la
  cookie.
- **Lectores de pantalla y Safari:** no se han probado.
- **Contexto:** `-race`, Linux y macOS siguen solo en CI.
- **Eventos:** `library.updated` del Enricher solo se registra; el bus
  llega en la Fase 6.
- **Usuarios con `manage`:** el panel muestra la gestión de metadatos
  solo a administradores. Un usuario con `manage` puede usar la API, pero
  no lo ve en el panel.

------------------------------------------------------------------------

## Fase 6 — Sesiones y progreso

**Objetivo:** el ciclo de vida del que dependen la entrega y la
transcodificación. Por eso va antes que ambas.

### Máquina de estados

```go
// internal/sessions
var transitions = map[Status][]Status{
    Created:      {Initializing, Failed, Expired},
    Initializing: {Playing, Stopping, Failed, Expired},
    Playing:      {Paused, Stopping, Failed, Expired},
    Paused:       {Playing, Stopping, Failed, Expired},
    Stopping:     {Completed, Stopped, Failed},
}

func (s *Session) Transition(to Status) error // ErrInvalidTransition
```

### Funciones

```go
type Manager struct{ /* registro en memoria + flush a SQLite */ }

func (m *Manager) Create(ctx, p CreateParams) (*Session, error)
func (m *Manager) Heartbeat(ctx, id string, hb Heartbeat) error
func (m *Manager) Touch(id string)                 // segmento o lectura por rango
func (m *Manager) Stop(ctx, id string) error
func (m *Manager) StreamToken(id string) string
func (m *Manager) VerifyToken(id, token string) (*Session, error)

func (m *Manager) flushLoop(ctx)                   // cada progress_persist_interval_seconds
func (m *Manager) sweepLoop(ctx)                   // expira por inactividad
func (m *Manager) Reconcile(ctx) error             // §12.5, antes de aceptar peticiones
func (m *Manager) Shutdown(ctx) error              // apagado controlado: vuelca todo

type Listener interface { OnSessionEnded(s *Session) } // libera trabajos y cupos
```

**Token de streaming:** `base64url(HMAC-SHA256(secreto, session_id))`, con
un secreto en `server_settings`. No requiere una columna, se verifica en
tiempo constante y muere con la sesión, porque `VerifyToken` también
comprueba que esté activa.

**Escrituras:** las posiciones viven en memoria y se vuelcan cada 30 s, o
al instante con `paused`, `stopped`, `completed` o `expired`. `Touch` solo
modifica memoria, de modo que la autorización por segmento no toca disco.

### Progreso

```sql
-- name: UpsertProgress :exec
INSERT INTO playback_progress
    (user_id, media_item_id, position_ms, duration_ms, completed, updated_at)
VALUES (?1, ?2, ?3, ?4, ?5, ?6)
ON CONFLICT (user_id, media_item_id) DO UPDATE SET
    position_ms = excluded.position_ms,
    duration_ms = excluded.duration_ms,
    completed = excluded.completed,
    updated_at = excluded.updated_at
WHERE excluded.updated_at >= playback_progress.updated_at;

-- name: ListContinueWatching :many
SELECT mi.id, mi.type, mi.title, mi.parent_id,
       pp.position_ms, pp.duration_ms, pp.updated_at
FROM playback_progress pp
JOIN media_items mi ON mi.id = pp.media_item_id
WHERE pp.user_id = ?1
  AND pp.completed = 0
  AND pp.position_ms > 0
  AND (?2 = 1 OR (l.enabled = 1 AND EXISTS (
    SELECT 1 FROM user_permissions up
    WHERE up.user_id = pp.user_id
      AND up.library_id = mi.library_id
  )))
  -- Más el filtro de edad del usuario restringido, como en la búsqueda.
ORDER BY pp.updated_at DESC
LIMIT ?3;
```

Igual que en la búsqueda de la Fase 5, la versión anterior exigía
`permission = 'view'` y no filtraba bibliotecas desactivadas ni edades. La
consulta real está en `queries/playback.sql`.

- `updated_at` es la hora de recepción. En `PUT /playback/progress` se
  acepta la hora del cliente si no es futura.
- Un elemento se marca completado al superar el 90 % (configurable).
- `playback_history` se escribe una vez, al cerrar la sesión o al
  completar. Las sesiones DLNA no lo escriben.
- Reproducir un archivo con varios episodios guarda el progreso en el
  elemento desde el que se abrió.

### Eventos

Bus interno y WebSocket, porque aquí aparecen los primeros eventos de
sesión.

```go
// internal/api/websocket
type Hub struct{ /* ... */ }
func (h *Hub) Publish(e Event)
func (h *Hub) Subscribe(ctx, actor Actor) <-chan Event
func canReceive(actor Actor, e Event) bool // filtro de §17
```

### Tareas

- [x] Migración `00007_playback.sql`.
- [x] `sessions.Manager` con pruebas de todas las transiciones.
- [x] `PATCH` y `DELETE /playback/sessions/{id}`, y
      `GET /playback/sessions/{id}`.
- [x] `PUT /playback/progress`, `GET /playback/continue-watching` y
      `GET /playback/history`.
- [x] `/api/v1/events`, con validación de `Origin`.
- [x] Ejecutar `Reconcile` en `lifecycle` antes de abrir el puerto, y
      borrar `transcodes/` sin sesión activa.

`POST /playback/sessions` llega en la Fase 7, cuando exista la decisión.
Hasta entonces, las pruebas crean sesiones con `Manager.Create`.

### Pruebas

- Toda transición fuera de la tabla se rechaza.
- Una sesión expirada no actualiza progreso.
- Un evento atrasado no sobrescribe el progreso reciente.
- Tras un `kill -9` simulado, `Reconcile` deja cero sesiones activas.
- El apagado controlado vuelca el progreso en memoria.
- El WebSocket no entrega eventos de otro usuario.

**Criterio de salida:** ciclo completo de sesión probado con un cliente
simulado, incluyendo caída y reanudación con `SESSION_EXPIRED`.

### Decisiones tomadas al implementar

- **Transiciones añadidas:**
  - `initializing → paused`: un navegador sin reproducción automática
    empieza en pausa.
  - `created → stopping`: cerrar una sesión recién creada.
  - `stopping → expired`: el documento permite expirar desde cualquier
    estado no terminal.

  Repetir el estado actual no es una transición. El cliente solo puede
  pedir `playing` o `paused`. `Manager.Create` deja la sesión en
  `initializing`, que es lo que devolverá `POST /playback/sessions`.
- **Columnas nuevas en `playback_sessions`:** `start_position_ms`,
  `position_ms` y `duration_ms`, más el índice `idx_sessions_open`. Así una
  sesión terminada se sigue consultando con su posición, y la
  reconciliación cierra cada sesión con la última posición volcada.
- **Escrituras:**
  - Al cambiar de estado se guarda al momento; al pausar o terminar, se
    vuelca también la posición.
  - El resto se vuelca cada `progress_persist_interval_seconds`, con la
    hora del heartbeat y no la del volcado.
  - Una sesión sin heartbeats no escribe progreso ni historial.
  - El historial se escribe una vez, al cerrar.
  - Un mutex ordena las escrituras, para que un volcado nunca se
    intercale con el cierre de la misma sesión.
- **Fin de una sesión:**
  - Cerrar lleva a `completed` si se superó
    `completed_threshold_percent` (90 por defecto, entre 50 y 100) y a
    `stopped` si no.
  - Cerrar una sesión ya terminada responde 204.
  - Un heartbeat a una terminada responde 410 `SESSION_EXPIRED`.
  - `GET` sigue devolviendo la sesión terminada, para retomar desde su
    posición.
  - Las sesiones solo las ve y controla su usuario; las ajenas dan 404.
- **Elemento borrado durante la reproducción:** si un escaneo purga el
  elemento, la fila de la sesión se borra en cascada. El siguiente volcado
  lo detecta y cierra la sesión como `failed`, sin escribir nada.
- **Cierre por §6.3:**
  - Eliminar o desactivar una biblioteca cierra sus sesiones.
  - Eliminar un usuario, desactivarlo o cambiarle la contraseña cierra las
    suyas.
  - Revocar un dispositivo cierra las suyas y emite `device.revoked`.
  - Los servicios reciben una interfaz pequeña y no dependen del paquete
    de sesiones.
- **Apagado:** las sesiones activas se cierran como `expired` con el
  progreso volcado. `Reconcile` cierra las abiertas de la ejecución
  anterior, marca los trabajos activos como `failed` con `SERVER_RESTART` y
  vacía `transcodes/`. Las sesiones arrancan justo después de la base,
  antes que el escáner y el servidor HTTP.
- **Token de streaming:** HMAC-SHA256 del identificador, con el secreto
  `sessions.stream_secret` en `server_settings`. Un token falso responde
  404; uno válido de una sesión terminada, `SESSION_EXPIRED`.
- **Eventos:**
  - **Bus y conexión:** el bus vive en `internal/events`, no en
    `api/websocket`, porque lo usan sesiones, escáner, Enricher y
    dispositivos. Cada conexión filtra en su propio bucle con
    `events.CanReceive`, y un suscriptor que no lee se descarta.
  - **WebSocket:** usa `github.com/coder/websocket` v1.8.15, la versión que
    ya traía goose. Valida `Origin` y revalida la sesión cada minuto.
  - **Eventos emitidos:** `session.created`, `session.updated`,
    `session.ended`, `playback.progress`, `library.scan.started`,
    `library.scan.completed`, `library.updated` y `device.revoked`.
- **Fallo de `sqlc`:** con `BETWEEN` y un parámetro, sqlc numera el
  parámetro pero no lo pone en la estructura generada, y los valores se
  enlazarían descolocados. La prueba de consultas lo prohíbe.
- **Panel:** al revisar la Fase 5 con el binario real apareció un fallo.
  La cookie de acceso caduca a la vez que su token, el navegador la borra
  y el servidor responde `AUTH_REQUIRED`, no `AUTH_TOKEN_EXPIRED`. El panel
  ahora renueva la sesión en ambos casos, salvo en las rutas que crean o
  cierran sesión.

### Verificación por mutación

Se aplicaron 43 mutaciones distintas en dos rondas. Se detectan todas salvo
cuatro:

- **Tres equivalentes:**
  - Quitar la comprobación de la tabla en el heartbeat: con solo `playing`
    y `paused`, todas las transiciones posibles ya están permitidas.
  - Escribir historial en DLNA: esas sesiones nunca reciben heartbeats.
  - Quitar `ended_at IS NULL` al cerrar: una sesión cerrada ya no está en
    memoria.
- **Una solo de ruta de error:** ignorar un fallo de escritura al
  reconciliar. Provocarlo exige inyectar un fallo en la base.

Huecos que destapó, ya cubiertos:

- **Estados del cliente:** ninguna prueba enviaba `failed`, `stopping` ni
  `expired`. La tabla los permite desde `paused`, así que si el filtro
  fallara, la sesión quedaría a medio terminar.
- **Sesiones de usuarios:** ninguna prueba desactivaba, cambiaba la
  contraseña ni eliminaba a un usuario con sesiones abiertas.

Al probar con el binario real apareció además el fallo de renovación de
sesión del panel descrito arriba.

**Estado:** cumplido en Windows. Una prueba con un cliente simulado recorre
el ciclo completo por la API:

1. Reproduce y pausa.
2. Sufre una caída sin apagado, simulada con un segundo servidor sobre la
   misma base.
3. Recibe `SESSION_EXPIRED` y lee la última posición volcada.
4. Retoma en una sesión nueva y la termina, con dos entradas de historial.

Con el binario real:

- La base de la Fase 5 migró a la versión 7.
- Tras un `kill -9` con una sesión abierta, un trabajo activo y restos en
  `transcodes/`, el arranque los reconcilió antes de abrir el puerto.
- El WebSocket recibió en el navegador, con la cookie del panel, los
  eventos de un escaneo.

**Pendiente o sin verificar:**

- `POST /playback/sessions` llega en la Fase 7. Hasta entonces las
  sesiones solo se crean desde las pruebas, así que ningún cliente real ha
  recorrido el ciclo con el binario.
- Un elemento sin duración conocida nunca se marca como completado y se
  queda en «Continuar viendo».
- Retirar el permiso sobre una biblioteca no cierra las sesiones ya
  abiertas en ella. La Fase 7 debe comprobar la autorización al verificar
  el token de streaming.
- `/admin/sessions` no existe todavía.
- `-race`, Linux y macOS siguen solo en CI.

------------------------------------------------------------------------

## Fase 7 — Decisión de reproducción y Direct Play

**Objetivo:** el Playback Manager y la primera reproducción real.

### Decisión como función pura

Sin base de datos ni red, para probar con una tabla de casos.

```go
// internal/playback
type Input struct {
    File      MediaFileInfo
    Streams   []Stream
    Subtitles []Track
    Audio     *Track
    Subtitle  *Track
    Client    ClientProfile
    Limits    Limits       // max_bitrate, max_resolution
}

type Decision struct {
    Mode        Mode        // direct_play, direct_stream, transcode
    Protocol    Protocol    // http, hls
    VideoAction Action      // copy, transcode
    AudioAction Action
    Subtitle    SubtitlePlan // none, sidecar, hls, convert, burn_in
    Profile     *OutputProfile
    Reasons     []Reason    // motivos de la transcodificación
}

func Decide(in Input) (Decision, error)

func resolveSubtitle(in Input) SubtitlePlan
func videoCompatible(s Stream, c ClientProfile) (bool, Reason)
func audioCompatible(s Stream, c ClientProfile) (bool, Reason)
func containerCompatible(f MediaFileInfo, c ClientProfile) bool
func withinLimits(in Input) (bool, Reason)
```

Orden de `Decide`:

1. `resolveSubtitle`. Si es `burn_in`, `VideoAction = transcode`.
2. `withinLimits`. Si no cumple, video a `transcode`.
3. Evaluar video y audio por separado.
4. Si todo es compatible, el contenedor también, y los subtítulos no
   exigen HLS: `direct_play`.
5. Si no hay nada que recodificar: `direct_stream`.
6. En otro caso: `transcode`, solo de lo incompatible.

### Orquestación

```go
func (s *Service) CreateSession(ctx, actor Actor, req Request) (*Response, error)
func (s *Service) selectFile(ctx, itemID string, fileID *string, c ClientProfile) (*MediaFile, error)
func (s *Service) selectTracks(ctx, f *MediaFile, req Request, prefs Preferences) (Tracks, error)
func (c ClientProfile) Validate() error
func PresetFor(platform string) ClientProfile
```

`CreateSession` sigue los pasos de §9.4. El perfil del cliente se valida
y se acota con `PresetFor`, porque §9.2 pide no darlo por garantizado.
`ClientProfile` y los tipos compartidos del protocolo viven en
`internal/protocol`.

### Entrega del archivo

```go
// internal/api/handlers/stream.go
func (h *Stream) File(w http.ResponseWriter, r *http.Request)
```

- `http.ServeContent` resuelve GET, HEAD, `Range`, `206` y
  `Content-Length`. Solo hay que abrir el archivo con `storage.OpenMedia`.
- Un `io.ReadSeeker` envuelve el archivo y llama a `sessions.Touch`.
- Si el cliente se desconecta, la escritura falla y el handler termina.
- `Content-Type` sale de la tabla propia de MIME, no de la extensión que
  envíe el cliente.
- Las rutas se montan fuera de `/api/v1` y autentican con `VerifyToken`.

### Tareas

- [x] `Decide` con al menos 40 casos en tabla.
- [x] `POST /playback/sessions`.
- [x] `selectFile`, que prefiere la versión compatible de mayor calidad.
- [x] Handler `/stream/session/{id}/{token}/file`.
- [x] Registrar decisión y motivos en logs.
- [x] Devolver `Reasons` en la respuesta.

### Pruebas

- MP4/H.264/AAC en cliente compatible da `direct_play`.
- MKV/H.264/AAC en cliente sin MKV da `direct_stream`.
- Audio DTS en cliente sin DTS da `transcode` con video en `copy`.
- PGS seleccionado da video en `transcode`, aunque sea compatible.
- SRT externo en AVPlayer da `direct_stream` por HLS.
- Rango inicial, medio, final e inválido en el endpoint de archivo.
- Una pista de otro archivo se rechaza.
- Un token válido de otra sesión se rechaza.

**Criterio de salida:** una película se reproduce en un `<video>` de
prueba con salto libre, usando una URL que no sirve tras cerrar la sesión.

### Decisiones tomadas al implementar

- **Sin FFmpeg todavía:** remux, transcodificación, HLS y quemado de
  subtítulos llegan en la Fase 8. Mientras tanto, `POST /playback/sessions`
  responde 422 `PLAYBACK_UNSUPPORTED`, con el modo y los motivos en
  `details`, cuando la decisión los necesita. Así no se devuelve una URL
  que no funciona. `Decide` ya calcula todos los modos.
- **`embedded` en `subtitle_mode`:** en Direct Play el archivo llega entero
  con sus pistas, y un reproductor que sabe dibujar el formato interno lo
  hace él mismo. Si el modo no es Direct Play, `Decide` recalcula el
  subtítulo sin esa opción, porque la pista interna habría que extraerla.
- **SRT externo a WebVTT:** se adelanta a esta fase porque `<video>` solo
  acepta WebVTT y no necesita FFmpeg. Un archivo que no es UTF-8 se lee
  como Windows-1252. Extraer pistas internas y convertir ASS sigue en la
  Fase 9 y, mientras, responde `PLAYBACK_UNSUPPORTED`. Las rutas de
  subtítulos solo sirven externos del archivo de la sesión.
- **Perfil del cliente:**
  - Lo no declarado sale del perfil de la plataforma; lo declarado manda,
    aunque sea una lista vacía.
  - Los nombres desconocidos se descartan y los campos desconocidos se
    rechazan.
  - Los perfiles predefinidos son estimaciones prudentes pendientes de
    validar (§26.6).
  - El perfil no se cruza con el predefinido: la plataforma no sabe más
    que el propio cliente, que puede consultar `canPlayType`.
  - Los nombres de FFprobe se traducen: `subrip` → `srt`, `pcm_*` → `pcm`,
    `hdmv_pgs_subtitle` → `pgs`.
- **Comprobaciones de compatibilidad:** códec, profundidad de bits, HDR,
  canales de audio, resolución máxima (del perfil y de la petición),
  bitrate, contenedor y protocolo.
- **Elección de versión:** gana el mejor modo y, a igualdad, la de más
  píxeles y luego más bitrate. Las versiones ausentes o con análisis
  fallido no cuentan. Sin ninguna, `MEDIA_FILE_UNAVAILABLE` (409).
- **Pistas:**
  - Una pista de otro archivo responde `VALIDATION_ERROR`.
  - `selected_subtitle_id: "none"` desactiva los subtítulos.
  - Sin pistas pedidas, mandan las preferencias del usuario.
  - Los idiomas se comparan sin distinguir 639-1, 639-2 ni región.
- **Entrega:**
  - `http.ServeContent` resuelve GET, HEAD, `Range` y `416`.
  - Un lector envuelve el archivo y registra actividad cada 5 s.
  - El tipo sale de una tabla por contenedor, con `Cache-Control:
    private, no-store`.
- **Reautorización:** cada petición de streaming comprueba que el usuario
  sigue activo, con el dispositivo sin revocar y permiso sobre el
  elemento. Se recuerda 15 s (`playback.Service.AuthTTL`). Si falla, la
  sesión se cierra y la URL responde `SESSION_EXPIRED`. Esto cierra el
  pendiente de la Fase 6: retirar un permiso corta la reproducción.
- **`preferred_protocol`:** se acepta y no se usa hasta que exista HLS.
- **Reproductor de prueba en el panel:** reproduce con `<video>`, envía
  heartbeats cada 10 s y al pausar o reanudar, y cierra la sesión al
  salir, también al cerrar la pestaña con `keepalive`.

### Verificación por mutación

Se aplicaron 40 mutaciones distintas en dos rondas, y se detectan todas.
La primera ronda destapó cinco huecos en las pruebas, ya cubiertos:

- **Subtítulo interno no admitido:** un subtítulo interno en un cliente
  que acepta pistas embebidas pero no ese formato (VobSub en Android).
- **`selected_subtitle_id: "none"`:** la prueba descartaba el error.
- **Usuario desactivado sin pasar por la API:** la reautorización del
  streaming no se probaba en ese caso.
- **Archivo desaparecido:** no se probaba con la sesión abierta.
- **Subtítulo de otro archivo:** no se probaba pedirlo con un token
  válido.

**Estado:** cumplido en Windows con el binario real:

1. Desde el botón Reproducir del panel, una película MP4 de 2 minutos se
   reprodujo en `<video>` y el salto a los 90 s cargó por rangos desde ahí.
2. Los heartbeats registraron la posición.
3. Al cerrar, la sesión quedó `completed` y la misma URL respondió 410
   `SESSION_EXPIRED`.
4. El mismo MKV dio 422 con `container_unsupported` en el navegador, y
   Direct Play en el perfil de Android.
5. La decisión queda registrada en el log.

**Pendiente o sin verificar:**

- Remux, transcodificación, HLS y quemado de subtítulos: Fase 8.
- Extraer subtítulos internos y convertir ASS: Fase 9.
- Los perfiles predefinidos no se han probado en ningún dispositivo real
  salvo Chrome (el navegador integrado). En particular, falta comprobar
  `embedded` y `sidecar` en Media3, Tizen y webOS.
- La frecuencia de imagen, el entrelazado y el perfil y nivel de H.264 o
  HEVC no se comprueban. Un H.264 High 4:4:4 de 8 bits pasaría por
  compatible.
- Un archivo con varios episodios usa la duración del archivo para
  decidir si se completó.
- `-race`, Linux y macOS siguen solo en CI.

------------------------------------------------------------------------

## Fase 8 — Direct Stream, transcodificación y HLS

**Objetivo:** reproducir cualquier archivo, con seek, sin saturar el
equipo. Es la fase de mayor riesgo técnico y depende de V3 y V4.

### Componentes

```go
// internal/ffmpeg
type Args struct{ /* constructor tipado, nunca cadenas de shell */ }
func BuildHLS(p HLSParams) ([]string, error)
func Probe(ctx) (*Capabilities, error)            // versión, códecs y filtros
func TestEncoder(ctx, enc Encoder) error          // inicialización real, §10.5

// internal/transcoding
type Manager struct{ /* ... */ }
func (m *Manager) Start(ctx, s *Session, d Decision, startMs int64) (*Job, error)
func (m *Manager) Restart(ctx, sessionID string, startMs int64) (*Job, error)
func (m *Manager) StopSession(ctx, sessionID string) error
func (m *Manager) OnSessionEnded(s *Session)      // libera cupo y limpia

type Admission struct{ /* ... */ }
func (a *Admission) Reserve(kind JobKind, sessionID string) error // TRANSCODE_CAPACITY_EXCEEDED
func (a *Admission) Release(sessionID string)                     // idempotente

// internal/transcoding/hls.go
func BuildMaster(s *Session, d Decision) []byte
func BuildMediaPlaylist(durationMs int64, segDur time.Duration) []byte
func BuildCopyPlaylist(keyframes []int64) []byte
func (m *Manager) Segment(ctx, sessionID string, path string) (*os.File, error)

// internal/transcoding/gc.go
func (m *Manager) gcLoop(ctx)                     // sesiones terminadas y cuota
```

### Playlist sintética

Con `seg = 6 s`, el número de segmentos es
`ceil(duracion / seg)`. El manifiesto se genera sin esperar a FFmpeg:

```text
#EXTM3U
#EXT-X-VERSION:7
#EXT-X-TARGETDURATION:6
#EXT-X-PLAYLIST-TYPE:VOD
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-MAP:URI="init.mp4"
#EXTINF:6.000000,
seg_00000.m4s
#EXTINF:6.000000,
seg_00001.m4s
...
#EXT-X-ENDLIST
```

### Comando base

Probado con `libx264` y FFmpeg 9.0.2, incluidos reinicios en hls.js
(ADR 0003). El resto de reproductores y codificadores sigue pendiente.

```text
ffmpeg -hide_banner -nostdin -progress pipe:1
  -ss <start> -i <archivo>
  -map 0:<video> -map 0:<audio>
  -c:v libx264 -preset veryfast -b:v <bitrate> -maxrate <max> -bufsize <buf>
  -force_key_frames expr:gte(t,n_forced*6)
  -c:a aac -ac 2 -b:a 192k
  -output_ts_offset <start>
  -f hls -hls_time 6 -hls_playlist_type event
  -hls_segment_type fmp4 -hls_fmp4_init_filename init.mp4
  -hls_flags temp_file+independent_segments
  -start_number <n> -hls_segment_filename seg_%05d.m4s
  ffmpeg.m3u8
```

- `<n> = floor(start / seg)` y `<start> = n * seg`, para que cada
  segmento coincida con la playlist sintética.
- `-output_ts_offset` conserva la línea de tiempo del archivo. Sin esta
  opción, un reinicio en el segundo 60 empieza en 0. `-copyts` queda
  descartado porque no produjo paquetes legibles.
- `init.mp4` cambia en cada reinicio. La playlist declara el del primer
  trabajo; cada trabajo debe escribir el suyo con otro nombre.
- `temp_file` evita servir un segmento a medio escribir.
- `ffmpeg.m3u8` no se entrega: lo que recibe el cliente es la playlist
  sintética.

### Lógica de `Segment`

```text
Pedido seg_N:
  ¿Existe en disco?                     -> Entregar
  ¿No está activo o N > actual + 5?     -> Restart desde N
  ¿N < inicio del trabajo activo?       -> Restart desde N
  En otro caso                          -> Esperar hasta 20 s
                                           Si no aparece: error 503
```

Los valores 6 y 5 son los defaults de `transcoding.segment_seconds` y
`transcoding.seek_restart_segments`.

`Restart` para el proceso con `cmd.Cancel`, espera con `cmd.WaitDelay`,
marca el trabajo como `stopped` y crea otro. El índice único
`idx_transcode_one_active` garantiza un solo activo. El cupo se mantiene
porque pertenece a la sesión.

### Procesos

- `exec.CommandContext` con lista de argumentos.
- En Linux, `SysProcAttr.Pdeathsig`. En Windows, un Job Object con
  `KILL_ON_JOB_CLOSE`. En macOS, supervisión desde un proceso auxiliar.
- Prioridad reducida con `nice` o `SetPriorityClass`.
- `-progress pipe:1` alimenta `transcode.progress`.
- El `stderr` se conserva en un búfer circular para diagnosticar.

### Aceleración por hardware

`ffmpeg.Probe` detecta los encoders y `TestEncoder` genera un segundo de
video real con `lavfi`. El resultado se guarda en memoria y se muestra en
`/server/capabilities`. Si falla al iniciar, se reintenta una vez en CPU
y se notifica el modo efectivo. Nunca hay un bucle de reintentos.

### Direct Stream

`BuildHLS` con `-c:v copy -c:a copy`. La playlist sale de
`BuildCopyPlaylist`, con un índice de keyframes guardado en
`data/cache/keyframes/{file_id}.json`. El índice se obtiene leyendo
paquetes con FFprobe, en segundo plano, porque en un archivo grande sobre
red puede tardar minutos (ADR 0004). Mientras no exista, se usa una
playlist `EVENT` y el seek queda limitado a lo ya generado.

### Tareas

- [x] `internal/ffmpeg`, con pruebas de argumentos sin invocar FFmpeg.
- [x] `Admission` y su reserva en `CreateSession` antes de crear sesión.
- [x] `Manager` (`Prepare`, `File`, reinicios en `waitSegment`,
      `OnSessionEnded`) y supervisión del proceso: Job Object en Windows y
      `Pdeathsig` en Linux. **macOS no tiene supervisión** (§12.5).
- [x] Playlists y handler de `/stream/session/{id}/{token}/{path}`, que
      solo acepta nombres de una lista permitida.
- [x] Detección de aceleración para NVENC, QSV, VAAPI, AMF y
      VideoToolbox. Se detectan y se prueban con `TestEncoder`, pero **solo
      `libx264` se ha ejecutado de verdad**: ningún codificador por hardware
      está verificado.
- [x] Recolector y cuota de `data/transcodes` (`transcoding.max_disk_gb`).
- [x] `/admin/transcodes`, `/admin/transcodes/{id}/cancel` y eventos
      (`transcode.started`, `transcode.progress`, `transcode.failed`). El
      panel todavía no tiene pantalla para ellos.
- [x] Reproductor del panel con hls.js (carga diferida) y perfil
      `protocols: ["http", "hls"]`.
- [x] Benchmarks y primer default de `max_concurrent_jobs` (ver más abajo).

### Pruebas

- [x] Saltar hacia adelante reinicia FFmpeg y reproduce en menos de 5 s
      (`TestRecodificarConSaltos`; en Edge con hls.js, salto de 2 s a 80 s).
- [x] Saltar a una zona ya generada no reinicia.
- [x] 20 saltos seguidos no dejan procesos huérfanos
      (`TestVeinteSaltosSinHuerfanos`).
- [x] La posición informada coincide con la del archivo tras cada salto.
- [x] Sin cupo, se responde 503 con `Retry-After` y no se crea sesión.
- [~] Un cambio de calidad no pierde el cupo: el cupo es de la sesión y
      `Reserve` es idempotente (probado), pero el API no expone todavía el
      cambio de calidad en una sesión viva.
- [x] Matar FFmpeg termina en `transcode.failed`, con diagnóstico.
- [~] Matar el servidor no deja FFmpeg vivo: **verificado en Windows**
      (`taskkill /F` con FFmpeg en marcha, 0 procesos después). `Pdeathsig`
      en Linux no tiene prueba propia; macOS no tiene mecanismo.
- [x] Rutas con `../` en el segmento se rechazan.
- [x] Un nombre de archivo con caracteres especiales no altera el comando
      (`file:` delante de la entrada, nombres de salida validados).

### Resultado de la Fase 8

**Verificación con el binario real** (`medianode-server`, FFmpeg 9.0.2,
fixtures de `testdata/fixtures`):

- 27 comprobaciones de extremo a extremo (sesión, master, playlists, init,
  segmentos, `ffprobe` de lo servido, salto, 503, cierre de sesiones y
  muerte del servidor).
- hls.js 1.6.15 en Edge sin interfaz: Direct Stream de 120 s con salto a
  60 s y vuelta; recodificación a 240p con salto a 80 s y vuelta; clips con
  AC3 y HEVC. Solo se vio un aviso no fatal, `bufferSeekOverHole`.
- El navegador integrado del escritorio de Claude rechaza las URL `blob:`
  de MSE, así que no sirve para probar hls.js; las pruebas se hicieron con
  Edge.

**Defectos que solo apareció al probar de verdad:**

1. El primer archivo sin índice de keyframes devolvía una playlist `EVENT`
   vacía, que los reproductores rechazan. Ahora la sesión espera al índice
   hasta 8 s y `Media()` espera al primer segmento (ADR 0004).
2. `safeName(".")` se aceptaba como nombre de salida.

**Pruebas de mutación:** 28 mutantes del código nuevo (admisión, playlists,
índice, argumentos de FFmpeg, `hlsContainers`, espera del índice). Mueren 26;
sobreviven 2. El mutante de `Indexer.Wait` que devuelve `nil` al agotar el
plazo es equivalente (solo omite una última lectura). El que quita la
condición «hay segmentos» de `Media()` solo se nota si FFmpeg tarda más en
terminar que en escribir el primer segmento, y con fixtures pequeños el
trabajo acaba antes.

**Benchmark** (Intel i5-2400, 4 núcleos, libx264 `veryfast`, origen 4K de
2 s, sin audio; es una medición corta y de una sola máquina):

| Salida | 1 trabajo | 2 a la vez | 4 a la vez |
|--------|-----------|------------|------------|
| 1080p | 1,46× tiempo real | 0,77× | 0,34× |
| 720p | 1,36× | 0,99× | 0,52× |
| 480p | 1,82× | 1,04× | 0,59× |

Copiar el vídeo cuesta muy poco: 120 s en 0,14 s.

**Default:** `max_concurrent_jobs: 1` y `max_concurrent_light_jobs: 4` se
mantienen. En un equipo de esta clase, dos recodificaciones simultáneas a
1080p ya no llegan a tiempo real. Quien tenga más núcleos o codificador por
hardware puede subirlo.

**Sin verificar:** Safari (HLS nativo), AVPlayer y Media3; codificadores por
hardware; `Pdeathsig` en Linux; supervisión en macOS; archivos de varios GB
sobre red.

**Criterio de salida:** cada combinación de contenedor y códec de §23.3
se reproduce en hls.js, Safari y Media3, con seek libre y sin fugas de
procesos ni de disco.

**Estado del criterio:** cumplido para hls.js en Chromium con las
combinaciones de los fixtures (H.264 con AAC y AC3, y HEVC; DTS y 4K no se probaron). **No
cumplido** para Safari y Media3, que no se han podido probar.

------------------------------------------------------------------------

## Fase 9 — Subtítulos, audio y HDR

**Objetivo:** cerrar la reproducción. Marca el **hito H2**.

### Funciones

```go
// internal/subtitles
func (m *Manager) Tracks(ctx, fileID string) ([]Track, error)
func (m *Manager) ToWebVTT(ctx, t Track) (string, error)      // con caché
func (m *Manager) Extract(ctx, t Track) (string, error)       // fuera de la petición
func ConvertSRT(r io.Reader, w io.Writer) error
func IsImageBased(codec string) bool                          // pgs, dvd_subtitle
func BurnFilter(t Track, f FileInfo) (string, error)

// internal/playback
func needsToneMapping(s Stream, c ClientProfile) bool
```

### Reglas

| Origen | Cliente | Estrategia |
|--------|---------|------------|
| SRT o WebVTT | `sidecar` | Archivo independiente en WebVTT |
| SRT o WebVTT | Solo `hls` | Pista WebVTT dentro de HLS |
| ASS/SSA | Renderiza ASS | Entregar original |
| ASS/SSA | No renderiza ASS | Convertir a WebVTT o burn-in, según política |
| PGS o VobSub | Cualquiera | Burn-in |

- Caché: `data/cache/subtitles/{file_id}/{track_id}.vtt`, invalidada por
  `mtime` y tamaño.
- `Extract` corre en segundo plano. Si aún no terminó, la sesión empieza
  sin subtítulos y el cliente recibe el aviso al estar lista la pista.
- `ToWebVTT` limita tamaño y líneas, y neutraliza el marcado no admitido.

### HDR

- `needsToneMapping` cubre HDR10, HLG y Dolby Vision con salida SDR.
- Filtros por orden de preferencia: hardware, `libplacebo`, `zscale` con
  `tonemap`.
- Si ninguno está disponible, aplicar la política de §10.8.
- Burn-in y tone mapping comparten la cadena `-vf` o `-filter_complex`,
  por lo que `BuildHLS` debe componerlos en un orden fijo: primero tone
  mapping, después subtítulos.

### Audio

- Mezcla a estéreo cuando el cliente solo admite dos canales.
- Copia cuando el códec y los canales son compatibles.
- Cambio de pista con `Restart`, conservando la posición.

### Tareas

- [ ] Selección por idioma, forzados y `user_preferences`.
- [ ] `/stream/.../subtitles/{id}.vtt`, por sesión.
- [ ] Pista WebVTT dentro de HLS, con su media playlist.
- [ ] Burn-in para texto e imagen.
- [ ] Tone mapping y comprobación de filtros en `System Check`.
- [ ] Completar la matriz §23.3 con PGS, VobSub y HDR.

### Pruebas

- Subtítulos forzados se aplican sin elección del usuario.
- Un SRT mal codificado (Latin-1) se convierte a UTF-8.
- La extracción no retrasa el inicio de la reproducción.
- Un HDR10 transcodificado a SDR conserva color correcto, por comparación
  visual y por metadatos de color de la salida.

**Criterio de salida:** toda la matriz §23.3 pasa en un cliente de
prueba, con audio y subtítulos cambiables durante la reproducción.

------------------------------------------------------------------------

## Fase 10 — Cliente web y empaquetado inicial

**Objetivo:** cumplir los 14 criterios del MVP. Marca el **hito H3**.

### Cliente web

Estructura por capas de §18.2, dentro de `web/`:

```text
web/src/
├── app/            rutas y proveedores
├── features/       home, library, detail, player, search, admin
├── domain/         tipos y reglas de cliente
├── api/            cliente generado desde OpenAPI
└── platform/       player, websocket y almacenamiento
```

- [ ] Generar el cliente TypeScript desde `docs/openapi.yaml`.
- [ ] Sesión con cookie, refresco y protección CSRF.
- [ ] Inicio, Continuar viendo, Películas, Series, Búsqueda y Detalle.
- [ ] Selección de versión, audio y subtítulos.
- [ ] Reproductor: hls.js o `<video>` nativo, heartbeat, pausa, seek y
      reanudación tras `SESSION_EXPIRED`.
- [ ] Perfil de capacidades con `canPlayType` y `MediaCapabilities`.
- [ ] Panel: Dashboard, Sessions, Transcoding, Hardware, Network, Storage,
      Logs y Settings.
- [ ] `internal/admin`, con los endpoints `/admin/system`,
      `/admin/sessions`, `/admin/logs`, `/admin/storage` y
      `/admin/settings`.
- [ ] `GET /admin/system-check`, con las comprobaciones de §19.1.
- [ ] `internal/monitoring`: exponer en el panel las métricas de §22.2.

### Empaquetado inicial

- [ ] `Dockerfile` multietapa con imagen mínima y FFmpeg incluido.
- [ ] `docker-compose.yml` de ejemplo, con red `host` para el descubrimiento.
- [ ] Binarios para Windows, Linux y macOS, en `amd64` y `arm64`.
- [ ] Unidad systemd de ejemplo, con usuario sin privilegios.
- [ ] Auditar la licencia de la build de FFmpeg que se distribuya.

### Comprobación del MVP

| # | Criterio de §23.5 | Fase |
|---|-------------------|------|
| 1 | Instalación sin herramientas de desarrollo | 10 |
| 2 | Configuración inicial desde navegador | 5 |
| 3 | Creación y escaneo de bibliotecas | 4 |
| 4 | Extracción técnica con FFprobe | 4 |
| 5 | Catálogo de películas y series | 5 |
| 6 | Login y permisos | 3 |
| 7 | Direct Play | 7 |
| 8 | Direct Stream | 8 |
| 9 | Transcodificación de un perfil | 8 |
| 10 | Selección de audio y subtítulos | 9 |
| 11 | Guardado y recuperación de progreso | 6 |
| 12 | Dos clientes con sesiones independientes | 8 |
| 13 | Reinicio sin pérdida de catálogo ni progreso | 6 |
| 14 | Logs útiles para diagnosticar | 1 y 8 |

### Pruebas

- Prueba completa en máquina limpia con Docker y con binario.
- Chrome, Firefox, Edge y Safari.
- Dos sesiones simultáneas, una con Direct Play y otra con transcodificación.
- Reinicio durante una reproducción, con reanudación desde el progreso.

**Criterio de salida:** los 14 criterios pasan en una instalación limpia.

------------------------------------------------------------------------

## Fase 11 — Clientes móviles

**Objetivo:** Android e iOS, con descubrimiento y vinculación.

### Servidor

```go
// internal/discovery
func (d *Discovery) Start(ctx) error  // anuncia _medianode._tcp.local.
```

- [ ] TXT solo con nombre, puerto, identificador público y protocolo.
- [ ] Generar y persistir el identificador público del servidor.
- [ ] Cerrar V5 y generar el certificado propio para LAN.

### Cliente React Native

- [ ] Proyecto TypeScript, reutilizando `domain` y `api` del web mediante
      un paquete compartido.
- [ ] Reproductor nativo con Media3 y AVPlayer, tras una interfaz común
      `NativePlayer`.
- [ ] Descubrimiento mDNS, entrada manual y código QR.
- [ ] Almacenamiento seguro: Keystore en Android y Keychain en iOS.
- [ ] Perfil de capacidades desde las API de cada plataforma.
- [ ] `subtitle_delivery: ["sidecar", "hls"]` en Android y `["hls"]` en iOS.
- [ ] Reanudación al volver de segundo plano.
- [ ] Permisos de iOS: red local y cifrado de transporte (ATS).

### Pruebas

- Vincular por código, QR y dirección manual.
- Pasar de Wi-Fi a datos durante la reproducción.
- Teléfono físico en cada sistema para formatos y HDR.
- Continuar en otro dispositivo donde se dejó.

**Criterio de salida:** ambas aplicaciones completan el flujo de ver una
película en dispositivos reales.

------------------------------------------------------------------------

## Fase 12 — Clientes de TV

**Objetivo:** experiencia de control remoto de §18.4.

**Decisión previa:** §18.1 deja abierto si Android TV es adaptado o
nativo. La recomendación es usar React Native con soporte de TV, para
compartir código con la Fase 11, y revisar la decisión si el foco o el
rendimiento no alcanzan.

- [ ] **Android TV:** navegación D-pad, Media3 y gestión de `back`.
- [ ] **tvOS:** cliente nativo con AVKit, sobre la misma API.
- [ ] **Tizen y webOS:** validar SDK y versiones (§26.6), y empaquetar el
      cliente web adaptado con foco y control remoto.
- [ ] Vinculación por código, sin teclado en pantalla.
- [ ] Perfiles predefinidos por modelo en `PresetFor`.
- [ ] Matriz de modelos probados, publicada en la documentación.

**Criterio de salida:** navegación completa sin pantalla táctil, con
Direct Play de 4K HDR en al menos un dispositivo real de cada plataforma.

------------------------------------------------------------------------

## Fase 13 — DLNA y acceso remoto

**Objetivo:** compatibilidad ampliada. Solo depende del MVP, por lo que
puede avanzar en paralelo a las Fases 11 y 12.

### DLNA

```go
// internal/dlna
func (s *Server) Start(ctx) error
func (s *Server) ssdpLoop(ctx)
func (s *Server) DeviceDescription(w, r)
func (s *Server) ContentDirectoryBrowse(ctx, req BrowseRequest) (*DIDLLite, error)
func (s *Server) ConnectionManager(w, r)
func (s *Server) ServeMedia(w, r)
```

- [ ] SSDP, descripción XML y servicios SOAP.
- [ ] Respuestas DIDL-Lite con MIME y `protocolInfo` por perfil.
- [ ] `libraries.dlna_enabled` y advertencia en el panel.
- [ ] Rechazar peticiones que no provengan de la red local.
- [ ] Sesiones `client_kind = 'dlna'` con expiración por inactividad.
- [ ] Direct Play y transcodificación a MPEG-TS con burn-in.
- [ ] Matriz de renderers probados.

### Remote Direct

```go
// internal/remote
func (r *Remote) Classify(req *http.Request) Origin      // local o remoto, §14.4
func (r *Remote) RequireLocalAdmin(next http.Handler) http.Handler
func (r *Remote) CheckReachability(ctx) (*Report, error)
```

- [ ] Rutas HTTPS con certificado propio del usuario o reverse proxy.
- [ ] `trusted_proxies` y clasificación local o remota de §14.4.
- [ ] `remote.admin_enabled` aplicado a `/admin/*` y al panel.
- [ ] Limitar bitrate remoto según `max_bitrate_remote`.
- [ ] Verificación de conectividad desde fuera de la LAN.
- [ ] Prueba de seguridad: fuerza bruta, CORS y rutas de administración.

### Remote Relay

Solo si existe demanda demostrada, tras cerrar §26.5.

- [ ] Definir protocolo, con TLS extremo a extremo entre cliente y
      servidor. El relay no debe poder leer el contenido.
- [ ] Diseñar `identity` y `relay` como servicios independientes.

**Criterio de salida:** DLNA en tres renderers de fabricantes distintos y
acceso remoto cifrado desde fuera de la LAN, con administración bloqueada
por defecto.

------------------------------------------------------------------------

## Fase 14 — Producto completo

**Objetivo:** versión preparada para distribuir. Marca el **hito H6**.

### Distribución

- [ ] Instaladores MSI, DEB, RPM y DMG o PKG firmados.
- [ ] Windows Service con cuenta configurable y detección de unidades
      mapeadas en `System Check`.
- [ ] LaunchDaemon en macOS.
- [ ] Desinstalación que conserva bibliotecas y datos.
- [ ] Canales estable y de pruebas, con notas de versión.
- [ ] Probar la actualización desde cada versión anterior publicada.

### Backup y restauración

- [ ] Panel Backups con `/admin/backups` (GET, POST) y
      `/admin/backups/{id}/restore`.
- [ ] Copias programadas y retención, según §26.9.
- [ ] Restauración con validación de versión y reinicio controlado.

### Funciones de producto

- [ ] Rol `restricted`: filtros por `users.max_age_rating` y
      `media_items.min_age` en `readmodel`. Las columnas ya existen desde
      las migraciones `00002` y `00004`; el adaptador de metadatos debe
      rellenar `min_age` desde la Fase 5.
- [ ] Perfiles familiares, modelados como usuarios vinculados a un Owner.
- [ ] Historial avanzado y recomendaciones locales.
- [ ] Descargas offline, si se aprueba el alcance.
- [ ] Documentación de usuario y de administración.
- [ ] Revisión final de licencias (§21.4).

**Criterio de salida:** instalar, actualizar, restaurar y desinstalar sin
pérdida de datos en cada sistema soportado.

------------------------------------------------------------------------

## 3. Riesgos del plan

| Riesgo | Fase | Señal temprana | Respuesta |
|--------|------|----------------|-----------|
| La playlist sintética falla en un reproductor | 0, 8 | V3 no pasa en AVPlayer | Usar `.ts` en esa plataforma |
| Seek con video copiado inviable en archivos grandes | 0, 8 | V4 tarda minutos | Limitar el seek o transcodificar |
| `modernc` lento en bibliotecas grandes | 0, 4 | V1 por debajo de lo aceptable | Pasar a CGO |
| Proveedor de metadatos con licencia incompatible | 5 | §26.4 sin cerrar | Usar otro adaptador o solo datos locales |
| Coste de probar dispositivos de TV | 12 | Falta de hardware | Limitar la lista oficial de modelos |
| Fases 11 a 13 en paralelo con un equipo pequeño | 11-13 | Avance disperso | Terminar móvil antes de abrir TV |

## 4. Cambios aplicados al documento maestro

Preparar este plan reveló huecos que se corrigieron en la versión 1.2:

| Cambio | Motivo |
|--------|--------|
| `user_preferences` en §6 | §11.2 usaba preferencias sin tabla |
| `internal/readmodel` en el árbol de §5 | Aloja las consultas de §5.1 |
| Endpoints de usuarios, rutas, identificación, `system-check` y backups en §16.2 | Eran necesarios para funciones ya descritas |
| `segment_seconds` y `seek_restart_segments` en §20.2 | Parámetros de §10.7 |
| `users.max_age_rating` y `media_items.min_age` en §6 y §15.1 | Restricciones de contenido comparables entre países |

La Fase 3 llevó el documento a la versión 1.3:

| Cambio | Motivo |
|--------|--------|
| Columnas nuevas en `pairing_codes` y `/auth/pairing/token` | Los tokens no llegaban a la TV |
| `PUT /users/me/password` | Un usuario no podía cambiar su contraseña |
| Estado consultado en cada petición, en §15.2 | Revocar y desactivar deben ser inmediatos |
| `cors_origins`, `shutdown_timeout_seconds` y `logging.level` en §20.2 | Parámetros ya implementados |
| Códigos genéricos y de autenticación en §22.3 | Errores que ya produce el servidor |

La Fase 4 llevó el documento a la versión 1.4:

| Cambio | Motivo |
|--------|--------|
| `file_hash` al indexar, en §7.3 | Un archivo ausente ya no se puede leer |
| Carpetas ilegibles y montajes vacíos en §7.3 | No deben marcar ausencias |
| `media_items.match_key` e índice único | Los metadatos cambian `title` |
| `media_files.duration_ms` y `bitrate` | Cada versión puede diferir |
| `scan_jobs.trigger` e `idx_scan_one_running` | Origen del escaneo y uno solo en curso |
| Política de symlinks y uniones en §7.4 | Faltaba concretarla |
| Restringido sin límite y herencia de edad en §15.1 | Faltaba el caso |
| `GET /admin/users/{id}/permissions` en §16.2 | El panel necesita leerlos |

La Fase 5 llevó el documento a la versión 1.5:

| Cambio | Motivo |
|--------|--------|
| `media_search_rows` y disparadores en §6.2 | Sincronizar en la misma transacción sin recorrer el índice al borrar |
| Caducidad de imágenes y estados de metadatos en §8.2 | «Tamaño y expiración» era incompatible con no descargar al abrir |
| Reglas de identificación manual en §8.3 | Permisos, bloqueo y proveedor ausente |
| Normalización de edades de TMDB en §15.1 | Faltaba el respaldo y el caso PG |
| Sección `metadata` en §20.2 | Parámetros ya implementados |
| `METADATA_PROVIDER_UNAVAILABLE` y `METADATA_LOCKED` en §22.3 | Errores que ya produce el servidor |
| §26.4 cerrado con el ADR 0005 | Licencia, caché de 6 meses y atribución de TMDB |

La Fase 6 llevó el documento a la versión 1.6:

| Cambio | Motivo |
|--------|--------|
| `start_position_ms`, `position_ms` y `duration_ms` en `playback_sessions` | Retomar tras `SESSION_EXPIRED` y reconciliar con la última posición |
| Transiciones `initializing → paused` y `created → stopping` en §12.1 | Navegadores sin reproducción automática y cierre temprano |
| Hora del dato, reproducción simultánea e historial en §12.3 | Un dato atrasado no debe pisar uno reciente |
| Historial al reconciliar y apagado en §12.5 | Faltaba decidirlo |
| Autenticación, `Origin`, revalidación y clientes lentos en §17 | Faltaba concretarlo |
| `completed_threshold_percent` en §20.2 | Umbral configurable de §12.3 |

La Fase 7 llevó el documento a la versión 1.7:

| Cambio | Motivo |
|--------|--------|
| `subtitle_mode` con `embedded` en §9.1 | En Direct Play el reproductor dibuja la pista interna |
| Perfiles predefinidos y campos declarados en §9.2 | Concretar cómo se valida el perfil |
| Elección de versión en §9.3 | Faltaba el criterio |
| Pistas por preferencias, `none` y `device_id` en §9.4 | Faltaba concretarlo |
| Campos añadidos a la respuesta en §9.5 | `reasons`, `subtitle_url` y `media_file_id` |
| Reautorización por petición en §16.4 | Un token válido no basta si se retira el permiso |
| SRT externo a WebVTT en §11.2 | `<video>` solo acepta WebVTT |
