# syntax=docker/dockerfile:1
#
# Imagen de MediaNode: el panel y el servidor en un binario, y FFmpeg del
# repositorio de Alpine. Compilar desde la raíz del repositorio:
#
#   docker build -t medianode .
#
# Los datos (base de datos, imágenes, caché, logs) viven en /medianode: monta ahí
# un volumen. Las bibliotecas se montan donde quieras y se añaden desde el panel.

ARG GO_VERSION=1.27.3
ARG NODE_VERSION=20
ARG ALPINE_VERSION=3.22

# 1. Panel web.
FROM node:${NODE_VERSION}-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# 2. Servidor, sin CGO: el driver de SQLite es Go puro.
FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/medianode-server ./cmd/medianode-server

# 3. Imagen final.
FROM alpine:${ALPINE_VERSION}
RUN apk add --no-cache ffmpeg ca-certificates tzdata \
 && adduser -D -H -u 10001 -s /sbin/nologin medianode \
 && mkdir -p /medianode/config \
 && chown -R medianode:medianode /medianode
# La imagen no se construye si FFmpeg no sirve para lo que MediaNode necesita:
# FFprobe y libx264 presentes, y una build sin componentes no redistribuibles
# (--enable-nonfree). docs/licencias-ffmpeg.md explica el porqué.
RUN ffmpeg -version | head -n 1 \
 && ffprobe -version | head -n 1 \
 && ffmpeg -hide_banner -encoders 2>/dev/null | grep -q libx264 \
 && ! ffmpeg -version | grep -q -- --enable-nonfree
COPY --from=build /out/medianode-server /usr/local/bin/medianode-server
COPY config/server.example.yaml /usr/share/medianode/server.example.yaml

USER medianode
WORKDIR /medianode
VOLUME ["/medianode"]
EXPOSE 8420
ENV TZ=UTC
# Sin --config: lee /medianode/config/server.yaml si existe y, si no, usa los
# valores por defecto, con los datos en /medianode/data.
ENTRYPOINT ["medianode-server"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:8420/health || exit 1
