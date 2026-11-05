# Contributing to MediaNode / Contribuir a MediaNode

MediaNode is an early alpha. The most useful help right now is **testing on real
hardware** and reporting what happens, even if the report is "it does not work".
La ayuda más útil ahora es **probar con hardware real** y contar qué pasa, aunque
sea «no funciona».

## Ways to help / Formas de ayudar

1. **Test DLNA on a real TV or player** and open a
   [DLNA test result](../../issues/new?template=dlna-test.yml). The matrix lives in
   [`docs/dlna-renderers.md`](docs/dlna-renderers.md).
2. **Test on another system**: macOS, Linux with systemd, Firefox, Safari,
   hardware encoders (NVENC, QSV, AMF, VAAPI), image subtitles (PGS, VobSub), HDR.
   Open a [platform test report](../../issues/new?template=platform-test.yml).
3. **Report a bug** with the [bug template](../../issues/new?template=bug.yml).
4. **Send code**: see below. Open an issue first; the design is still moving.

## How to test DLNA / Cómo probar DLNA

1. Install FFmpeg and run `medianode-server` (see the README).
2. In `config/server.yaml` set `discovery.dlna_enabled: true`.
3. In the panel: **Libraries → Publish via DLNA** on one library.
4. Allow **UDP 1900** and **TCP 8420** on your local network firewall. On Docker,
   discovery needs `network_mode: host` (Linux only).
5. On the TV, look for media servers / DLNA sources. It should show your
   `server.name`.
6. Try a compatible MP4 (H.264 + AAC), an MKV, an HEVC file and a file with an
   external `.srt`; try seeking. In **Administration → Sessions** you can see which
   mode each play used (`direct_play`, `direct_stream`, `transcode`).
7. Report the result, including the server log lines mentioning `dlna`.

Do not paste passwords, setup codes or your TMDB key. Logs are scrubbed of secrets,
but check before posting.

## Development / Desarrollo

```bash
go build ./...
go vet ./...
go test ./...                      # the playback tests need FFmpeg and skip without it
golangci-lint run ./...
npm --prefix web ci && npm --prefix web test
```

- Go 1.27 or newer; no CGO needed to build. `go test -race` needs CGO and a C compiler.
- `make fixtures` generates the test videos (needs FFmpeg).
- After changing `queries/*.sql` run `sqlc generate`; after changing `docs/openapi.yaml`
  run `npm --prefix web run gen:api` (CI checks both are up to date).
- `scripts/netcheck` runs the DLNA and remote-access checks against a running server;
  CI runs it on Ubuntu and macOS.
- Decisions and their reasons are in [`docs/adr/`](docs/adr); the plan, and what is
  verified or not, is in [`plan_de_trabajo.md`](plan_de_trabajo.md) (Spanish).

## Pull requests

- One change per pull request, with tests. A bug fix comes with a test that fails
  without it.
- Keep the honesty of the docs: if something is not tested on real hardware, say so.
- Comments and documentation are written in Spanish in this repository; English is
  fine in issues and pull request descriptions.
- By contributing you agree your work is licensed under the MIT license.
