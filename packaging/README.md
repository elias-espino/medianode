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

## Generar los binarios

```bash
make release VERSION=v0.1.0
```

Compila el panel y los binarios de Windows, Linux y macOS, en `amd64` y `arm64`,
y deja los archivos y `SHA256SUMS` en `dist/release/`. No hace falta CGO.

## Estado

Lo que se ha comprobado de verdad y lo que no, en [plan_de_trabajo.md](../plan_de_trabajo.md),
Fase 10.
