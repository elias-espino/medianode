# Empaquetado

Formas de instalar MediaNode sin herramientas de desarrollo. Todas dan el mismo
servidor, con el panel incrustado.

| Forma | Cuándo | Estado |
|-------|--------|--------|
| [Binario](#binario) | Windows, Linux y macOS, lo más simple | Verificado en Windows; el de Linux arranca en WSL |
| [Docker](#docker) | Servidor siempre encendido, sin instalar FFmpeg a mano | Ver «Estado» |
| [systemd](#systemd) | Linux como servicio | Sin probar en un Linux real |

FFmpeg y FFprobe son un programa aparte. Si usas el binario, instálalos tú (el
servidor los busca junto a su ejecutable y en el `PATH`). La imagen de Docker
los trae. **Antes de distribuir una build de FFmpeg junto a MediaNode, lee
[docs/licencias-ffmpeg.md](../docs/licencias-ffmpeg.md).**

## Binario

1. Descarga el archivo de tu sistema desde la página de versiones y comprueba su
   suma con `SHA256SUMS`.
2. Descomprímelo en una carpeta tuya. Dentro hay `medianode-server`, los README,
   la licencia y `config/server.example.yaml`.
3. (Opcional) copia `config/server.example.yaml` a `config/server.yaml` y
   edítalo. Sin archivo, el servidor usa los valores por defecto.
4. Ejecuta `medianode-server`. Los datos, los logs y las copias se guardan junto a
   la carpeta `config/`.
5. Abre <http://127.0.0.1:8420> y usa el código inicial que imprime el log
   (`usa este código en /auth/setup`) para crear la cuenta propietaria.

## Docker

```bash
docker compose up -d --build
docker compose logs medianode | grep "usa este código"
```

Edita las rutas de `docker-compose.yml` antes: la carpeta de datos
(`./medianode`), y las de tus películas y series. En las bibliotecas del panel se
usan las rutas **de dentro del contenedor** (por ejemplo `/media/peliculas`).

- La imagen se ejecuta como el usuario `10001`, sin privilegios. Si montas una
  carpeta de datos del equipo, tiene que pertenecerle
  (`sudo chown -R 10001 medianode`) o fijar tu propio usuario con `user:`.
- `network_mode: host` solo funciona en Linux. En Docker Desktop (Windows o macOS)
  usa `ports: ["8420:8420"]`.
- La imagen no se construye si el FFmpeg de Alpine no trae `ffprobe` y `libx264`,
  o si es una build con `--enable-nonfree`.

## systemd

```bash
sudo install -m 0755 medianode-server /usr/local/bin/medianode-server
sudo useradd --system --home-dir /var/lib/medianode --shell /usr/sbin/nologin medianode
sudo install -D -m 0640 -o root -g medianode packaging/server.packaged.yaml /etc/medianode/server.yaml
sudo install -m 0644 packaging/systemd/medianode.service /etc/systemd/system/medianode.service
sudo systemctl daemon-reload
sudo systemctl enable --now medianode
journalctl -u medianode | grep "usa este código"
```

El servicio corre sin privilegios, con el sistema de archivos en solo lectura salvo
`/var/lib/medianode` y `/var/log/medianode`. Si tus bibliotecas pertenecen a otro
usuario, añade `medianode` a su grupo (`sudo usermod -aG <grupo> medianode`).

## Red: puertos, DLNA y acceso desde Internet

Por defecto el servidor solo responde a la red local. Qué abrir en el firewall:

| Para | Puerto | Hacia |
|------|--------|-------|
| Panel y clientes en la red local | TCP 8420 (`server.http_port`) | Solo la red local |
| DLNA (descubrimiento) | UDP 1900, multidifusión `239.255.255.250` | Solo la red local |
| HTTPS propio (`server.https_enabled`) | TCP 8421 (`server.https_port`) | Lo que decidas |

- **DLNA** (`discovery.dlna_enabled: true`, y *Publicar por DLNA* en cada biblioteca).
  Cualquier equipo de la red local ve lo publicado, sin contraseña. En Docker, el
  descubrimiento necesita `network_mode: host` (solo Linux); en Docker Desktop, el
  servidor se alcanza escribiendo su dirección en el televisor. Nunca abras el
  UDP 1900 a Internet.
- **Acceso desde Internet** (`remote.enabled` y `remote.direct_enabled`): publica
  **solo** el puerto de HTTPS o el del reverse proxy. Exige un certificado propio
  (`server.tls_cert_file` y `tls_key_file`, que se recargan solos al renovarlos) o
  un proxy declarado en `server.trusted_proxies`; un reverse proxy debe enviar
  `X-Forwarded-For` con la dirección del cliente. Pon `remote.public_url` para
  que el panel (*Administración → Red*) compruebe el certificado, el DNS y, con una
  prueba abierta desde los datos del móvil, que se llega de verdad. El panel de
  administración sigue cerrado desde fuera salvo `remote.admin_enabled`.
- **Si ya usabas un reverse proxy** (Traefik, nginx, Caddy) sin declararlo, desde esta
  versión el servidor **rechaza con `403 REMOTE_ACCESS_DISABLED`** lo que llega con
  `X-Forwarded-For` desde un origen que no está en `server.trusted_proxies`: sin eso
  no puede saber si el cliente de verdad está en tu red local (§14.4). El registro
  lo dice (`conexión remota rechazada`). Declara la dirección del proxy en
  `trusted_proxies` y activa `remote.enabled` y `remote.direct_enabled`.
- Una VPN (Tailscale, WireGuard) no es «red local» para el servidor: añade su rango en
  `remote.local_networks` si quieres tratarla como tal.

## Generar los binarios

```bash
make release VERSION=v0.1.0
```

Compila el panel y los binarios de Windows, Linux y macOS, en `amd64` y `arm64`,
y deja los archivos y `SHA256SUMS` en `dist/release/`. No hace falta CGO.

## Estado

Lo que se ha comprobado de verdad y lo que no, en [plan_de_trabajo.md](../plan_de_trabajo.md),
Fase 10.
