# MediaNode

**English** · [Español](README.es.md)

A self-hosted media server written in Go. Point it at your movie and TV folders,
and it indexes them, identifies what they are, and plays them in a web panel or
any client that speaks its REST API. One static binary, SQLite for storage, no
external services required.

> **Status: alpha.** It works end to end on Windows and in Docker (Linux), but it is
> early software.
> Expect rough edges, breaking changes and missing features (see
> [What works today](#what-works-today)). Do not expose it to the Internet.

**Looking for help.** It has only been tried on Windows and Docker, and DLNA has never
run on a real TV. If you have a smart TV, a Mac, a Linux box or a GPU, a quick test
and a report is the most useful contribution: see
[Status and where help is wanted](#status-and-where-help-is-wanted) and
[`CONTRIBUTING.md`](CONTRIBUTING.md).

## What works today

- **Libraries.** Movie and TV libraries with one or more folders. A scan reads
  every file with FFprobe, detects moved and renamed files without re-reading
  them, and keeps watching the folders for changes.
- **Identification from file names.** `Movie (1999).mkv`, `Show.S01E02.mkv`,
  `1x03`, multi-episode files, and loose numbering such as
  `Show 01 - Title.mp4`. Works offline.
- **Metadata from TMDB (optional).** Titles, synopses, genres, cast, posters,
  backdrops and age ratings, with manual search, identification and locking.
  Without an API key the catalog still works from file names.
- **Catalog and search.** Cursor-paginated browsing, accent-insensitive search,
  seasons and episodes, files, tracks (codec, HDR, languages) and subtitles.
- **Users and permissions.** Owner, administrators, users and age-restricted
  profiles, with per-library `view`, `scan` and `manage` permissions. Access
  tokens with rotating refresh tokens, linked devices and pairing codes for TVs.
- **Playback sessions.** Progress, continue watching, history, resume after a
  crash, and real-time events over WebSocket.
- **Direct Play.** The original file is served with HTTP `Range`, so seeking
  works. A playback decision engine already works out when a client would need
  remuxing or transcoding.
- **Direct Stream and transcoding over HLS.** A file the browser cannot play
  as is gets remuxed (video copied into MPEG-TS segments) or re-encoded to
  H.264/AAC (fMP4 segments) while you watch, with free seeking. Concurrent jobs
  are limited (`transcoding.max_concurrent_jobs`), the server answers `503` with
  `Retry-After` when it is full, and FFmpeg never outlives the server on
  Windows and Linux.
- **Subtitles and audio.** Embedded and external text subtitles (SRT, ASS,
  WebVTT) are converted to WebVTT, delivered next to the video or inside HLS, or
  burned into the picture when the client cannot show them. Surround audio is
  mixed down for stereo clients. Audio and subtitles can be switched while
  playing, resuming at the same position.
- **HDR to SDR.** An HDR video is converted for clients that cannot show it.
  This is expensive in software: the server measures its speed at startup and
  `transcoding.tone_mapping` (`auto`, `realtime`, `off`) decides what to do
  when the machine cannot keep up.
- **Web panel.** A React panel embedded in the binary: first-run wizard,
  libraries, users, metadata, search and a player (Direct Play and HLS through
  hls.js) with audio and subtitle selectors.
- **DLNA.** Off by default, published per library (`discovery.dlna_enabled` and a
  switch on each library in the panel, with a warning: TVs have no password, so
  anyone on the local network sees what you publish). SSDP discovery, a UPnP
  MediaServer with ContentDirectory and ConnectionManager, the original file
  with `Range`, or MPEG-TS (copying what it can, with seeking and burned-in
  subtitles) when the TV cannot play it. It only answers the local network.
- **Remote access.** Off by default: the server only answers the local network.
  `remote.direct_enabled` accepts connections from outside with your own
  certificate (`server.https_enabled`) or behind a reverse proxy
  (`server.trusted_proxies`); the admin panel stays closed to them unless
  `remote.admin_enabled` says otherwise. A per-user remote bitrate limit applies,
  and the panel can test reachability from outside the LAN.

## Status and where help is wanted

MediaNode is **alpha**. It works end to end on Windows over a LAN (the 14 MVP
criteria pass on a clean Windows binary and on Docker; CI is green on Windows,
Linux and macOS), but a lot is untested on real hardware. Honest list:

**Needs testing on real hardware (the most useful help)**

- [ ] **DLNA on real TVs and players.** Only tested against our own control
      point (`scripts/netcheck`). The compatibility matrix in
      [`docs/dlna-renderers.md`](docs/dlna-renderers.md) is empty: results for
      any Samsung, LG, Sony, Android TV, VLC, Kodi, etc. are very welcome.
- [ ] **Remote access from outside the LAN** with a real domain, port forward or
      reverse proxy (only simulated so far).
- [ ] **macOS**: the SSDP multicast search got no answer on the CI runner; the
      rest of DLNA works there. FFmpeg may also stay alive if the server is
      killed.
- [ ] **Linux with systemd**: the unit in `packaging/` has never run on a real
      machine.
- [ ] **Browsers**: Firefox and Safari (native HLS) are untested; Chrome and Edge
      are verified.
- [ ] **Hardware encoders** (NVENC, QSV, AMF, VAAPI, VideoToolbox): detected but
      never run.
- [ ] **Image subtitles** (PGS, VobSub) burned into the picture: implemented, never
      run on a real track.
- [ ] **HDR to SDR**: on the development machine (an old 4-core i5) with the
      Windows FFmpeg build it runs at about a third of real time.

**Not implemented yet**

- [ ] Mobile apps (Android, iOS) and TV apps (Android TV, tvOS, Tizen, webOS):
      phases 11 and 12 of [`plan_de_trabajo.md`](plan_de_trabajo.md).
- [ ] mDNS discovery of MediaNode and the generated LAN certificate.
- [ ] Installers (MSI, DEB, RPM, PKG), a Windows service, and signed releases;
      the release workflow has never been run on a tag.
- [ ] Backups and editable settings from the panel (settings are read-only).
- [ ] Remote Relay (only if there is demand).
- [ ] DLNA: `DLNA.ORG_PN` profiles (left out on purpose: a wrong one makes strict
      TVs reject the item), `X_MS_MediaReceiverRegistrar` (Windows Media Player,
      Xbox) and `PrepareForConnection`.
- [ ] Family profiles, age restrictions in the catalog, recommendations.

To help, see [`CONTRIBUTING.md`](CONTRIBUTING.md): it explains how to test DLNA on your
TV and has issue templates for the results. The decisions behind the code are in
[`docs/adr/`](docs/adr).

## Install

No development tools needed. Pick one (details in [`packaging/`](packaging)):

- **Binary.** Download the archive for your system (Windows, Linux or macOS, `amd64`
  or `arm64`), check it against `SHA256SUMS`, unpack it and run `medianode-server`.
  Install [FFmpeg](https://ffmpeg.org/download.html) yourself: it is a separate
  program and is not included (see
  [`docs/licencias-ffmpeg.md`](docs/licencias-ffmpeg.md)).
- **Docker.** `docker compose up -d --build`, after editing the paths in
  [`docker-compose.yml`](docker-compose.yml). The image brings FFmpeg.
- **systemd.** A hardened unit and a configuration for a Linux service.

Then open <http://127.0.0.1:8420>, enter the one-time setup code the log prints and
create the owner account.

The 14 MVP criteria are checked by `scripts/acceptance` against a running server;
they pass on a clean Windows binary and on the Docker image. HLS playback was
checked in Edge and Chrome. Not yet tried: macOS, a real systemd, Firefox and Safari.

## Build from source

### Requirements

- [Go](https://go.dev/dl/) 1.27 or newer
- [Node.js](https://nodejs.org/) 20 or newer, only to build the web panel
- [FFmpeg](https://ffmpeg.org/download.html) with `ffprobe`, on your `PATH` or
  next to the server binary. `ffprobe` is required to scan.

The server builds without CGO, so no C compiler is needed.

### Quick start

```bash
git clone https://github.com/elias-espino/medianode.git
cd medianode

# Build the web panel and the server (the panel is embedded in the binary).
make build

# Or, by hand:
#   cd web && npm ci && npm run build && cd ..
#   go build -o bin/ ./cmd/...
```

Create a configuration file (every setting has a default, so this is optional):

```bash
mkdir -p config
cp config/server.example.yaml config/server.yaml
```

Start the server:

```bash
./bin/medianode-server --config config/server.yaml
```

On the first run the log prints a **one-time setup code**:

```
servidor sin configurar: usa este código en /auth/setup
```

Open <http://127.0.0.1:8420>, enter that code and create the owner account, then
add a library and point it at a folder on the machine running the server. Data,
logs and backups are stored next to the configuration file.

### Configuration

Relative paths start at the install root, the folder that contains `config/`.
Any key can be set with an environment variable: `server.http_port` becomes
`MEDIANODE_SERVER_HTTP_PORT`. The full list is in
[`config/server.example.yaml`](config/server.example.yaml).

| Setting | Default | What it does |
|---|---|---|
| `server.bind_address` | `0.0.0.0` | Interface to listen on. Use `127.0.0.1` for this machine only |
| `server.http_port` | `8420` | HTTP port |
| `metadata.tmdb_api_key` | empty | TMDB key. Prefer `MEDIANODE_METADATA_TMDB_API_KEY` |
| `metadata.language` | `es-ES` | Language of metadata and country of the age rating |
| `scanner.workers` | `2` | Files analysed in parallel |
| `scanner.missing_file_retention_days` | `30` | Days a vanished file is kept before it is purged |
| `sessions.completed_threshold_percent` | `90` | Progress at which something counts as watched |

### TMDB metadata

Get a free API key at <https://www.themoviedb.org/settings/api>. Each
installation uses its own key. Do not commit it:

```bash
# Linux and macOS
export MEDIANODE_METADATA_TMDB_API_KEY="your-key"
# Windows PowerShell
$env:MEDIANODE_METADATA_TMDB_API_KEY = "your-key"
```

If a title is not matched automatically, open **Metadata** in the panel and
identify it by hand.

### Use it from another device

Set `server.bind_address: "0.0.0.0"` and allow the port through your firewall.
Then open `http://<server-ip>:8420` from a device on the same network.

There is no TLS yet, so passwords travel in clear text. Use it on a trusted
home network only, and never forward the port from your router.

## Development

```bash
make test          # go test ./...
go test -short ./...   # skip the slow tests (10 000-file scan, real FFmpeg)
make generate      # sqlc generate, after editing queries/ or migrations/
make lint          # golangci-lint
make cross         # cross-compile for Windows, Linux and macOS (amd64, arm64)

cd web && npm run dev   # panel with hot reload, proxying /api to :8420
```

Tests that need FFmpeg or the generated sample videos skip themselves when
they are missing. `go run ./scripts/gen-fixtures` generates the videos.

The API contract is [`docs/openapi.yaml`](docs/openapi.yaml), and a test fails if
a route is not documented there.

### Project layout

```
cmd/medianode-server   server entry point
cmd/medianode-cli      placeholder for a future command-line tool
internal/
  scanner  probe       indexing, file watching, FFprobe
  metadata images      TMDB, the enrichment queue, image cache
  readmodel            catalog and search queries
  playback protocol    playback decision and client profiles
  sessions events      sessions, progress and the event bus
  ffmpeg transcoding   FFmpeg commands, HLS and jobs
  auth authorization   identity and permissions
  api                  HTTP handlers, routes, WebSocket
  database             SQLite, migrations and sqlc code
migrations/            goose migrations
queries/               SQL for sqlc
web/                   React + TypeScript panel
docs/                  OpenAPI, architecture document, decision records
```

## Documentation

The project is built in phases, each verified before the next one starts.

- [`plan_de_trabajo.md`](plan_de_trabajo.md): the work plan, with what was
  decided, verified and left pending in every phase
- [`docs/documento_maestro_de_arquitectura.md`](docs/documento_maestro_de_arquitectura.md):
  the architecture and design document
- [`docs/adr/`](docs/adr): architecture decision records
- [`packaging/`](packaging): installing as a binary, with Docker or with systemd
- [`docs/licencias-ffmpeg.md`](docs/licencias-ffmpeg.md): what the FFmpeg license
  means for distributing MediaNode (in Spanish)

The plan and the architecture document are written in Spanish.

## Contributing

It is an early alpha and the design is still moving, so please open an issue to
discuss a change before sending a pull request.

## License

[MIT](LICENSE) © 2026 Elias Espino.

This product uses TMDB and the TMDB APIs but is not endorsed, certified, or
otherwise approved by TMDB. MediaNode runs FFmpeg as a separate program; check
the license of the FFmpeg build you install.
