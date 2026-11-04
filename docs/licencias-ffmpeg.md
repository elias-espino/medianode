# Licencia de FFmpeg y de MediaNode

Auditoría de la Fase 10 sobre lo que MediaNode distribuye y lo que no. No es
asesoría legal: describe lo que se ha comprobado y qué obligaciones suele
implicar cada caso.

## Resumen

- MediaNode es MIT. **No incluye FFmpeg**: lo ejecuta como un programa aparte
  (`exec`, con listas de argumentos, sin enlazar su código ni compartir memoria).
  La licencia de FFmpeg no pasa a MediaNode.
- Los archivos de las versiones (`.zip` y `.tar.gz`) **no contienen FFmpeg**. Quien
  los instala aporta el suyo.
- La imagen de Docker sí lo instala, desde el repositorio de Alpine, al
  construirla. Quien construye la imagen y la publica en un registro **está
  distribuyendo un FFmpeg con licencia GPL** y tiene las obligaciones de la GPL.
- Una build de FFmpeg con `--enable-nonfree` **no se puede redistribuir**. La imagen
  de Docker se niega a construirse con una así.

## Qué licencia tiene cada build

FFmpeg es LGPL 2.1+ por defecto. Activar componentes GPL (como `libx264` o
`libx265`) hace que **toda la build sea GPL**, y `--enable-version3` la sube a
GPLv3 (o LGPLv3 si no hay componentes GPL).

| Build | Licencia | Comprobado |
|-------|----------|------------|
| gyan.dev «full» 9.0.2 (la del equipo de desarrollo) | GPLv3: `--enable-gpl --enable-version3`, con `libx264`, `libx265` y `libass`; sin `--enable-nonfree` | `ffmpeg -version` |
| `ffmpeg` de Alpine 3.22 (la imagen de Docker), 6.1.2 | GPL-2.0-or-later y LGPL-2.1-or-later, según la ficha del paquete | pkgs.alpinelinux.org; en la construcción de la imagen, `ffmpeg -version` no debe contener `--enable-nonfree` |

MediaNode necesita `libx264` para recodificar, y es GPL: **no se puede tener una
build solo LGPL con ese codificador**. Quien quiera una build LGPL tendría que
usar un codificador por hardware (NVENC, QSV, VAAPI, VideoToolbox), que MediaNode
detecta pero no ha verificado (ver el plan, Fase 8).

## Si distribuyes un FFmpeg junto a MediaNode

Cualquier build GPL obliga a:

1. Entregar el texto de la licencia (GPL) junto al binario.
2. Poner a disposición el **código fuente correspondiente** de esa build, o una
   oferta escrita válida durante tres años. Con el paquete de Alpine, el fuente
   es la receta pública del paquete en `aports`; con la build de gyan.dev, su
   página de descargas enlaza el código.
3. No añadir restricciones adicionales a lo que la GPL concede.

Y no distribuyas nunca una build con `--enable-nonfree` (por ejemplo, con
`libfdk_aac`): el resultado no es redistribuible. Para comprobarlo:

```bash
ffmpeg -version | grep -- --enable-nonfree   # no debe devolver nada
```

## Patentes

Las licencias de código no cubren las patentes: H.264, HEVC y AAC tienen
licencias de patentes aparte (grupos como Via LA y Access Advance). Es un asunto
distinto del copyright y depende del país y del uso, comercial o no. MediaNode
es software autoalojado para uso personal y no entrega códecs: usa los del
FFmpeg que instalas.

## Otros componentes

- **TMDB:** los metadatos y las imágenes llegan de TMDB bajo sus condiciones de
  uso (cada instalación usa su propia clave). El panel muestra el aviso que
  exigen cuando el proveedor está configurado.
- **hls.js** (Apache-2.0) y **React** (MIT) van dentro del panel compilado.
- **Dependencias de Go:** las 26 que se compilan en el servidor (comprobado con
  `go list -deps` para Windows, Linux y macOS) son 12 BSD, 11 MIT, 2 Apache-2.0 y
  1 ISC: ninguna con copyleft. En el grafo de módulos hay una MPL-2.0
  (`hashicorp/golang-lru`, copyleft débil por archivo) que solo entra por
  dependencias de pruebas de `modernc.org` y no se compila en el binario. Vuelve a
  comprobarlo al cambiar de dependencias.

## Lista de comprobación antes de publicar una versión

- [ ] Los archivos de la versión no contienen `ffmpeg` ni `ffprobe`.
- [ ] Si publicas una imagen de Docker: incluye el texto de la GPL y enlaza el
      fuente del paquete `ffmpeg` de esa versión de Alpine.
- [ ] `ffmpeg -version` de la build que recomiendas no contiene
      `--enable-nonfree`.
- [ ] El `README` dice que FFmpeg es un programa aparte con su propia licencia.
