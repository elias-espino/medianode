# MediaNode

## Documento maestro de arquitectura y diseño técnico

**Versión:** 1.4\
**Estado:** Arquitectura objetivo propuesta\
**Fecha:** 3 de noviembre de 2026\
**Producto:** Servidor multimedia autoalojado y ecosistema
multiplataforma

------------------------------------------------------------------------

## Control del documento

  -----------------------------------------------------------------------
  Campo                               Definición
  ----------------------------------- -----------------------------------
  Nombre                              MediaNode --- Technical Design
                                      Document

  Versión                             1.9

  Estado                              Propuesta técnica de referencia
                                      para iniciar el desarrollo

  Arquitectura                        Monolito modular con procesos
                                      auxiliares

  Backend                             Go

  Persistencia                        SQLite en modo WAL

  Motor multimedia                    FFmpeg / FFprobe

  Clientes                            Web, Android, iOS, Android TV,
                                      tvOS, Tizen, webOS y escritorio

  Acceso                              Red local y remoto

  Objetivo                            Producto autoalojado con
                                      capacidades comparables a
                                      Plex/Jellyfin
  -----------------------------------------------------------------------

> **Convención:** este documento define la arquitectura objetivo
> recomendada. Las decisiones marcadas como «por validar» requieren
> pruebas de compatibilidad, rendimiento, licencias o experiencia de
> usuario antes de considerarse cerradas. Los ejemplos de API y SQL son
> contratos de diseño; no representan código ya implementado.

# 1. Visión y objetivos del producto

MediaNode es una plataforma multimedia autoalojada que permite al
usuario administrar bibliotecas de archivos locales o montados, obtener
metadatos, organizar películas y series, reproducir contenido en
distintos dispositivos y acceder a sus bibliotecas desde la red local o
desde Internet.

El producto se compone de:

1.  **MediaNode Server:** servicio central que indexa, organiza, protege
    y entrega contenido.
2.  **Clientes MediaNode:** aplicaciones web, móviles, televisores y
    escritorio.
3.  **Motor de reproducción:** lógica que determina Direct Play, Direct
    Stream o transcodificación.
4.  **Motor DLNA/UPnP:** compatibilidad con reproductores de red que
    implementen los perfiles correspondientes.
5.  **Capa de acceso remoto:** conexión segura directa y, opcionalmente,
    relay.
6.  **Panel de administración:** configuración, supervisión,
    bibliotecas, usuarios, dispositivos y sesiones.

## 1.1 Objetivos funcionales

-   Indexar bibliotecas de películas, series, episodios y, en fases
    posteriores, música y fotografías.
-   Mantener una base de datos de medios independiente de la ubicación
    visual del catálogo.
-   Extraer información técnica mediante FFprobe.
-   Asociar metadatos, imágenes, géneros, personas, temporadas y
    episodios.
-   Reproducir archivos originales cuando el cliente sea compatible.
-   Remultiplexar streams cuando solo sea necesario cambiar el
    contenedor.
-   Transcodificar audio, video o ambos cuando sea necesario.
-   Entregar subtítulos como pistas independientes o integrarlos en la
    imagen cuando corresponda.
-   Mantener progreso, historial y sesiones por usuario y dispositivo.
-   Descubrir servidores en la red local.
-   Permitir acceso remoto autenticado y cifrado.
-   Administrar usuarios, permisos y dispositivos.
-   Instalarse en Windows, Linux y macOS.
-   Ofrecer clientes para Android, iOS, Android TV, tvOS, web y, sujeto
    a validación, Tizen y webOS.

## 1.2 Objetivos no funcionales

-   **Autonomía:** las funciones esenciales deben operar sin depender de
    servicios externos.
-   **Seguridad:** ningún cliente debe poder leer rutas arbitrarias del
    sistema de archivos.
-   **Portabilidad:** el servidor debe compilarse y empaquetarse para
    los sistemas soportados.
-   **Observabilidad:** logs estructurados, métricas y diagnóstico de
    procesos.
-   **Recuperación:** manejo de fallos de escaneo, transcodificación,
    clientes desconectados y apagados.
-   **Rendimiento:** preferir entrega directa y limitar trabajos de
    CPU/GPU.
-   **Mantenibilidad:** módulos internos con contratos explícitos y
    migraciones versionadas.
-   **Compatibilidad:** capacidades declaradas por cliente y verificadas
    mediante pruebas reales.

## 1.3 Fuera del alcance inicial

No se considera requisito del MVP:

-   Sincronización distribuida entre varios servidores.
-   Clúster de servidores o base de datos compartida.
-   Transcodificación distribuida.
-   Tienda de contenido o distribución de medios.
-   Servicios externos obligatorios para autenticación o reproducción.
-   Aplicaciones nativas completas para todas las marcas de Smart TV
    desde la primera versión.
-   Descargas offline con DRM.

# 2. Principios de arquitectura

1.  **Monolito modular:** un ejecutable principal Go con módulos
    separados por responsabilidad.
2.  **Procesos aislados para multimedia:** FFmpeg/FFprobe se ejecutan
    como procesos externos controlados; no se integran como lógica
    dentro del proceso HTTP.
3.  **API estable y versionada:** los clientes consumen contratos
    `/api/v1`.
4.  **Clientes delgados:** la lógica de permisos y decisiones de
    reproducción pertenece al servidor; la decodificación pertenece al
    reproductor nativo.
5.  **Entrega directa primero:** evitar transcodificar si el cliente
    puede reproducir el contenido.
6.  **Una base local por servidor:** SQLite reside en almacenamiento
    local, no en NFS/SMB.
7.  **Servicios externos opcionales:** metadatos, relay y
    actualizaciones no pueden ser dependencias del funcionamiento local.
8.  **Seguridad por defecto:** autorización por biblioteca, tokens de
    alcance limitado, TLS obligatorio en conexiones remotas y disponible
    en LAN, y validación estricta de rutas.
9.  **Evolución gradual:** no introducir microservicios, Redis o un
    message broker hasta que exista una necesidad medible.

# 3. Arquitectura lógica

``` text
┌──────────────────────────────────────────────────────────────┐
│                    CLIENTES MEDIANODE                         │
│ Web | Android | iOS | Android TV | tvOS | Desktop | Smart TV  │
│                 DLNA Renderers / Players                      │
└──────────────────────────────┬───────────────────────────────┘
                               │
                 LAN / Remote Direct / Relay
                               │
┌──────────────────────────────▼───────────────────────────────┐
│                    CONNECTION LAYER                           │
│ Discovery | TLS | Pairing | Rate Limit | Routing              │
└──────────────────────────────┬───────────────────────────────┘
                               │
┌──────────────────────────────▼───────────────────────────────┐
│                    MEDIANODE SERVER CORE                      │
│ Go | REST API | WebSocket | Authentication | Authorization    │
│                                                              │
│ Library | Scanner | Metadata | Playback | Sessions | Devices  │
│ Subtitles | DLNA | Remote Access | Monitoring | Admin         │
└─────────────────────┬──────────────────────┬─────────────────┘
                      │                      │
          ┌───────────▼──────────┐  ┌────────▼───────────────┐
          │ STREAMING ENGINE     │  │ DLNA / UPnP ENGINE     │
          │ Direct Play          │  │ SSDP                   │
          │ Direct Stream        │  │ Content Directory      │
          │ FFmpeg / HLS         │  │ Connection Manager     │
          └───────────┬──────────┘  └────────┬───────────────┘
                      │                      │
          ┌───────────▼──────────────────────▼─────────────────┐
          │ STORAGE                                             │
          │ SQLite | Metadata Cache | Images | Transcode Files  │
          │ Original Media Files (local disk / mounted storage) │
          └────────────────────────────────────────────────────┘
```

## 3.1 Componentes de alto nivel

  -----------------------------------------------------------------------
  Componente                          Responsabilidad
  ----------------------------------- -----------------------------------
  Server Core                         Inicio, configuración, ciclo de
                                      vida y apagado controlado

  API Gateway                         Rutas HTTP, middleware, validación
                                      y serialización

  Authentication                      Login, tokens, renovación y
                                      revocación

  Authorization                       Permisos por usuario, biblioteca y
                                      operación

  Library Manager                     Definición de bibliotecas y rutas

  Scanner                             Descubrimiento e indexación de
                                      archivos

  Media Probe                         Extracción técnica mediante FFprobe

  Metadata Manager                    Identificación y metadatos
                                      descriptivos

  Image Manager                       Posters, fondos, thumbnails y caché

  Playback Manager                    Compatibilidad y selección del modo

  Transcoding Manager                 Procesos FFmpeg, colas, límites y
                                      limpieza

  Subtitle Manager                    Pistas internas/externas, selección
                                      y entrega

  Session Manager                     Sesiones activas, heartbeat y
                                      progreso

  Device Manager                      Dispositivos, perfiles y
                                      capacidades

  Discovery Manager                   mDNS y descubrimiento de servidores

  DLNA Manager                        Servidor UPnP MediaServer

  Remote Access Manager               Configuración de conexión remota

  Monitoring                          Logs, métricas y diagnósticos

  Admin Manager                       Configuración y panel
                                      administrativo
  -----------------------------------------------------------------------

# 4. Stack tecnológico

  -----------------------------------------------------------------------
  Capa                    Tecnología propuesta    Observaciones
  ----------------------- ----------------------- -----------------------
  Backend                 Go                      Servicio principal

  HTTP Router             Chi                     Router ligero; Gin es
                                                  alternativa, no ambos

  API                     REST/JSON               Versionada

  Eventos                 WebSocket               Eventos de sesión y
                                                  tareas

  Base de datos           SQLite WAL              Archivo local

  Driver SQLite           modernc.org/sqlite      Go puro, sin CGO, para
                                                  simplificar la
                                                  compilación cruzada;
                                                  rendimiento por validar

  Búsqueda                SQLite FTS5             Sin distinguir
                                                  mayúsculas ni acentos

  Acceso SQL              SQLC                    Consultas tipadas

  Migraciones             Goose                   Migraciones SQL
                                                  versionadas

  Multimedia              FFmpeg / FFprobe        Procesos externos

  Streaming               HLS                     fMP4/CMAF cuando la
                                                  compatibilidad lo
                                                  permita

  Descubrimiento cliente  mDNS/DNS-SD             Servicio MediaNode

  DLNA                    SSDP/UPnP               Servicio independiente
                                                  dentro del servidor

  Logs                    Zerolog                 Logs JSON estructurados

  Cliente móvil           React Native +          Android/iOS
                          TypeScript              

  Android/Android TV      AndroidX Media3         Reproductor nativo
  Player                                          

  iOS/tvOS Player         AVPlayer/AVKit          Reproductor nativo

  Web                     React + TypeScript      Cliente web

  Web playback            HLS.js o HLS nativo     Según navegador

  Desktop                 Cliente web empaquetado Validar integración de
                          con Tauri, opcional     red local

  Instalación             MSI/EXE, DEB/RPM,       Distribución por
                          DMG/PKG, Docker         plataforma
  -----------------------------------------------------------------------

**Nota de selección:** Chi y Gin son alternativas para el router; la
implementación debe elegir uno. La recomendación base de este documento
es Chi. SQLC genera código a partir de consultas SQL y evita introducir
un ORM pesado.

# 5. Estructura del repositorio

``` text
medianode/
├── cmd/
│   ├── medianode-server/
│   │   └── main.go
│   └── medianode-cli/
│       └── main.go
│
├── internal/
│   ├── core/
│   ├── config/
│   ├── lifecycle/
│   ├── database/
│   ├── api/
│   │   ├── handlers/
│   │   ├── middleware/
│   │   ├── routes/
│   │   ├── dto/
│   │   └── websocket/
│   ├── auth/
│   ├── authorization/
│   ├── users/
│   ├── devices/
│   ├── libraries/
│   ├── scanner/
│   ├── probe/
│   ├── metadata/
│   ├── images/
│   ├── playback/
│   ├── transcoding/
│   ├── subtitles/
│   ├── sessions/
│   ├── discovery/
│   ├── dlna/
│   ├── remote/
│   ├── storage/
│   ├── monitoring/
│   ├── admin/
│   ├── media/
│   ├── readmodel/
│   ├── protocol/
│   └── ffmpeg/
│
├── migrations/
├── queries/
├── web/
│   └── dist/
├── resources/
│   ├── images/
│   └── fonts/
├── build/
├── packaging/
├── scripts/
├── docs/
├── go.mod
└── go.sum
```

## 5.1 Reglas de organización

-   `cmd` contiene los puntos de entrada.
-   `internal` contiene la implementación privada del servidor.
-   No se crea `pkg` hasta que exista un consumidor externo real.
-   `migrations` contiene migraciones SQL ordenadas.
-   `queries` contiene SQL fuente para SQLC.
-   `web/dist` contiene el build del panel web cuando se distribuya
    integrado.
-   `packaging` contiene definiciones de instaladores y servicios.
-   Cada tabla tiene un módulo propietario y solo ese módulo escribe en
    ella; los demás deben utilizar interfaces o servicios definidos.
-   Las consultas de lectura que combinan varios módulos, como listados
    filtrados por permisos o «Continuar viendo», se implementan como
    consultas SQL con JOIN en `internal/readmodel`. No deben resolverse
    con N+1 ni filtrando en memoria, porque rompería la paginación.
-   Los handlers HTTP no deben contener lógica de negocio.
-   Las llamadas a FFmpeg deben pasar por Transcoding Manager.
-   Ninguna ruta proporcionada por un cliente debe convertirse
    directamente en una ruta local sin autorización y validación.

# 6. Modelo de dominio

## 6.1 Entidades principales

### User

Representa una cuenta local del servidor.

Campos: - `id` - `username` - `password_hash` - `role` -
`max_age_rating` nullable - `is_active` - `created_at` - `updated_at` -
`last_login_at`

### UserPreferences

Preferencias de reproducción de un usuario. Se aplican cuando la
solicitud no indica pistas concretas.

Campos: - `user_id` - `audio_language` - `subtitle_language` -
`subtitle_mode` - `max_bitrate_remote`

### Device

Representa un cliente vinculado.

Campos: - `id` - `user_id` - `device_name` - `platform` -
`client_version` - `device_identifier` - `capabilities_json` -
`created_at` - `last_seen_at` - `revoked_at`

### Library

Representa una biblioteca lógica: películas, series, música o
fotografías.

Campos: - `id` - `name` - `type` - `enabled` - `dlna_enabled` -
`created_at` - `updated_at`

### LibraryPath

Una biblioteca puede contener varias rutas.

Campos: - `id` - `library_id` - `path` - `is_available` - `last_scan_at`

### MediaItem

Representa una entidad de catálogo, no necesariamente un único archivo.

Campos: - `id` - `library_id` - `type` - `title` - `original_title` -
`sort_title` - `year` - `release_date` - `overview` - `age_rating` -
`min_age` - `original_language` - `duration_ms` - `parent_id` - `season_number` -
`episode_number` - `metadata_status` - `metadata_locked` - `created_at`
- `updated_at`

La jerarquía de series es `series` → `season` → `episode`, mediante
`parent_id`. La aplicación debe validar el tipo de relación. Los
identificadores externos, géneros, personas e imágenes se guardan en
tablas propias.

### MediaFileItem

Relaciona archivos y entidades de catálogo. Un elemento puede tener
varios archivos (versiones) y un archivo puede contener varios episodios
(`S01E01-E02`). Un archivo sin esta relación todavía no está
identificado. Un archivo con varios episodios se reproduce completo.

Campos: - `media_file_id` - `media_item_id` - `part_order`

### MediaFile

Representa un archivo físico.

Campos: - `id` - `library_path_id` - `relative_path` - `file_size` -
`modified_at` - `file_hash` (opcional) - `container` - `probe_status` -
`missing_since` - `created_at` - `updated_at`

La ruta se guarda relativa a su `LibraryPath`. Así, cambiar un punto de
montaje solo modifica `LibraryPath.path`.

### MediaStream

Representa una pista detectada dentro de un archivo.

Campos: - `id` - `media_file_id` - `stream_index` - `stream_type` -
`codec` - `profile` - `level` - `language` - `channels` - `width` -
`height` - `frame_rate` - `bitrate` - `bit_depth` - `color_transfer` -
`color_primaries` - `hdr_format` - `is_default` - `is_forced` -
`stream_metadata_json`

### Subtitle

Representa un archivo de subtítulos externo. Las pistas internas se
guardan únicamente en `MediaStream` con `stream_type = 'subtitle'`. La
API expone ambas como pistas con un UUID único.

Campos: - `id` - `media_file_id` - `relative_path` - `language` -
`format` - `is_default` - `is_forced`

### PlaybackProgress

Progreso individual por usuario y elemento.

Campos: - `user_id` - `media_item_id` - `position_ms` - `duration_ms` -
`completed` - `updated_at`

### PlaybackSession

Sesión activa o recientemente cerrada.

Campos: - `id` - `client_kind` - `user_id` nullable - `device_id`
nullable - `media_item_id` - `media_file_id` - `selected_audio_stream_id`
nullable - `selected_subtitle_id` nullable - `playback_mode` - `status` -
`started_at` - `last_heartbeat_at` - `last_activity_at` - `ended_at` -
`client_ip`

`user_id` y `device_id` solo pueden ser nulos si `client_kind = 'dlna'`.

### PlaybackHistory

Registro permanente de lo reproducido. Es independiente de las sesiones
operativas y conserva el título aunque el elemento se elimine.

Campos: - `id` - `user_id` - `device_id` nullable - `media_item_id`
nullable - `title` - `position_ms` - `completed` - `watched_at`

### TranscodeJob

Proceso FFmpeg asociado a una sesión. Una sesión puede tener varios
trabajos a lo largo del tiempo (seek, cambio de calidad o de pista),
pero solo uno activo.

Campos: - `id` - `playback_session_id` - `job_kind` - `status` -
`input_file_id` - `output_profile` - `start_position_ms` - `progress` -
`started_at` - `finished_at` - `error_code` - `error_message`

### RefreshToken y PairingCode

Persisten los tokens rotatorios y los códigos de vinculación de la
sección 15.2. Solo se guarda el hash de cada valor.

## 6.2 Esquema SQL de referencia

El siguiente DDL es una base de diseño. Debe completarse con índices,
restricciones, triggers de actualización y migraciones incrementales
antes de producción.

``` sql
PRAGMA foreign_keys = ON;

CREATE TABLE users (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'user',
    is_active INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    last_login_at TEXT,
    max_age_rating INTEGER
);

CREATE TABLE user_preferences (
    user_id TEXT PRIMARY KEY,
    audio_language TEXT,
    subtitle_language TEXT,
    subtitle_mode TEXT NOT NULL DEFAULT 'default',
    max_bitrate_remote INTEGER,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE libraries (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    type TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    dlna_enabled INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE library_paths (
    id TEXT PRIMARY KEY,
    library_id TEXT NOT NULL,
    path TEXT NOT NULL,
    is_available INTEGER NOT NULL DEFAULT 1,
    last_scan_at TEXT,
    FOREIGN KEY (library_id) REFERENCES libraries(id)
        ON DELETE CASCADE,
    UNIQUE (library_id, path)
);

CREATE TABLE user_permissions (
    user_id TEXT NOT NULL,
    library_id TEXT NOT NULL,
    permission TEXT NOT NULL,
    PRIMARY KEY (user_id, library_id, permission),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (library_id) REFERENCES libraries(id) ON DELETE CASCADE
);

CREATE TABLE devices (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    device_name TEXT NOT NULL,
    platform TEXT NOT NULL,
    client_version TEXT,
    device_identifier TEXT,
    capabilities_json TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    last_seen_at TEXT,
    revoked_at TEXT,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE media_items (
    id TEXT PRIMARY KEY,
    library_id TEXT NOT NULL,
    parent_id TEXT,
    type TEXT NOT NULL,
    title TEXT NOT NULL,
    original_title TEXT,
    sort_title TEXT,
    year INTEGER,
    release_date TEXT,
    overview TEXT,
    age_rating TEXT,
    min_age INTEGER,
    original_language TEXT,
    duration_ms INTEGER,
    season_number INTEGER,
    episode_number INTEGER,
    match_key TEXT,
    metadata_status TEXT NOT NULL DEFAULT 'pending',
    metadata_locked INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    FOREIGN KEY (library_id) REFERENCES libraries(id) ON DELETE CASCADE,
    FOREIGN KEY (parent_id) REFERENCES media_items(id) ON DELETE CASCADE
);

CREATE TABLE media_external_ids (
    media_item_id TEXT NOT NULL,
    provider TEXT NOT NULL,
    external_id TEXT NOT NULL,
    PRIMARY KEY (media_item_id, provider),
    FOREIGN KEY (media_item_id) REFERENCES media_items(id) ON DELETE CASCADE
);

CREATE TABLE genres (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);

CREATE TABLE media_item_genres (
    media_item_id TEXT NOT NULL,
    genre_id TEXT NOT NULL,
    PRIMARY KEY (media_item_id, genre_id),
    FOREIGN KEY (media_item_id) REFERENCES media_items(id) ON DELETE CASCADE,
    FOREIGN KEY (genre_id) REFERENCES genres(id) ON DELETE CASCADE
);

CREATE TABLE people (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL
);

CREATE TABLE media_item_people (
    media_item_id TEXT NOT NULL,
    person_id TEXT NOT NULL,
    role TEXT NOT NULL,
    character_name TEXT,
    sort_order INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (media_item_id, person_id, role),
    FOREIGN KEY (media_item_id) REFERENCES media_items(id) ON DELETE CASCADE,
    FOREIGN KEY (person_id) REFERENCES people(id) ON DELETE CASCADE
);

CREATE TABLE media_images (
    id TEXT PRIMARY KEY,
    media_item_id TEXT NOT NULL,
    type TEXT NOT NULL,
    local_path TEXT,
    source_url TEXT,
    FOREIGN KEY (media_item_id) REFERENCES media_items(id) ON DELETE CASCADE,
    UNIQUE (media_item_id, type)
);

CREATE TABLE media_files (
    id TEXT PRIMARY KEY,
    library_path_id TEXT NOT NULL,
    relative_path TEXT NOT NULL,
    file_size INTEGER,
    modified_at TEXT,
    file_hash TEXT,
    container TEXT,
    duration_ms INTEGER,
    bitrate INTEGER,
    probe_status TEXT NOT NULL DEFAULT 'pending',
    missing_since TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    FOREIGN KEY (library_path_id) REFERENCES library_paths(id) ON DELETE CASCADE,
    UNIQUE (library_path_id, relative_path)
);

CREATE TABLE media_file_items (
    media_file_id TEXT NOT NULL,
    media_item_id TEXT NOT NULL,
    part_order INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (media_file_id, media_item_id),
    FOREIGN KEY (media_file_id) REFERENCES media_files(id) ON DELETE CASCADE,
    FOREIGN KEY (media_item_id) REFERENCES media_items(id) ON DELETE CASCADE
);

CREATE TABLE media_streams (
    id TEXT PRIMARY KEY,
    media_file_id TEXT NOT NULL,
    stream_index INTEGER NOT NULL,
    stream_type TEXT NOT NULL,
    codec TEXT,
    profile TEXT,
    level TEXT,
    language TEXT,
    channels INTEGER,
    width INTEGER,
    height INTEGER,
    frame_rate TEXT,
    bitrate INTEGER,
    bit_depth INTEGER,
    color_transfer TEXT,
    color_primaries TEXT,
    hdr_format TEXT,
    is_default INTEGER NOT NULL DEFAULT 0,
    is_forced INTEGER NOT NULL DEFAULT 0,
    stream_metadata_json TEXT,
    FOREIGN KEY (media_file_id) REFERENCES media_files(id) ON DELETE CASCADE,
    UNIQUE (media_file_id, stream_index)
);

CREATE TABLE subtitles (
    id TEXT PRIMARY KEY,
    media_file_id TEXT NOT NULL,
    relative_path TEXT NOT NULL,
    language TEXT NOT NULL DEFAULT 'und',
    format TEXT,
    is_default INTEGER NOT NULL DEFAULT 0,
    is_forced INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (media_file_id) REFERENCES media_files(id) ON DELETE CASCADE,
    UNIQUE (media_file_id, relative_path)
);

CREATE TABLE playback_progress (
    user_id TEXT NOT NULL,
    media_item_id TEXT NOT NULL,
    position_ms INTEGER NOT NULL DEFAULT 0,
    duration_ms INTEGER,
    completed INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (user_id, media_item_id),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (media_item_id) REFERENCES media_items(id) ON DELETE CASCADE
);

CREATE TABLE playback_sessions (
    id TEXT PRIMARY KEY,
    client_kind TEXT NOT NULL DEFAULT 'app',
    user_id TEXT,
    device_id TEXT,
    media_item_id TEXT NOT NULL,
    media_file_id TEXT NOT NULL,
    selected_audio_stream_id TEXT,
    selected_subtitle_id TEXT,
    playback_mode TEXT NOT NULL,
    status TEXT NOT NULL,
    start_position_ms INTEGER NOT NULL DEFAULT 0,
    position_ms INTEGER NOT NULL DEFAULT 0,
    duration_ms INTEGER,
    started_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    last_heartbeat_at TEXT,
    last_activity_at TEXT,
    ended_at TEXT,
    client_ip TEXT,
    CHECK (client_kind = 'dlna'
        OR (user_id IS NOT NULL AND device_id IS NOT NULL)),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE CASCADE,
    FOREIGN KEY (media_item_id) REFERENCES media_items(id) ON DELETE CASCADE,
    FOREIGN KEY (media_file_id) REFERENCES media_files(id) ON DELETE CASCADE
);

CREATE TABLE playback_history (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    device_id TEXT,
    media_item_id TEXT,
    title TEXT NOT NULL,
    position_ms INTEGER NOT NULL DEFAULT 0,
    completed INTEGER NOT NULL DEFAULT 0,
    watched_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE SET NULL,
    FOREIGN KEY (media_item_id) REFERENCES media_items(id) ON DELETE SET NULL
);

CREATE TABLE transcode_jobs (
    id TEXT PRIMARY KEY,
    playback_session_id TEXT NOT NULL,
    job_kind TEXT NOT NULL,
    status TEXT NOT NULL,
    input_file_id TEXT NOT NULL,
    output_profile TEXT NOT NULL,
    start_position_ms INTEGER NOT NULL DEFAULT 0,
    progress REAL NOT NULL DEFAULT 0,
    started_at TEXT,
    finished_at TEXT,
    error_code TEXT,
    error_message TEXT,
    FOREIGN KEY (playback_session_id) REFERENCES playback_sessions(id)
        ON DELETE CASCADE,
    FOREIGN KEY (input_file_id) REFERENCES media_files(id) ON DELETE CASCADE
);

CREATE TABLE server_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE scan_jobs (
    id TEXT PRIMARY KEY,
    library_id TEXT NOT NULL,
    trigger TEXT NOT NULL,
    status TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    files_discovered INTEGER NOT NULL DEFAULT 0,
    files_processed INTEGER NOT NULL DEFAULT 0,
    error_message TEXT,
    FOREIGN KEY (library_id) REFERENCES libraries(id) ON DELETE CASCADE
);

CREATE TABLE refresh_tokens (
    id TEXT PRIMARY KEY,
    device_id TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    expires_at TEXT NOT NULL,
    rotated_at TEXT,
    revoked_at TEXT,
    FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE CASCADE
);

CREATE TABLE pairing_codes (
    id TEXT PRIMARY KEY,
    code_hash TEXT NOT NULL UNIQUE,
    poll_hash TEXT NOT NULL UNIQUE,
    device_name TEXT NOT NULL,
    platform TEXT NOT NULL,
    client_version TEXT,
    device_identifier TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    expires_at TEXT NOT NULL,
    claimed_by_user_id TEXT,
    claimed_at TEXT,
    consumed_at TEXT,
    FOREIGN KEY (claimed_by_user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE VIRTUAL TABLE media_search USING fts5(
    media_item_id UNINDEXED,
    title,
    original_title,
    tokenize = 'unicode61 remove_diacritics 2'
);

-- Localiza la fila de búsqueda de cada elemento. FTS5 no admite índices
-- sobre media_item_id, y el rowid de media_items puede cambiar con VACUUM.
CREATE TABLE media_search_rows (
    media_item_id TEXT PRIMARY KEY,
    search_rowid INTEGER NOT NULL
);
```

`media_search` se mantiene con disparadores `AFTER INSERT`, `AFTER UPDATE
OF title, original_title` y `AFTER DELETE` sobre `media_items`, que
también se ejecutan en los borrados en cascada. Las temporadas no se
indexan. Sin la tabla de correspondencia, cada borrado recorrería todo el
índice; con ella, borrar una biblioteca de 10 000 elementos tarda medio
segundo.

### Índices recomendados

``` sql
CREATE INDEX idx_media_items_library
    ON media_items(library_id);

CREATE INDEX idx_media_items_parent
    ON media_items(parent_id);

CREATE INDEX idx_media_items_sort
    ON media_items(library_id, sort_title);

CREATE UNIQUE INDEX idx_media_items_match
    ON media_items(library_id, type, match_key)
    WHERE parent_id IS NULL AND match_key IS NOT NULL;

CREATE UNIQUE INDEX idx_media_items_season
    ON media_items(parent_id, season_number)
    WHERE type = 'season';

CREATE UNIQUE INDEX idx_media_items_episode
    ON media_items(parent_id, episode_number)
    WHERE type = 'episode';

CREATE INDEX idx_external_ids_lookup
    ON media_external_ids(provider, external_id);

CREATE INDEX idx_file_items_item
    ON media_file_items(media_item_id);

CREATE INDEX idx_media_files_missing
    ON media_files(missing_since)
    WHERE missing_since IS NOT NULL;

CREATE INDEX idx_media_streams_file
    ON media_streams(media_file_id);

CREATE INDEX idx_progress_updated
    ON playback_progress(updated_at);

CREATE INDEX idx_sessions_user_status
    ON playback_sessions(user_id, status);

CREATE INDEX idx_sessions_activity
    ON playback_sessions(last_activity_at);

CREATE INDEX idx_history_user
    ON playback_history(user_id, watched_at);

CREATE INDEX idx_transcode_status
    ON transcode_jobs(status);

CREATE UNIQUE INDEX idx_transcode_one_active
    ON transcode_jobs(playback_session_id)
    WHERE status IN ('starting', 'running');

CREATE INDEX idx_scan_library_status
    ON scan_jobs(library_id, status);

CREATE UNIQUE INDEX idx_scan_one_running
    ON scan_jobs(library_id)
    WHERE status = 'running';
```

`match_key` es la clave de agrupación local: título normalizado y año,
sacados del nombre del archivo. Los metadatos pueden cambiar `title`,
pero `match_key` no, para que un archivo nuevo encuentre el mismo
elemento. `duration_ms` y `bitrate` van en `media_files` porque cada
versión de un elemento puede diferir y la decisión de reproducción los
necesita por archivo.

## 6.3 Reglas de persistencia

-   Activar `PRAGMA foreign_keys = ON` en cada conexión.
-   Configurar WAL y `busy_timeout`.
-   Definir explícitamente `synchronous` según el equilibrio de
    durabilidad y rendimiento.
-   Utilizar transacciones para operaciones que modifican varias
    entidades.
-   No mantener transacciones abiertas durante escaneos, llamadas HTTP o
    transcodificación.
-   Mantener la base SQLite en disco local.
-   Hacer backup mediante la API de backup de SQLite o un procedimiento
    consistente; no copiar únicamente el archivo principal mientras WAL
    está activo.
-   Ejecutar migraciones al iniciar, con backup previo y manejo de
    errores.
-   Mantener una sola instancia escritora lógica del servidor.
-   Persistir todas las fechas con un único formato UTC de ancho fijo,
    `YYYY-MM-DDTHH:MM:SS.sssZ`, para que comparar texto sea correcto.
-   Generar `sort_title` en la aplicación, normalizado sin mayúsculas,
    acentos ni artículos iniciales. `NOCASE` solo cubre ASCII.
-   Mantener `media_search` sincronizada con `media_items` en la misma
    transacción.
-   Eliminar una biblioteca, un usuario o un elemento en curso exige
    cancelar antes sus sesiones activas y procesos FFmpeg.

# 7. Sistema de bibliotecas e indexación

## 7.1 Tipos de biblioteca

Primera versión: - Movies - TV Shows

Diseño extensible: - Music - Photos - Home Videos

## 7.2 Flujo de escaneo

``` text
Solicitud de escaneo
       |
       v
Crear ScanJob
       |
       v
Validar rutas configuradas
       |
       v
Recorrer directorios
       |
       v
Filtrar extensiones y archivos
       |
       v
Comparar tamaño y fecha de modificación
       |
       v
FFprobe para archivos nuevos/modificados
       |
       v
Persistir MediaFile y MediaStream
       |
       v
Identificar localmente por nombre y carpeta
       |
       v
Crear o reutilizar MediaItem y enlazar en MediaFileItem
       |
       v
Actualizar estado de biblioteca
       |
       v
Emitir evento library.updated
       |
       v
Marcar metadata_status = pending

(proceso asíncrono independiente)

Cola de enriquecimiento
       |
       v
Consultar proveedor, con límite de peticiones
       |
       v
Guardar metadatos e imágenes
       |
       v
Emitir evento library.updated
```

La indexación local termina sin depender del proveedor. Un elemento sin
metadatos externos aparece en el catálogo con los datos del archivo. La
cola se reconstruye desde `metadata_status`, por lo que sobrevive a los
reinicios, y omite los elementos con `metadata_locked = 1`.

## 7.3 Detección de cambios

La detección debe combinar:

-   Escaneo completo manual o programado.
-   Comparación de ruta, tamaño y `mtime`.
-   Watchers del sistema operativo cuando estén disponibles.
-   Reconciliación periódica para detectar eventos perdidos.
-   Detección de rutas inaccesibles o volúmenes desconectados.

Los watchers son una optimización, no la única fuente de verdad. En
redes SMB/NFS y discos extraíbles pueden perder eventos o comportarse de
manera diferente.

Tratamiento de archivos ausentes:

1.  Si la raíz de `LibraryPath` no está accesible, se marca
    `is_available = 0` y no se modifica ningún archivo.
2.  Si la raíz está accesible y falta un archivo, se registra
    `missing_since`. No se borra ni se pierde su progreso.
3.  Un archivo nuevo con el mismo tamaño y la misma huella parcial que
    uno ausente se trata como movido: se actualiza su ruta y conserva su
    identificador.
4.  Si reaparece, se limpia `missing_since`.
5.  Pasado un periodo de retención configurable, se elimina el archivo.
    Solo entonces se elimina también el `MediaItem` sin archivos, y con
    él su progreso.

`file_hash` se calcula al indexar cada archivo nuevo o modificado:
cuando un archivo falta ya no se puede leer, así que su huella debe estar
guardada antes. Cuesta leer 2 MiB por archivo nuevo, una sola vez. Un
archivo nuevo cuya huella coincide con la de uno ausente se trata como
movido sin volver a analizarlo con FFprobe.

Dos casos más no deben tomarse por ausencias:

-   Una carpeta que no se puede leer: sus archivos se conservan.
-   Una raíz accesible pero completamente vacía cuando la base conoce
    archivos en ella. Así se ve un punto de montaje sin su disco, y se
    trata como raíz no disponible.

## 7.4 Seguridad del scanner

-   Las rutas deben registrarse explícitamente por el administrador.
-   Resolver rutas absolutas y symlinks según una política definida.
-   Evitar path traversal.
-   No seguir symlinks fuera de las raíces autorizadas salvo
    configuración explícita. Un symlink de archivo se indexa solo si su
    destino queda dentro de la raíz; los symlinks de carpeta y las
    uniones de Windows no se recorren.
-   No aceptar rutas de medios enviadas por clientes para abrir archivos
    arbitrarios.
-   Ejecutar el servidor con permisos mínimos sobre los directorios
    multimedia.
-   Aplicar límites de tamaño, profundidad y tiempo de análisis.

## 7.5 Identificación de películas y series

La identificación de metadatos debe separar:

1.  Nombre de archivo y normalización.
2.  Identificación probable.
3.  Consulta al proveedor de metadatos.
4.  Confirmación automática o manual.
5.  Persistencia del identificador externo.
6.  Caché local de resultados e imágenes.

Los proveedores externos serán adaptadores configurables. El sistema
debe poder conservar el catálogo local si el proveedor se desconecta o
cambia sus condiciones.

# 8. Metadatos e imágenes

## 8.1 Datos descriptivos

-   Título original.
-   Título de presentación.
-   Título ordenable.
-   Sinopsis.
-   Año.
-   Fecha de estreno.
-   Géneros.
-   Clasificación por edades.
-   Duración.
-   Idioma original.
-   Personas: actores, directores, guionistas.
-   Identificadores externos.
-   Temporadas y episodios.

## 8.2 Imágenes

Tipos: - Poster. - Backdrop. - Thumbnail. - Logo. - Imagen de persona,
si se incorpora esa función.

Las imágenes se descargan desde la cola de enriquecimiento y se guardan
en `data/images/{item_id}/{type}.{ext}`, en el tamaño que sirve el
proveedor y sin redimensionar. Abrir un detalle nunca descarga: sin
archivo, la API responde 404. Se sirven con `Cache-Control: private` y
`ETag`.

No hay un límite de tamaño: borrar una imagen vigente obligaría a
descargarla al abrir el detalle. La caducidad tiene dos partes:

- Se borran los archivos que ninguna fila referencia: elementos
  eliminados, imágenes sustituidas y descargas interrumpidas.
- Los metadatos y las imágenes de un elemento sin bloquear se renuevan a
  los 150 días, porque TMDB prohíbe guardar su contenido más de seis meses
  (ADR 0005).

Estado de metadatos de cada elemento principal:

- `pending`: forma la cola.
- `done`: identificado.
- `unmatched`: el proveedor no devolvió una coincidencia segura.
- `failed`: el proveedor respondió con un error permanente.

Un fallo pasajero, como la falta de red, un error 5xx, un 429 o una clave
rechazada, deja el elemento en `pending` y pausa la cola con espera
creciente, de 1 minuto a 1 hora. La coincidencia automática solo se acepta
en dos casos:

- Con año local: un resultado con el año a ±1 que sea el primero o tenga
  el mismo título.
- Sin año: un resultado con el mismo título.

Un título igual con otro año es otra versión y no se acepta.

## 8.3 Identificación manual

El panel debe permitir: - Buscar coincidencias. - Elegir el resultado
correcto. - Cambiar el proveedor o identificador. - Bloquear metadatos
para evitar sobrescrituras automáticas. - Forzar actualización.

Las operaciones exigen el permiso `manage` sobre la biblioteca:

- **Identificar, buscar coincidencias y forzar actualización:** solo para
  películas y series, porque temporadas y episodios se identifican con su
  serie. Sin proveedor configurado responden
  `METADATA_PROVIDER_UNAVAILABLE`.
- **Forzar actualización:** devuelve el elemento a la cola. Si ya tiene
  identificador, se piden los detalles por él, sin volver a buscar.
- **Editar:** vale para cualquier elemento y no necesita proveedor.
  Bloquea el elemento salvo que la petición indique otra cosa.
- **Bloqueo:** identificar o actualizar un elemento bloqueado responde
  `METADATA_LOCKED`.

# 9. Motor de reproducción

## 9.1 Modos de reproducción

  -----------------------------------------------------------------------
  Modo                    Descripción             Uso
  ----------------------- ----------------------- -----------------------
  Direct Play             Entrega del archivo     Contenedor, video,
                          original                audio y subtítulos
                                                  compatibles

  Direct Stream / Remux   Reempaquetado sin       Códecs compatibles pero
                          recodificar streams     contenedor no
                                                  compatible

  Audio Transcode         Recodificación de audio Video compatible, audio
                                                  incompatible

  Video Transcode         Recodificación de video Video incompatible

  Full Transcode          Recodificación de audio Ambos incompatibles
                          y video                 
  -----------------------------------------------------------------------

La selección debe ser por stream. No se debe asumir que la
incompatibilidad de audio obliga a recodificar video.

En la API, `playback_mode` solo admite `direct_play`, `direct_stream` y
`transcode`. El protocolo se indica por separado en `protocol` (`http` o
`hls`), y el tratamiento de cada stream en `video_action` y
`audio_action` (`copy` o `transcode`).

El burn-in no es un modo de reproducción: es un filtro de la
recodificación de video. Por eso, un subtítulo que requiere burn-in
obliga a `video_action = transcode` aunque el video sea compatible.

`subtitle_mode` admite `none`, `embedded`, `sidecar`, `convert`, `hls` y
`burn_in`:

- **`embedded`:** solo existe en Direct Play. El archivo llega entero con
  sus pistas, y un reproductor como Media3 dibuja el subtítulo interno él
  mismo. Si no hay Direct Play, la pista interna hay que extraerla
  (`convert`).
- **`convert`:** extraer o convertir a un formato que el cliente acepte
  aparte.
- **Subtítulos de imagen:** si no van `embedded`, siempre se queman.

## 9.2 Perfil de capacidades del cliente

Cada cliente enviará un perfil con: - Plataforma y versión. -
Contenedores soportados. - Códecs de video. - Perfiles y niveles. -
Códecs de audio. - Resoluciones máximas. - Frame rates soportados. - HDR
y formatos de color soportados. - Subtítulos y formatos. - Formas de
entrega de subtítulos: archivo independiente (`sidecar`) o pista HLS
(`hls`). - Protocolos de streaming. - Capacidad de reproducción
directa. - Limitaciones conocidas.

El servidor puede mantener perfiles predefinidos por plataforma, pero el
perfil enviado por el cliente debe ser validado y no debe considerarse
una garantía absoluta.

- **Campos del perfil:** lo que el cliente no envía toma el valor
  predefinido de su plataforma (`web`, `android`, `androidtv`, `ios`,
  `tvos`, `tizen`, `webos`). Lo que envía manda, aunque sea una lista
  vacía.
- **Nombres:** los desconocidos se descartan, porque nunca casarían con
  lo que devuelve FFprobe.
- **Perfiles predefinidos:** son estimaciones prudentes, pendientes de
  validar en cada dispositivo (§26.6). Una plataforma desconocida recibe
  MP4, H.264, AAC y WebVTT aparte.
- **Límites:** además de los códecs, el perfil declara resolución
  máxima, profundidad de bits, formatos HDR y número de canales.

## 9.3 Algoritmo de decisión

``` text
CreatePlaybackRequest
        |
        v
Authorize user and library
        |
        v
Select media file (media_file_id or best match)
        |
        v
Load stream metadata and client capabilities
        |
        v
Check network/quality constraints
        |
        v
Resolve subtitle strategy
(none / sidecar / hls / convert / burn-in)
        |
        v
Choose playback mode:
  - Burn-in required           -> transcode (video)
  - Everything supported       -> direct_play
  - Only container unsupported -> direct_stream
  - Incompatible streams       -> transcode (audio / video / both)
        |
        v
Select output profile (if not direct_play)
        |
        v
Reserve capacity (if FFmpeg is required)
        |
        v
Create playback session and job
        |
        v
Return playback manifest/URL
```

Todas las ramas pasan por la estrategia de subtítulos antes de elegir el
modo, y todas terminan creando la sesión.

Si no hay capacidad, se rechaza la solicitud antes de crear la sesión.

Sin `media_file_id`, se evalúan todas las versiones disponibles y gana
la de mejor modo: Direct Play antes que Direct Stream, y este antes que
transcodificar. A igualdad de modo gana la de más píxeles y, después, la
de más bitrate. Las versiones ausentes o con análisis fallido no
cuentan.
Un cliente que solo admite subtítulos por HLS, como AVPlayer, recibe
`direct_stream` por HLS en lugar de Direct Play cuando necesita una
pista externa.

## 9.4 Solicitud de reproducción

El cliente debe enviar: - `media_item_id` - `media_file_id` (opcional;
versión elegida por el usuario) - `device_id` - `client_profile` -
`selected_audio_stream_id` - `selected_subtitle_id` - `max_bitrate` -
`max_resolution` - `preferred_protocol` - `start_position_ms`

Las pistas se identifican por UUID, no por índice, porque cada índice
solo tiene sentido dentro de un archivo concreto y un subtítulo externo
no tiene índice. Si se indican pistas, deben pertenecer al archivo
seleccionado o se aplican las preferencias de idioma del usuario.

Una pista de otro archivo responde `VALIDATION_ERROR`.
`selected_subtitle_id` admite el valor `none`, que desactiva los
subtítulos. Sin pistas indicadas:

- **Audio:** el del idioma preferido; si no lo hay, el marcado por
  defecto, y si tampoco, el primero.
- **Subtítulo según `subtitle_mode`:**
  - `off`: ninguno.
  - `always`: el del idioma preferido, sin forzados si hay otro.
  - `forced`: el forzado en el idioma del audio.
  - `default`: el marcado por defecto.

Los idiomas se comparan sin distinguir ISO 639-1, 639-2 ni región:
«spa», «es» y «es-ES» son el mismo. `device_id` es opcional y, si
llega, debe ser el dispositivo de la sesión iniciada.

El servidor debe: 1. Validar identidad y permisos. 2. Validar que el
archivo exista y esté disponible. 3. Comprobar que el dispositivo
pertenece al usuario o está autorizado. 4. Seleccionar archivo y
pistas. 5. Analizar compatibilidad, incluidos los subtítulos. 6.
Reservar capacidad si corresponde. 7. Crear sesión. 8. Crear trabajo de
transcodificación si corresponde. 9. Emitir URL de reproducción con su
token de sesión. 10. Registrar la decisión y su motivo.

## 9.5 Respuesta conceptual

``` json
{
  "session_id": "uuid",
  "status": "initializing",
  "playback_mode": "transcode",
  "protocol": "hls",
  "container": "fmp4",
  "video_action": "transcode",
  "audio_action": "copy",
  "video_codec": "h264",
  "audio_codec": "aac",
  "subtitle_mode": "hls",
  "selected_audio_stream_id": "stream-uuid",
  "selected_subtitle_id": "subtitle-uuid",
  "stream_url": "/stream/session/uuid/stream-token/master.m3u8",
  "idle_timeout_seconds": 90
}
```

La respuesta final debe incluir también un identificador de perfil de
salida y, cuando aplique, información de resolución y bitrate. El
contrato definitivo se documentará con OpenAPI.

La respuesta implementada añade `media_file_id`, `subtitle_url`,
`start_position_ms`, `duration_ms` y `reasons`. `reasons` es la lista de
motivos, cada uno con `code` y `stream`, y está vacía en Direct Play.

# 10. Streaming y FFmpeg

## 10.1 Transcoding Manager

El Transcoding Manager controla todos los procesos multimedia.

Responsabilidades: - Crear trabajos. - Validar parámetros. - Generar
argumentos FFmpeg sin invocar un shell. - Iniciar y supervisar
procesos. - Capturar stderr y progreso. - Aplicar timeout y
cancelación. - Limitar concurrencia. - Detectar errores. - Mantener
archivos de salida. - Limpiar recursos. - Finalizar trabajos al cerrar
sesión.

**Seguridad:** no concatenar parámetros de usuarios en comandos de
shell. Construir argumentos como una lista de parámetros, validar cada
opción y utilizar rutas autorizadas.

## 10.2 Estructura de trabajo

``` text
data/
└── transcodes/
    └── {session_id}/
        ├── master.m3u8
        ├── video/
        │   ├── init.mp4
        │   └── segment_*.m4s
        ├── audio/
        │   ├── init.mp4
        │   └── segment_*.m4s
        ├── subtitles/
        └── job.json
```

La estructura exacta dependerá del perfil HLS elegido. El servidor no
debe asumir que todos los clientes soportan todas las variantes de HLS o
todos los formatos de subtítulos.

## 10.3 Perfiles de salida

Definir perfiles configurables, por ejemplo: - Original compatible. -
4K. - 1080p. - 720p. - 480p. - Audio compatible. - Perfil de bajo ancho
de banda.

Los valores de bitrate, preset, codec y resolución deben ajustarse
mediante pruebas y configuración. No deben codificarse como una única
política para todos los equipos.

## 10.4 HLS

HLS será el protocolo de streaming principal para los clientes que lo
soporten.

Consideraciones: - Master playlist y media playlists. - Segmentos
fMP4/CMAF cuando el cliente lo admita. - Segmentos y manifiestos
protegidos por sesión. - Limpieza de segmentos al finalizar o expirar la
sesión. - Política de retención para permitir pequeños retrocesos. -
Manejo de discontinuidades y reinicios. - Audio y subtítulos como pistas
seleccionables cuando sea compatible. - TLS en acceso remoto. - Token de
sesión con alcance limitado (sección 15.4).

No asumir que HLS es obligatorio para todos los clientes. Direct Play
puede entregar el archivo original por HTTP con soporte correcto de
Range Requests.

**Contenedor de los segmentos (implementado en la Fase 8, ADR 0006):**

  -----------------------------------------------------------------------
  Vídeo         Segmentos             Muxer de FFmpeg   Playlist
  ------------- --------------------- ----------------- -----------------
  Recodificar   fMP4 (`init.mp4` +    `hls`             VOD sintética,
                `.m4s`)                                 rejilla fija

  Copiar        MPEG-TS (`.ts`)       `segment`         VOD sintética
                                                        desde el índice de
                                                        keyframes; `EVENT`
                                                        sin índice
  -----------------------------------------------------------------------

-   Al recodificar, `movflags=+frag_discont` hace que `tfdt` sea absoluto
    y que el `init.mp4` sea idéntico en cada reinicio; una sola
    `EXT-X-MAP` sirve para toda la sesión.
-   Al copiar, los cortes se pasan a FFmpeg de forma explícita, en
    microsegundos y relativos al salto, para que un reinicio produzca los
    mismos segmentos que la pasada completa.
-   El servidor recodifica lo que el contenedor de salida no puede llevar
    aunque el cliente lo admita (VP9 en MPEG-TS, Opus en MPEG-TS).
-   El preset `web` declara solo `http`: un cliente con hls.js debe
    declarar `protocols: ["http", "hls"]`.
-   Verificado con hls.js 1.6 en Chromium. Pendiente en Safari, AVPlayer y
    Media3.

## 10.5 Hardware acceleration

Detección y perfiles por sistema:

  Sistema   Tecnologías a evaluar
  --------- ---------------------------------------
  Windows   NVIDIA NVENC, Intel Quick Sync, AMD AMF
  Linux     NVIDIA NVENC, VAAPI, Intel Quick Sync
  macOS     VideoToolbox

La disponibilidad depende de hardware, drivers, versión y compilación de
FFmpeg. La detección debe incluir una prueba real de inicialización, no
solo comprobar que existe un dispositivo.

Fallback: 1. Intentar aceleración compatible. 2. Registrar el fallo. 3.
Cambiar a CPU si la política lo permite. 4. Notificar el modo efectivo.
5. Evitar ciclos infinitos de reintento.

## 10.6 Control de recursos

Configurar: - Máximo de transcodificaciones de video simultáneas. -
Máximo de trabajos ligeros simultáneos (remux y solo audio). - Límite de
memoria por proceso cuando el sistema lo permita. - Presets de CPU. -
Límite de bitrate. - Límite de resolución. - Timeout de
inicialización. - Timeout de inactividad. - Cuota de almacenamiento
temporal. - Prioridad de trabajos.

El número máximo de trabajos debe basarse en benchmarks del hardware
real, no en una cifra universal.

El control de admisión cuenta trabajos por tipo. La reproducción es en
tiempo real, así que no hay cola: si no hay cupo, se rechaza con
`TRANSCODE_CAPACITY_EXCEEDED`. El cupo pertenece a la sesión, no al
proceso, de modo que un seek o un cambio de calidad no lo pierde.

## 10.7 Seek, reinicios y cambio de calidad

La transcodificación ocurre mientras se reproduce, por lo que el usuario
puede saltar a una posición que aún no existe.

1.  El servidor genera por adelantado una playlist VOD sintética con
    todos los segmentos y una duración fija. El reproductor conoce así
    la duración completa y puede saltar a cualquier punto.
2.  FFmpeg fuerza keyframes en cada límite de segmento para que coincidan
    con la playlist. Con `video_action = copy`, los límites dependen de
    los keyframes del original, por lo que se necesita un índice de
    keyframes (por validar).
3.  Al pedir un segmento:
    -   Si ya existe, se entrega.
    -   Si está dentro de la ventana que FFmpeg producirá pronto, se
        espera con un límite de tiempo.
    -   Si está más lejos, se detiene el trabajo activo y se crea otro
        desde esa posición.
4.  Los timestamps de salida conservan la línea de tiempo original, de
    modo que la posición informada por el reproductor siempre es la del
    archivo fuente.
5.  Los segmentos ya generados se conservan hasta agotar la cuota
    temporal, para que retroceder no obligue a transcodificar otra vez.

**Calidad:** cada sesión produce una sola variante. Cambiar de calidad o
de pista crea un trabajo nuevo en su propio subdirectorio, en la misma
sesión y sin consumir otro cupo. Generar varias variantes a la vez solo
se incorporará si los benchmarks lo justifican.

La duración de segmento y la distancia que obliga a reiniciar FFmpeg se
configuran con `transcoding.segment_seconds` y
`transcoding.seek_restart_segments`.

**Índice de keyframes y playlist `EVENT`:** crear una sesión que copia el
vídeo pide el índice y espera hasta 8 s a que exista. Si no llega (un
archivo enorme que aún no se ha indexado), la sesión usa una playlist
`EVENT` con seek limitado a lo ya generado, y la petición de la playlist
espera al primer segmento para no devolver una lista vacía. La siguiente
sesión del mismo archivo ya tiene el índice (ADR 0004).

**Reinicios y orfandad:** se reinicia FFmpeg si no hay trabajo activo, si
el segmento pedido es anterior al inicio del trabajo, o si está más allá de
`seek_restart_segments` del segmento en curso. El trabajo reiniciado lleva
un límite de tiempo (`-t`) para no reescribir segmentos que ya existen.
Si el servidor muere, FFmpeg muere con él: Job Object con
`KILL_ON_JOB_CLOSE` en Windows y `Pdeathsig` en Linux. **En macOS no hay
supervisión todavía**: un fallo del servidor puede dejar FFmpeg vivo.

## 10.8 HDR y tone mapping

Si la salida es SDR y el origen es HDR10, HLG o Dolby Vision, la
transcodificación debe aplicar tone mapping. Sin él, la imagen se ve
apagada y con colores lavados. La decisión usa `hdr_format`,
`color_transfer` y `color_primaries` del stream. System Check debe
comprobar si la build de FFmpeg y el hardware disponen del filtro
necesario. Si no lo tienen, el servidor debe informar del modo efectivo
o rechazar la reproducción, según la política configurada.

**Implementación (Fase 9, ADR 0007).** El filtro por software es
`setparams` (BT.2020 y la transferencia del origen), `zscale` a luz lineal,
`tonemap=hable` y `zscale` a BT.709 en 8 bits; la salida declara BT.709.
Dolby Vision se trata por su capa base HDR10, de modo que el perfil 5 se
vería mal. Sin subtítulos grabados, el escalado va antes del tone mapping:
este trabaja en coma flotante y cuesta por píxel.

El coste es alto. En un Intel i5-2400, un HDR de 720p se convierte a 0,44×
tiempo real (0,32× medido al arrancar), así que la imagen se corta. Al
arrancar, `ffmpeg.Probe` comprueba la cadena con una imagen real y mide su
velocidad (`tone_mapping_speed` en `/server/capabilities`). La política es
`transcoding.tone_mapping`:

-   `auto` (por defecto): convierte siempre que FFmpeg pueda y avisa en el log
    si el equipo no llega a tiempo real.
-   `realtime`: convierte solo si el equipo llega a tiempo real.
-   `off`: no convierte nunca.

Lo que no se convierte se rechaza con `422 PLAYBACK_UNSUPPORTED` y un motivo.
`libplacebo` y `tonemap_opencl` necesitan GPU y no están probados.

# 11. Subtítulos y pistas de audio

## 11.1 Formatos

Primera prioridad: - SRT externo. - SRT interno. - WebVTT. - ASS/SSA,
con estrategia según cliente. - PGS (Blu-ray) y VobSub (DVD).

PGS y VobSub son subtítulos de imagen: no se pueden convertir a texto
sin OCR, así que en la mayoría de clientes requieren burn-in.

Otros formatos internos se incorporarán después de pruebas de
compatibilidad.

## 11.2 Estrategias

1.  **Soft subtitles:** entregar la pista por separado si el cliente la
    soporta.
2.  **Subtitle conversion:** convertir a un formato compatible cuando
    sea posible.
3.  **Burn-in:** integrar el subtítulo en la imagen cuando el cliente no
    pueda renderizarlo adecuadamente.

La forma de entrega depende del reproductor. Media3 admite archivos
independientes. AVPlayer no acepta un `.srt` o `.vtt` suelto: necesita
WebVTT como pista dentro de HLS, salvo que la aplicación dibuje el
subtítulo por su cuenta.

Un SRT externo se convierte a WebVTT en el servidor, sin FFmpeg, para los
reproductores que solo aceptan WebVTT, como `<video>`. Un archivo que no
es UTF-8 se interpreta como Windows-1252, lo habitual en subtítulos en
español.

Extraer un subtítulo interno suele exigir leer todo el contenedor, lo
que puede tardar minutos en un NAS. El resultado se guarda en caché por
archivo y pista, y no debe bloquear el inicio de la reproducción.

**Implementación (Fase 9, ADR 0007).**

-   Una pista de texto interna (SRT, ASS/SSA, WebVTT, mov_text) o un externo
    que no sea SRT ni WebVTT se convierte con FFmpeg a WebVTT en
    `data/cache/subtitles/{file_id}/{track_id}.vtt`, invalidado por tamaño y
    fecha del origen. Crear la sesión no espera: lanza la extracción. Pedir
    el subtítulo espera hasta 25 s y, si no llega, responde `503` con
    `Retry-After` mientras la extracción sigue. Dos extracciones a la vez
    como máximo, y las peticiones de la misma pista comparten una.
-   Para un cliente que solo declara `subtitle_delivery: ["hls"]`, el master
    lleva una pista `EXT-X-MEDIA` de subtítulos con `subs.m3u8` y `subs.vtt`.
-   **Burn-in:** el subtítulo de texto se convierte a ASS y se aplica con el
    filtro `subtitles`; el de imagen (PGS, VobSub, DVB) se superpone con
    `overlay` desde el propio archivo. Fuerza recodificar el vídeo. La cadena
    de filtros va en este orden: tone mapping, subtítulos y escalado.
-   Un formato que FFmpeg no convierte, o un FFmpeg sin el filtro necesario, se
    rechaza con `422 PLAYBACK_UNSUPPORTED` y un motivo.
-   Los subtítulos de imagen grabados no se han ejecutado con una pista real.

La selección debe considerar: - Idioma. - Pista por defecto. - Pista
forzada. - Preferencias del usuario. - Capacidad del cliente. -
Posibilidad de conversión sin recodificar video.

## 11.3 Audio

Permitir: - Selección de pista. - Preferencia de idioma. -
Identificación de canales. - Audio por defecto. - Recodificación
independiente del video. - Información de códec y bitrate.

**Cambio de pista en una sesión viva (Fase 9).**
`POST /playback/sessions/{id}/tracks` cambia el audio o los subtítulos:

-   En una sesión HLS reinicia FFmpeg con las pistas nuevas y devuelve un
    `stream_url` con `?rev=N`; el cliente recarga la lista desde la
    posición actual. El cupo pertenece a la sesión y no se toca; si el cambio
    pasa de copiar a recodificar, se reserva el cupo pesado y, si no hay
    sitio, la sesión sigue como estaba (`503` con `Retry-After`).
-   En Direct Play el archivo llega entero y el reproductor elige el audio, así
    que solo cambia la selección guardada y la URL del subtítulo. Si el cambio
    exigiera remux o transcodificar, `422` y hay que crear otra sesión.
-   El estado de la sesión vive en memoria: tras un reinicio del servidor la
    sesión se cierra (§12.5) y el cliente crea otra.

Un 5.1 se mezcla a estéreo cuando el cliente solo admite dos canales, y se
copia cuando los admite.

# 12. Sesiones y sincronización de progreso

## 12.1 Ciclo de vida de una sesión

``` text
CREATED
   |
   v
INITIALIZING
   |
   v
PLAYING <---> PAUSED
   |             |
   +------+------+
          |
          v
       STOPPING
          |
          v
COMPLETED / STOPPED
```

Desde cualquier estado no terminal también se puede pasar a `FAILED` o
`EXPIRED`. En la API, los estados se escriben en minúsculas.

El diagrama admite además `INITIALIZING → PAUSED`, porque un navegador que
bloquea la reproducción automática empieza en pausa, y `CREATED →
STOPPING`, para cerrar una sesión recién creada. Repetir el estado actual
en un heartbeat no es una transición.

`position_ms` y `duration_ms` de `playback_sessions` guardan el último
estado volcado: tras una caída, la reconciliación cierra la sesión con esa
posición. Cerrar a petición del cliente lleva a `COMPLETED` si se superó
el umbral y a `STOPPED` si no.

Las transiciones deben validarse en el servidor. Una sesión expirada no
puede volver a reproducir segmentos ni actualizar progreso.

## 12.2 Heartbeat

El cliente enviará heartbeats periódicos mientras la sesión esté activa.
El intervalo será configurable; cinco segundos no debe ser un requisito
fijo.

El heartbeat puede incluir: - Posición actual en milisegundos. - Estado
de reproducción. - Timestamp del cliente. - Buffer aproximado, si está
disponible. - Identificador de sesión.

El servidor: - Actualiza `last_heartbeat_at`. - Actualiza progreso de
forma controlada. - Detecta sesiones abandonadas. - Cierra sesiones
expiradas. - Libera trabajos de transcodificación.

Guardar cada heartbeat en disco puede generar escrituras innecesarias.
Se recomienda agrupar persistencia y guardar inmediatamente en eventos
relevantes: pausa, stop, finalización y cierre de sesión.

## 12.3 Continuar viendo

-   Progreso independiente por usuario.
-   Umbral configurable para marcar como completado.
-   Reanudación entre dispositivos.
-   No sobrescribir progreso reciente con eventos atrasados de otro
    dispositivo.
-   Política para reproducción simultánea del mismo usuario: se permite.
    Cada dispositivo escribe su progreso y gana el dato más reciente.
-   Historial separado de progreso actual.

`playback_progress.updated_at` es la hora del dato, no la de escritura: la
del último heartbeat o la que envía el cliente en `PUT /playback/progress`
si no es futura. El volcado periódico usa la hora del heartbeat; con la de
volcado, un progreso más antiguo de otro dispositivo podría pisar uno
reciente.

Una sesión sin ningún heartbeat no escribe progreso ni historial: abrir
algo por error desde el principio no debe borrar la posición guardada. El
historial se escribe una sola vez, al cerrar la sesión, y conserva el
título aunque el elemento desaparezca. Un elemento sin duración conocida
no puede marcarse como completado.

## 12.4 Expiración y reanudación

La sesión no tiene una caducidad fija, porque un reproductor no puede
cambiar su URL a mitad de una película. Expira por inactividad:

-   `last_activity_at` se actualiza con cada heartbeat, segmento o
    lectura por rango.
-   Si pasa `heartbeat_timeout_seconds` sin actividad, la sesión pasa a
    `EXPIRED` y se libera su trabajo y su cupo.
-   El token de streaming vive lo mismo que la sesión.

Una pausa en primer plano sigue enviando heartbeats. Si el sistema
suspende la aplicación, la sesión expira. Al volver, el cliente recibe
`SESSION_EXPIRED` y crea una sesión nueva con `start_position_ms` en la
última posición conocida, sin que el usuario tenga que intervenir.

## 12.5 Reconciliación al arrancar

Tras una caída o un apagado incorrecto, y antes de aceptar peticiones,
el servidor debe:

1.  Pasar las sesiones no terminales a `EXPIRED`.
2.  Marcar los trabajos activos como `failed` con
    `error_code = SERVER_RESTART`.
3.  Marcar como interrumpidos los escaneos en curso.
4.  Eliminar los directorios de `transcodes/` sin sesión activa.

El historial de cada sesión reconciliada se escribe con su última
posición volcada; lo que solo estaba en memoria se pierde con la caída. En
un apagado controlado, en cambio, las sesiones activas se cierran como
`EXPIRED` con todo su progreso volcado.

Para que FFmpeg no sobreviva al servidor, cada proceso se vincula al
padre con el mecanismo del sistema: `PR_SET_PDEATHSIG` en Linux, Job
Objects en Windows y un mecanismo de supervisión equivalente en macOS.

# 13. Descubrimiento de servidores y DLNA

## 13.1 Descubrimiento MediaNode

Usar mDNS/DNS-SD para anunciar un servicio propio, por ejemplo:

``` text
_medianode._tcp.local.
```

El anuncio debe incluir únicamente información no sensible: - Nombre
visible. - Puerto HTTP/HTTPS local. - Identificador público del
servidor. - Versión del protocolo.

No anunciar tokens, nombres de usuarios ni rutas de bibliotecas.

Alternativas: - Entrada manual de IP o dominio. - QR/código de
vinculación. - Historial de servidores conocidos. - Descubrimiento
mediante un servicio de emparejamiento remoto, si se incorpora.

## 13.2 DLNA / UPnP

El servidor DLNA es un subsistema separado del descubrimiento MediaNode.

Componentes: - SSDP discovery. - Device Description XML. - Content
Directory Service. - Connection Manager Service. - HTTP media
delivery. - Respuestas DIDL-Lite. - Gestión de perfiles y MIME types.

El soporte DLNA debe probarse con televisores y reproductores concretos.
La compatibilidad real depende de los perfiles DLNA, formatos admitidos
y comportamiento de cada fabricante.

No se debe asumir que un renderer DLNA puede utilizar la misma URL o los
mismos mecanismos de autenticación que una aplicación MediaNode.

Política de acceso DLNA:

-   Desactivado por defecto; lo activa un administrador.
-   Solo se exponen las bibliotecas con `dlna_enabled = 1`. Un renderer
    no se autentica, por lo que cualquier equipo de la LAN puede ver
    esas bibliotecas. El panel debe advertirlo al activarlas.
-   SSDP y los endpoints DLNA responden únicamente a direcciones de la
    red local y nunca por la capa de acceso remoto.
-   Las sesiones usan `client_kind = 'dlna'`, sin usuario ni dispositivo.
    No generan progreso ni historial por usuario.
-   Como no envían heartbeat, expiran por inactividad de las peticiones
    HTTP.

# 14. Acceso remoto

## 14.1 Modalidades

  -----------------------------------------------------------------------
  Modalidad                           Descripción
  ----------------------------------- -----------------------------------
  LAN                                 Conexión directa en red local

  Remote Direct                       Conexión al servidor mediante
                                      dominio/IP y TLS

  Remote Relay                        Transporte mediante un servicio
                                      intermediario opcional
  -----------------------------------------------------------------------

## 14.2 Remote Direct

Requisitos: - Dominio o DNS dinámico configurado por el usuario. - TLS
válido. - Puerto publicado o reverse proxy. - Autenticación y
autorización. - Tokens de reproducción limitados. - Límites de
conexiones. - Protección contra abuso. - Comprobación de conectividad
desde fuera de la LAN.

No exponer endpoints administrativos sin autenticación. El panel debe
poder deshabilitarse para acceso remoto.

## 14.3 Remote Relay

Diseño conceptual: 1. El servidor abre una conexión saliente autenticada
al relay. 2. El cliente se autentica con el servicio de
identidad/vinculación. 3. El relay verifica que existe autorización para
el servidor. 4. Se crea un túnel de transporte entre cliente y servidor.
5. El relay transporta datos, sin interpretar ni almacenar contenido
multimedia. 6. El servidor mantiene la autorización efectiva de cada
recurso.

El relay aumenta costos de ancho de banda y latencia. Debe ser opcional
y puede reservarse para una fase posterior.

## 14.4 Seguridad de red

-   TLS moderno.
-   Certificados validados.
-   En LAN, el servidor genera un certificado propio y activa HTTPS por
    defecto. Los clientes lo fijan durante la vinculación. Los navegadores
    mostrarán una advertencia (por validar).
-   HTTP queda disponible solo para clientes de LAN que lo necesiten, y
    el login por HTTP debe avisar de que la contraseña viaja sin cifrar.
-   Las cabeceras de reenvío solo se aceptan desde `trusted_proxies`. Una
    petición con esas cabeceras desde otro origen se trata como remota,
    para que un reverse proxy no haga pasar tráfico remoto por local.
-   Tokens con expiración y alcance.
-   Revocación de dispositivos.
-   Rate limiting.
-   Protección contra fuerza bruta.
-   CORS restrictivo para el cliente web.
-   Protección CSRF donde se utilicen cookies.
-   No confiar en IP como mecanismo de identidad.
-   Evitar exposición de puertos de administración.
-   No utilizar UPnP IGD para abrir puertos automáticamente sin
    consentimiento explícito del usuario.

# 15. Autenticación, autorización y perfiles

## 15.1 Roles

  -----------------------------------------------------------------------
  Rol                                 Capacidades
  ----------------------------------- -----------------------------------
  Owner                               Control total y configuración
                                      inicial

  Administrator                       Usuarios, bibliotecas, dispositivos
                                      y configuración permitida

  User                                Acceso a bibliotecas asignadas

  Restricted User                     Acceso limitado y restricciones de
                                      contenido
  -----------------------------------------------------------------------

Las restricciones de contenido de un Restricted User se definen con
`users.max_age_rating`, la edad máxima permitida. `age_rating` conserva
la clasificación original para mostrarla, y cada adaptador de metadatos
la normaliza a `media_items.min_age`, un número comparable entre
países. El adaptador de TMDB usa la clasificación de la región de
`metadata.language` y, si falta o no la reconoce, la de Estados Unidos;
PG y TV-PG cuentan como 10 años. Un elemento sin `min_age` queda oculto para estos usuarios, y un
Restricted User sin `max_age_rating` solo ve contenido para todos los
públicos. Un episodio sin clasificación propia usa la de su temporada o
su serie.

## 15.2 Autenticación

-   Contraseñas con Argon2id.
-   Creación del Owner protegida por un código de un solo uso que el
    servidor genera en el primer arranque y muestra en la consola, los
    logs o el instalador. Sin ese código, nadie que alcance el puerto
    puede apropiarse del servidor. El endpoint se deshabilita al crear
    el Owner.
-   Tokens de acceso de vida corta.
-   Refresh tokens rotatorios y revocables.
-   Sesiones vinculadas a dispositivos.
-   Códigos de vinculación de un solo uso y expiración corta. El
    dispositivo sin teclado pide el código, lo muestra y consulta
    `/auth/pairing/token` con un token propio; un usuario con sesión lo
    reclama en `/auth/pairing/claim`, y los tokens llegan solo al
    dispositivo que se vincula.
-   El estado del usuario y del dispositivo se consulta en cada petición.
    Revocar, desactivar o cambiar un rol surte efecto de inmediato, sin
    esperar a que caduque el token de acceso.
-   Recuperación de cuenta mediante un mecanismo local seguro definido
    por el administrador.
-   Registro de intentos de acceso.

## 15.3 Autorización

Comprobar autorización en cada operación: - Ver biblioteca. - Ver
elemento. - Leer archivo. - Obtener imagen. - Crear sesión. -
Seleccionar pista. - Acceder a manifiesto HLS. - Acceder a cada
segmento. - Consultar sesiones. - Administrar servidor.

La autorización debe comprobar el recurso asociado a la URL, no solo la
validez del token.

## 15.4 Transporte de credenciales

Un navegador no puede añadir la cabecera `Authorization` a `<img>`,
`<video>` ni al WebSocket, por lo que cada caso se define por separado:

  -----------------------------------------------------------------------
  Caso                                Mecanismo
  ----------------------------------- -----------------------------------
  API REST en clientes nativos        Cabecera `Authorization: Bearer`

  Cliente y panel web                 Cookie `HttpOnly`, `Secure` si hay
                                      TLS, `SameSite=Strict` y protección
                                      CSRF

  Imágenes                            Bearer en clientes nativos; cookie
                                      en la web

  WebSocket                           Bearer en clientes nativos; cookie
                                      y validación de `Origin` en la web

  Streaming (archivo, HLS y           Token opaco de la sesión en la ruta
  subtítulos)                         de `/stream`, con alcance limitado
                                      a esa sesión

  DLNA                                Sin credenciales; política de la
                                      sección 13.2
  -----------------------------------------------------------------------

El token de acceso nunca debe aparecer en una URL. Los segmentos usan
rutas relativas, por lo que heredan el token del manifiesto.

# 16. API REST

Base:

``` text
/api/v1
```

Todas las respuestas deben tener formato consistente y errores
estructurados.

## 16.1 Convenciones

-   JSON UTF-8.
-   IDs opacos (UUID recomendado).
-   Tiempos en ISO 8601 UTC.
-   Duraciones y posiciones en milisegundos.
-   Paginación por cursor o límite/offset definido.
-   Filtros explícitos.
-   Validación de parámetros.
-   OpenAPI como contrato fuente.
-   Compatibilidad hacia atrás dentro de una versión mayor.

## 16.2 Endpoints

### Autenticación

  Método   Ruta                    Propósito
  -------- ----------------------- -----------------------------
  POST     `/auth/setup`           Crear Owner con código inicial
  POST     `/auth/login`           Iniciar sesión
  POST     `/auth/refresh`         Renovar tokens
  POST     `/auth/logout`          Revocar sesión
  GET      `/auth/me`              Usuario actual
  POST     `/auth/pairing`         Crear código de vinculación
  POST     `/auth/pairing/claim`   Reclamar un código con sesión
  POST     `/auth/pairing/token`   Recoger tokens tras reclamar

### Servidor

  Método   Ruta                     Propósito
  -------- ------------------------ --------------------------
  GET      `/server/info`           Información pública
  GET      `/server/status`         Estado
  GET      `/server/capabilities`   Capacidades del servidor
  GET      `/server/version`        Versión

### Bibliotecas

  Método   Ruta                               Propósito
  -------- ---------------------------------- --------------------------------
  GET      `/libraries`                       Listar bibliotecas autorizadas
  POST     `/libraries`                       Crear biblioteca
  GET      `/libraries/{id}`                  Detalle
  PATCH    `/libraries/{id}`                  Actualizar
  DELETE   `/libraries/{id}`                  Eliminar
  GET      `/libraries/{id}/items`            Contenido
  POST     `/libraries/{id}/paths`            Añadir ruta
  DELETE   `/libraries/{id}/paths/{pathId}`   Quitar ruta
  POST     `/libraries/{id}/scan`             Iniciar escaneo
  GET      `/libraries/{id}/scan`             Estado de escaneo

### Medios

  Método   Ruta                          Propósito
  -------- ----------------------------- --------------------
  GET      `/media/{id}`                 Detalle
  GET      `/media/{id}/files`           Versiones/archivos
  GET      `/media/{id}/streams`         Pistas
  GET      `/media/{id}/subtitles`       Subtítulos
  GET      `/media/{id}/images/{type}`   Imagen
  GET      `/media/{id}/children`        Temporadas/episodios
  GET      `/media/{id}/matches`         Buscar coincidencias
  POST     `/media/{id}/identify`        Elegir coincidencia
  POST     `/media/{id}/refresh`         Forzar actualización
  PATCH    `/media/{id}/metadata`        Editar y bloquear
  GET      `/search`                     Búsqueda

### Reproducción

  Método   Ruta                             Propósito
  -------- -------------------------------- ---------------------------
  POST     `/playback/sessions`             Crear sesión
  GET      `/playback/sessions/{id}`        Consultar sesión
  PATCH    `/playback/sessions/{id}`        Actualizar estado
  POST     `/playback/sessions/{id}/tracks` Cambiar audio o subtítulos
  DELETE   `/playback/sessions/{id}`        Cerrar sesión
  PUT      `/playback/progress`             Guardar progreso
  GET      `/playback/continue-watching`    Continuar viendo
  GET      `/playback/history`              Historial

### Streaming

Estas rutas están fuera de `/api/v1` porque el cliente no las construye:
las recibe como URL opaca al crear la sesión.

  Método      Ruta                                                 Propósito
  ----------- ---------------------------------------------------- ---------------------------
  GET, HEAD   `/stream/session/{id}/{token}/file`                  Archivo original
  GET         `/stream/session/{id}/{token}/master.m3u8`           Manifiesto HLS
  GET         `/stream/session/{id}/{token}/subs.m3u8`             Pista de subtítulos de HLS
  GET         `/stream/session/{id}/{token}/subs.vtt`              WebVTT de esa pista
  GET         `/stream/session/{id}/{token}/subtitles/{id}.vtt`    Subtítulo para el cliente
  GET         `/stream/session/{id}/{token}/{path}`                Segmentos y pistas

### Usuario actual

  Método   Ruta                      Propósito
  -------- ------------------------- --------------------
  GET      `/users/me/preferences`   Consultar preferencias
  PUT      `/users/me/preferences`   Guardar preferencias
  PUT      `/users/me/password`      Cambiar contraseña propia

### Dispositivos

  Método   Ruta              Propósito
  -------- ----------------- --------------------------------
  GET      `/devices`        Listar
  GET      `/devices/{id}`   Detalle
  DELETE   `/devices/{id}`   Revocar
  PATCH    `/devices/{id}`   Actualizar nombre/preferencias

### Administración

  Método   Ruta                              Propósito
  -------- --------------------------------- --------------------------
  GET      `/admin/system`                   CPU, RAM y sistema
  GET      `/admin/sessions`                 Sesiones activas
  GET      `/admin/transcodes`               Trabajos FFmpeg
  POST     `/admin/transcodes/{id}/cancel`   Cancelar trabajo
  GET      `/admin/logs`                     Logs consultables
  GET      `/admin/storage`                  Almacenamiento
  GET      `/admin/settings`                 Configuración
  PATCH    `/admin/settings`                 Actualizar configuración
  GET      `/admin/system-check`             Ejecutar System Check
  GET      `/admin/users`                    Listar usuarios
  POST     `/admin/users`                    Crear usuario
  GET      `/admin/users/{id}`               Detalle de usuario
  PATCH    `/admin/users/{id}`               Actualizar usuario
  DELETE   `/admin/users/{id}`               Eliminar usuario
  GET      `/admin/users/{id}/permissions`   Consultar permisos
  PUT      `/admin/users/{id}/permissions`   Permisos por biblioteca
  GET      `/admin/backups`                  Listar copias
  POST     `/admin/backups`                  Crear copia
  POST     `/admin/backups/{id}/restore`     Restaurar copia

## 16.3 Contrato de creación de sesión

Solicitud conceptual:

``` json
{
  "media_item_id": "media-uuid",
  "media_file_id": "file-uuid",
  "device_id": "device-uuid",
  "client_profile": {
    "platform": "android",
    "containers": ["mp4", "mkv"],
    "video_codecs": ["h264", "hevc"],
    "audio_codecs": ["aac", "ac3"],
    "subtitle_formats": ["srt", "vtt"],
    "subtitle_delivery": ["sidecar", "hls"],
    "max_resolution": "1920x1080",
    "protocols": ["http", "hls"]
  },
  "selected_audio_stream_id": "stream-uuid",
  "selected_subtitle_id": "subtitle-uuid",
  "max_bitrate": 12000000,
  "max_resolution": "1920x1080",
  "preferred_protocol": "hls",
  "start_position_ms": 0
}
```

Respuesta conceptual:

``` json
{
  "session_id": "session-uuid",
  "status": "initializing",
  "playback_mode": "direct_play",
  "protocol": "http",
  "stream_url": "/stream/session/session-uuid/stream-token/file",
  "subtitle_mode": "sidecar",
  "subtitle_url": "/stream/session/session-uuid/stream-token/subtitles/subtitle-uuid.vtt",
  "idle_timeout_seconds": 90,
  "selected_audio_stream_id": "stream-uuid",
  "selected_subtitle_id": "subtitle-uuid"
}
```

Los perfiles y campos se deben formalizar en OpenAPI. La respuesta debe
indicar el motivo de transcodificación cuando no se utilice Direct Play.

## 16.4 Entrega de archivos

El endpoint de archivo debe soportar: - HTTP GET. - HTTP HEAD. - `Range`
y respuestas `206 Partial Content`. - `Content-Length` cuando sea
posible. - `Content-Type` correcto. - Validación de sesión y
autorización. - Cancelación de lectura cuando el cliente desconecte.

No devolver rutas físicas del servidor al cliente.

`Content-Type` sale de una tabla por contenedor, nunca de la petición. Las
respuestas llevan `Cache-Control: private, no-store`. El token prueba que
la URL es de la sesión, pero no basta: cada petición vuelve a comprobar
que el usuario sigue activo, que el dispositivo no está revocado y que el
usuario puede ver el elemento.

La comprobación se recuerda 15 segundos, porque un reproductor pide muchos
rangos. Si falla, la sesión se cierra y la URL responde
`SESSION_EXPIRED`.

# 17. WebSocket y eventos

Endpoint propuesto:

``` text
/api/v1/events
```

Eventos: - `session.created` - `session.updated` - `session.ended` -
`playback.progress` - `transcode.started` - `transcode.progress` -
`transcode.failed` - `library.scan.started` - `library.scan.progress` -
`library.scan.completed` - `library.updated` - `device.connected` -
`device.revoked` - `server.status`

Ejemplo:

``` json
{
  "event": "transcode.progress",
  "timestamp": "2026-11-02T15:45:00Z",
  "data": {
    "job_id": "job-uuid",
    "session_id": "session-uuid",
    "progress": 42.5,
    "status": "running"
  }
}
```

Los eventos son notificaciones; no sustituyen la API de consulta. El
cliente debe poder recuperar el estado mediante REST después de
reconectarse.

Cada conexión recibe solo lo que su usuario puede consultar por REST.
Los eventos de sesión y de progreso se limitan al propio usuario, los de
biblioteca a las bibliotecas autorizadas, y los de transcodificación y
estado del servidor a los administradores. Un evento sin destinatario no
llega a nadie.

- **Autenticación:** la conexión usa la misma credencial que REST, cookie o
  Bearer. Se acepta el `Origin` del propio servidor y los de
  `cors_origins`; un cliente nativo no envía `Origin`. Así un sitio ajeno
  no puede abrir la conexión con la cookie del usuario.
- **Revalidación:** la sesión se vuelve a validar cada minuto. Un
  dispositivo revocado, un usuario desactivado o un token caducado cierran
  la conexión con el código 1008, y el cliente se reconecta con un token
  renovado.
- **Clientes lentos:** a un cliente que no lee se le desconecta, en vez de
  frenar a quien publica.

# 18. Aplicaciones cliente

## 18.1 Matriz de plataformas

  Plataforma      UI                          Player
  --------------- --------------------------- -------------------------
  Android móvil   React Native                Media3
  iOS             React Native                AVPlayer
  Android TV      Cliente adaptado o nativo   Media3
  tvOS            Cliente nativo              AVPlayer/AVKit
  Web             React + TypeScript          HLS.js/nativo
  Windows         Web/Tauri opcional          Player compatible
  macOS           Web/Tauri opcional          Player compatible
  Linux           Web/Tauri opcional          Player compatible
  Samsung Tizen   Aplicación web TV           Player Tizen compatible
  LG webOS        Aplicación web TV           Player webOS compatible
  DLNA            No aplica                   Renderer del televisor

La cobertura de Tizen y webOS requiere pruebas por versión de sistema y
modelo de televisor. No se debe prometer compatibilidad universal por el
mero hecho de disponer de un cliente web.

## 18.2 Separación de capas cliente

``` text
Presentation
    |
Application / Use Cases
    |
Domain
    ├── Server Discovery
    ├── Library
    ├── Playback
    ├── Session
    └── Device
    |
Platform Adapters
    ├── REST Client
    ├── WebSocket Client
    ├── Native Player
    └── Secure Storage
```

## 18.3 Funciones de interfaz

-   Inicio.
-   Continuar viendo.
-   Películas.
-   Series.
-   Bibliotecas.
-   Búsqueda.
-   Detalle multimedia.
-   Selección de versión.
-   Selección de audio.
-   Selección de subtítulos.
-   Reproductor.
-   Historial.
-   Perfil.
-   Selección de servidor.
-   Configuración.

## 18.4 Experiencia de TV

-   Navegación completa con control remoto.
-   Foco visible.
-   Orden de foco predecible.
-   Acciones accesibles desde D-pad.
-   Textos legibles a distancia.
-   Gestión de back.
-   Reanudación de reproducción.
-   Selección de audio y subtítulos desde el reproductor.
-   Evitar interacciones que requieran pantalla táctil.

# 19. Panel administrativo

El panel web se integrará con el servidor y tendrá estas áreas:

  Sección       Funciones
  ------------- ----------------------------------
  Dashboard     Estado, sesiones y actividad
  Libraries     Bibliotecas, rutas y escaneos
  Metadata      Identificación y corrección
  Users         Usuarios, roles y permisos
  Devices       Dispositivos vinculados
  Sessions      Reproducciones activas
  Transcoding   Procesos, progreso y errores
  Hardware      CPU, RAM, GPU y aceleradores
  Network       IP, puertos, TLS y acceso remoto
  Storage       Discos, rutas y espacio
  Logs          Diagnóstico
  Settings      Configuración general
  Backups       Copias y restauración

## 19.1 Diagnóstico del servidor

Una herramienta «System Check» comprobará: - Versión y estado de
FFmpeg. - Versión y estado de FFprobe. - Códecs disponibles. - Prueba de
inicialización de aceleración. - Permisos de bibliotecas. - Espacio
temporal. - Estado de SQLite. - Disponibilidad de puertos. - Estado
mDNS/SSDP. - Certificado TLS. - Conectividad remota, si está
configurada.

# 20. Configuración y directorios

## 20.1 Directorios

``` text
MediaNode/
├── config/
│   └── server.yaml
├── data/
│   ├── medianode.db
│   ├── metadata/
│   ├── images/
│   ├── cache/
│   ├── thumbnails/
│   └── transcodes/
├── logs/
└── backups/
```

Las bibliotecas originales no se copian a esta estructura. Se registran
como rutas administradas por el usuario.

## 20.2 Configuración conceptual

``` yaml
server:
  name: "MediaNode"
  bind_address: "0.0.0.0"
  http_port: 8420
  https_enabled: true
  https_port: 8421
  trusted_proxies: []
  cors_origins: []
  shutdown_timeout_seconds: 30

database:
  path: "./data/medianode.db"
  journal_mode: "WAL"
  busy_timeout_ms: 5000

paths:
  metadata: "./data/metadata"
  images: "./data/images"
  cache: "./data/cache"
  transcodes: "./data/transcodes"
  logs: "./logs"
  backups: "./backups"

logging:
  level: "info"

scanner:
  workers: 2
  reconcile_interval_minutes: 60
  missing_file_retention_days: 30

metadata:
  provider: "tmdb"        # tmdb o none
  language: "es-ES"       # su región elige la clasificación por edades
  tmdb_api_key: ""        # o MEDIANODE_METADATA_TMDB_API_KEY

transcoding:
  max_concurrent_jobs: 1
  max_concurrent_light_jobs: 4
  segment_seconds: 6
  seek_restart_segments: 5
  hardware_acceleration: "auto"
  cleanup_after_minutes: 30
  max_disk_gb: 20
  tone_mapping: "auto"    # auto, realtime u off

sessions:
  heartbeat_timeout_seconds: 90
  progress_persist_interval_seconds: 30
  completed_threshold_percent: 90

discovery:
  mdns_enabled: true
  dlna_enabled: false

remote:
  enabled: false
  direct_enabled: false
  relay_enabled: false
  admin_enabled: false
```

Los valores son defaults ilustrativos. SSDP se activa junto con DLNA. El
instalador debe generar una configuración segura, escribir rutas
absolutas y validar que existan y tengan permisos, porque un servicio no
arranca necesariamente desde el directorio de instalación.

# 21. Instalación y distribución

## 21.1 Paquetes

  Sistema          Paquete
  ---------------- ----------------
  Windows          MSI o EXE
  Debian/Ubuntu    DEB
  Fedora/RHEL      RPM
  Linux genérico   Binario
  macOS            DMG o PKG
  Docker           Imagen oficial

## 21.2 Servicio del sistema

-   Windows Service.
-   systemd en Linux.
-   LaunchDaemon/servicio equivalente en macOS, según el modelo de
    distribución.

El servicio debe: - Iniciar automáticamente si el usuario lo
configura. - Ejecutar con permisos mínimos. - Mantener directorios de
datos persistentes. - Registrar logs. - Permitir apagado controlado. -
Recuperarse de fallos según política del sistema.

En Windows, un servicio no ve las unidades de red mapeadas por el
usuario. Las bibliotecas en red deben registrarse con rutas UNC y el
servicio debe ejecutarse con una cuenta que tenga acceso a ellas. System
Check debe detectar este caso.

## 21.3 Actualizaciones

-   Paquetes firmados cuando la plataforma lo permita.
-   Verificación de integridad.
-   Backup previo a migraciones.
-   Migraciones compatibles y reversibles cuando sea viable.
-   No borrar bibliotecas ni datos de usuario al desinstalar.
-   Canal estable y canal de pruebas.
-   Notas de versión.

## 21.4 Licencias y distribución

Antes de empaquetar FFmpeg, revisar la licencia de la compilación
exacta, bibliotecas habilitadas y obligaciones de redistribución. No
todas las builds de FFmpeg tienen las mismas condiciones.

También revisar licencias de: - Dependencias Go. - Bibliotecas del
cliente. - Fuentes tipográficas. - Recursos gráficos. - SDK de
plataformas. - Proveedores de metadatos e imágenes.

# 22. Observabilidad y soporte

## 22.1 Logs

Formato JSON con: - Timestamp. - Nivel. - Módulo. - Request ID. - User
ID cuando corresponda y sea seguro. - Session ID. - Job ID. - Código de
error. - Mensaje.

No registrar: - Contraseñas. - Tokens. - URLs de `/stream` completas,
porque contienen el token de sesión. - Datos sensibles innecesarios. -
Contenido de archivos.

## 22.2 Métricas

-   CPU y memoria del servidor.
-   Espacio libre.
-   Cantidad de archivos indexados.
-   Duración de escaneos.
-   Sesiones activas.
-   Trabajos FFmpeg activos.
-   Errores de transcodificación.
-   Bitrate aproximado por sesión.
-   Conexiones locales/remotas.
-   Latencia de endpoints.

## 22.3 Códigos de error

Definir códigos estables: - `INTERNAL_ERROR` - `NOT_FOUND` -
`METHOD_NOT_ALLOWED` - `VALIDATION_ERROR` - `RATE_LIMITED` - `FORBIDDEN`
- `CSRF_INVALID` - `USERNAME_TAKEN` - `AUTH_REQUIRED` -
`AUTH_TOKEN_EXPIRED` - `AUTH_SETUP_COMPLETED` - `AUTH_PAIRING_INVALID` -
`AUTH_INVALID_CREDENTIALS` -
`AUTH_DEVICE_REVOKED` - `PERMISSION_LIBRARY_DENIED` -
`METADATA_PROVIDER_UNAVAILABLE` - `METADATA_LOCKED` - `MEDIA_NOT_FOUND` - `MEDIA_FILE_UNAVAILABLE` - `PLAYBACK_UNSUPPORTED` -
`TRANSCODE_CAPACITY_EXCEEDED` - `TRANSCODE_INITIALIZATION_FAILED` -
`SUBTITLE_UNSUPPORTED` - `LIBRARY_PATH_UNAVAILABLE` - `SCAN_FAILED` -
`SESSION_EXPIRED` - `SERVER_RESTART`

Los mensajes para usuarios no deben revelar rutas internas ni detalles
sensibles del sistema.

# 23. Pruebas y criterios de calidad

## 23.1 Pruebas unitarias

-   Autorización por biblioteca.
-   Selección de modo de reproducción.
-   Validación de perfiles de cliente.
-   Gestión de sesiones.
-   Progreso.
-   Identificación de medios.
-   Normalización de rutas.
-   Gestión de errores FFmpeg.

## 23.2 Pruebas de integración

-   SQLite y migraciones.
-   Scanner y FFprobe.
-   API REST.
-   WebSockets.
-   Creación y cancelación de trabajos.
-   Seek hacia adelante y hacia atrás durante la transcodificación.
-   Reconciliación al arrancar tras una caída.
-   Archivos movidos, ausentes y volúmenes desconectados.
-   Entrega HTTP con Range.
-   HLS y protección de segmentos.
-   Descubrimiento mDNS.
-   SSDP y endpoints DLNA.

## 23.3 Matriz de contenido

Probar, como mínimo: - MP4/H.264/AAC. - MKV/H.264/AAC. - MKV/HEVC. -
Video con múltiples pistas de audio. - SRT externo. - SRT interno. -
ASS/SSA. - PGS y VobSub. - Audio AC3/DTS. - Archivos de alta
resolución. - HDR, incluido el tone mapping a SDR, cuando el hardware y
los clientes disponibles lo permitan. - Un archivo con varios
episodios. - Archivos incompletos o corruptos.

## 23.4 Pruebas de plataforma

-   Android móvil.
-   iOS.
-   Android TV.
-   tvOS.
-   Navegadores principales.
-   Windows.
-   macOS.
-   Linux.
-   Modelos seleccionados de Samsung y LG.
-   Renderers DLNA de fabricantes diferentes.

## 23.5 Criterios de aceptación del MVP

1.  Instalación del servidor sin herramientas de desarrollo.
2.  Configuración inicial desde navegador.
3.  Creación y escaneo de bibliotecas.
4.  Extracción técnica mediante FFprobe.
5.  Catálogo básico de películas y series.
6.  Login y permisos.
7.  Reproducción Direct Play.
8.  Direct Stream cuando corresponda.
9.  Transcodificación de un perfil compatible.
10. Selección de pistas de audio y subtítulos.
11. Guardado y recuperación de progreso.
12. Dos clientes con sesiones independientes.
13. Reinicio del servidor sin pérdida de catálogo ni progreso
    confirmado.
14. Logs útiles para diagnosticar fallos.

# 24. Roadmap de implementación

## Fase 1 --- Core Server

Entregables: - Repositorio Go. - Configuración y lifecycle. - SQLite y
migraciones con backup previo. - API REST. - Autenticación inicial y
creación protegida del Owner. - Usuarios y permisos. - Bibliotecas y
rutas. - Scanner. - FFprobe. - Catálogo básico, con modelo de series. -
Metadatos e imágenes. - Panel administrativo inicial.

**Salida:** servidor capaz de indexar y administrar bibliotecas.

## Fase 2 --- Streaming Engine

Entregables: - Playback Manager. - Perfiles de capacidades. - Sesiones
y reconciliación al arrancar. - Progreso. - Direct Play. - Direct
Stream. - Transcoding Manager. - FFmpeg. - HLS con seek. - Subtítulos. -
Límites de recursos. - Empaquetado inicial: imagen Docker y binario que
no requieran herramientas de desarrollo.

**Salida:** reproducción estable desde un cliente de prueba.

## Fase 3 --- Clientes principales

Entregables: - Web Client. - Android. - iOS. - Android TV. -
Descubrimiento. - Vinculación de dispositivos. - Sincronización de
progreso.

**Salida:** ecosistema principal de clientes.

## Fase 4 --- Red y compatibilidad

Entregables: - DLNA/UPnP. - Remote Direct. - Diseño e implementación de
Remote Relay opcional. - tvOS. - Samsung Tizen. - LG webOS. -
Aplicaciones de escritorio, si la validación lo justifica.

**Salida:** compatibilidad ampliada y acceso remoto.

## Fase 5 --- Producto completo

Entregables: - Perfiles familiares. - Restricciones de contenido. -
Historial avanzado. - Recomendaciones. - Descargas offline, si se
aprueba el alcance. - Panel avanzado. - Backup y restore desde el
panel. - Actualizaciones. - Instaladores firmados. - Documentación de usuario y
administración.

**Salida:** versión preparada para distribución.

# 25. Riesgos técnicos y mitigaciones

  -----------------------------------------------------------------------
  Riesgo                  Impacto                 Mitigación
  ----------------------- ----------------------- -----------------------
  Incompatibilidad de     Reproducción fallida    Perfiles y pruebas por
  códecs                                          dispositivo

  Saturación de CPU/GPU   Buffering               Límites de trabajos y
                                                  perfiles

  Fallos de hardware      Transcode fallido       Prueba real y fallback
  acceleration                                    

  SQLite bloqueada        Errores de escritura    WAL, transacciones
                                                  cortas, un escritor
                                                  lógico

  Discos desconectados    Archivos no disponibles Estado de rutas y
                                                  reconciliación

  mDNS no cruza subredes  Discovery fallido       Entrada manual y
                                                  pairing

  DLNA inconsistente      Compatibilidad parcial  Matriz de renderers
                                                  probados

  Remote Direct           Sin acceso remoto       Diagnóstico y relay
  inaccesible                                     opcional

  Costos de relay         Costos operativos       Relay opcional y
                                                  límites

  Proveedor de metadatos  Catálogo incompleto     Caché local y proveedor
  caído                                           desacoplado

  Segmentos expuestos     Acceso no autorizado    Tokens de sesión y
                                                  autorización por
                                                  recurso

  FFmpeg mal invocado     Riesgo de ejecución     Argumentos separados,
                                                  validación y permisos
                                                  mínimos
  -----------------------------------------------------------------------

# 26. Decisiones pendientes de validación

La arquitectura define la dirección, pero estos puntos necesitan pruebas
antes de congelar los contratos de producción:

1.  **Router Go:** confirmar Chi como implementación final.
2.  **Perfiles de streaming:** seleccionar codecs, bitrates, presets y
    segmentos mediante benchmarks.
3.  **HLS:** validar fMP4/CMAF con cada reproductor objetivo.
4.  **Metadatos:** cerrado en el ADR 0005. TMDB, con una clave por
    instalación, atribución en el panel, renovación antes de seis meses e
    imágenes sin modificar. Falta el logotipo de TMDB en el panel.
5.  **Relay:** definir proveedor, protocolo, costos, capacidad y
    política de privacidad.
6.  **Tizen/webOS:** validar SDK, versiones objetivo y capacidades
    reales de reproducción.
7.  **Desktop:** decidir si basta el cliente web o si Tauri aporta
    suficiente valor.
8.  **Identificación de series:** validar las reglas de nombres,
    especiales y numeración absoluta. El modelo de datos queda cerrado
    en la sección 6.
9.  **Backups:** definir retención, cifrado y restauración.
10. **Licencias multimedia:** auditar la build exacta de FFmpeg y sus
    dependencias.
11. **Driver SQLite:** confirmar con benchmarks que `modernc.org/sqlite`
    rinde lo suficiente frente a un driver con CGO.
12. **Seek con video copiado:** definir cómo generar y guardar el índice
    de keyframes para la playlist sintética.
13. **TLS en LAN:** validar el certificado propio y su fijación con cada
    cliente, y la experiencia en navegadores y televisores.

# 27. Orden recomendado de construcción

No desarrollar todos los clientes en paralelo. El orden recomendado es:

1.  Crear el esqueleto del repositorio Go.
2.  Implementar configuración, logging y lifecycle.
3.  Implementar SQLite, migraciones y SQLC.
4.  Implementar usuarios, autenticación y permisos.
5.  Implementar bibliotecas y rutas.
6.  Implementar Scanner y FFprobe.
7.  Implementar metadatos y catálogo.
8.  Implementar API de lectura.
9.  Implementar sesiones, progreso y reconciliación al arrancar.
10. Implementar Playback Manager.
11. Implementar Direct Play y HTTP Range.
12. Implementar Direct Stream.
13. Implementar Transcoding Manager, HLS y seek.
14. Implementar subtítulos.
15. Crear cliente web de prueba y empaquetado inicial.
16. Incorporar Android y iOS.
17. Incorporar Android TV y después el resto de plataformas.
18. Incorporar DLNA y acceso remoto.
19. Crear y probar los instaladores definitivos.

Las sesiones van antes de la entrega de medios porque toda URL de
`/stream` y todo trabajo de transcodificación dependen de una sesión.

# 28. Referencias técnicas oficiales

-   Go: https://go.dev/doc/
-   Chi Router: https://github.com/go-chi/chi
-   SQLC: https://docs.sqlc.dev/
-   Goose: https://github.com/pressly/goose
-   SQLite WAL: https://www.sqlite.org/wal.html
-   SQLite Backup API: https://www.sqlite.org/backup.html
-   FFmpeg: https://ffmpeg.org/documentation.html
-   FFprobe: https://ffmpeg.org/ffprobe.html
-   Android Media3: https://developer.android.com/media/media3
-   Apple AVFoundation:
    https://developer.apple.com/documentation/avfoundation
-   Apple HTTP Live Streaming:
    https://developer.apple.com/documentation/http-live-streaming
-   React Native: https://reactnative.dev/docs/getting-started
-   React: https://react.dev/
-   Tauri: https://tauri.app/
-   mDNS/DNS-SD: https://www.rfc-editor.org/rfc/rfc6762
-   DNS-Based Service Discovery: https://www.rfc-editor.org/rfc/rfc6763
-   UPnP:
    https://openconnectivity.org/developer/specifications/upnp-resources/upnp/

------------------------------------------------------------------------

# 29. Definición final del producto

MediaNode será un servidor multimedia autoalojado, modular y
multiplataforma, con un núcleo Go, base SQLite, procesamiento FFmpeg,
API REST/WebSocket y clientes específicos por plataforma.

La primera versión priorizará:

-   Instalación sencilla.
-   Indexación confiable.
-   Catálogo de películas y series.
-   Reproducción Direct Play.
-   Transcodificación controlada.
-   Subtítulos y selección de audio.
-   Usuarios y permisos.
-   Cliente web.
-   Persistencia de progreso.

El acceso remoto avanzado, relay, Smart TV de fabricantes y funciones
familiares se incorporarán por fases, sin comprometer la autonomía del
servidor.

**Principio rector:** MediaNode debe ser local-first, seguro, eficiente
y extensible. El servidor debe seguir siendo útil aunque no haya
conexión a Internet; los servicios externos deben ampliar la
experiencia, nunca convertirse en requisito para reproducir la
biblioteca local.

------------------------------------------------------------------------

# 30. Sugerencias técnicas y consideraciones de implementación

A partir del análisis técnico del documento maestro, se recomiendan las siguientes optimizaciones específicas de ingeniería para la fase de construcción:

### 30.1 Estrategia de Persistencia de Heartbeats en Memoria (Debouncing)
En la **Sección 12.2** se menciona la reducción de escrituras en SQLite durante la reproducción. Se sugiere implementar un mecanismo de caché en memoria (*write-behind pattern*):
* Mantener las posiciones de reproducción activas en un `sync.Map` o estructura en memoria dentro del `Session Manager`.
* Volcar las posiciones a SQLite únicamente cada 30 segundos (intervalo configurable) o de forma inmediata ante eventos de cambio de estado (`PAUSED`, `STOPPED`, `COMPLETED`, `EXPIRED`) y durante el apagado controlado del servidor. Un cierre abrupto del cliente se detecta por el timeout de inactividad de la sección 12.4.
* Esto reduce la sobrecarga de I/O en SQLite en más de un 80% cuando hay múltiples streams concurrentes en la red.

### 30.2 Manejo de Capacidad de Transcodificación (`TRANSCODE_CAPACITY_EXCEEDED`)
Para evitar el colapso por recursos de hardware en dispositivos host de gama baja o media:
* Implementar el control de admisión de la sección 10.6 en el `Transcoding Manager`, con contadores por tipo de trabajo (`max_concurrent_jobs` y `max_concurrent_light_jobs`). Un semáforo ponderado solo se incorporará si los benchmarks demuestran que hace falta.
* La reserva se hace antes de crear la sesión. Si no hay cupo, devolver `503 Service Unavailable` con `Retry-After` y el código estructurado `TRANSCODE_CAPACITY_EXCEEDED`.
* La respuesta debe indicar el motivo de la transcodificación. Si se debe únicamente a `max_bitrate` o `max_resolution`, el cliente puede ofrecer reproducir el original sin ese límite. En los demás casos, solo puede ofrecer reintentar o detener otra reproducción, porque el servidor ya descartó Direct Play y bajar la calidad también requiere transcodificar.

### 30.3 Estrategia de Subtítulos SRT "Client-Side First"
Para reducir la carga computacional en el servidor:
* Si el subtítulo es de texto, la API debe preferir entregarlo sin transcodificar el video, según `subtitle_delivery`: como archivo independiente o como pista WebVTT dentro de HLS. Los subtítulos internos se extraen una vez y se guardan en caché (sección 11.2).
* Delegar el renderizado (*Client-Side Subtitle Rendering*) a Media3 (`SubtitleView` en Android) o a AVPlayer en iOS, que requiere la pista dentro de HLS.
* Reservar la recodificación con subtítulos incrustados (*Burn-in Subtitles*) como última opción: subtítulos de imagen como PGS, y clientes DLNA o Smart TVs sin soporte de subtítulos independientes.

### 30.4 Gestión de Segmentos HLS Temporales en Disco
* Configurar una rutina de limpieza (*Garbage Collector*) en el `Transcoding Manager` que elimine segmentos `.m4s` o `.ts` de sesiones terminadas (`COMPLETED`, `STOPPED`, `EXPIRED` o `FAILED`). Los directorios huérfanos tras una caída se eliminan con la reconciliación de la sección 12.5.
* Permitir que `paths.transcodes` apunte a un montaje `tmpfs` dedicado cuando el host disponga de RAM suficiente, prolongando la vida útil de los discos SSD. No usar `/tmp` por defecto: no siempre es `tmpfs` y el sistema puede limpiarlo. Una película transcodificada puede ocupar varios GB, por lo que debe aplicarse la cuota de la sección 10.6.