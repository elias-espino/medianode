import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError, type MediaFile } from "./api";
import type { ContinueItem } from "./api/types";
import { detectProfile } from "./platform/capabilities";
import { Alert, Spinner, errorText, formatBytes, formatDuration } from "./ui";

// Reproductor de prueba del panel: Direct Play en <video> y HLS (hls.js, o el
// HLS nativo de Safari). Envía heartbeats, cambia de audio y de subtítulos
// durante la reproducción y cierra la sesión al salir, para que la URL deje de
// servir (§12, §16.4).

interface Session {
  session_id: string;
  media_file_id: string;
  stream_url: string;
  subtitle_url: string | null;
  subtitle_mode: string;
  playback_mode: string;
  start_position_ms: number;
  selected_audio_stream_id: string | null;
  selected_subtitle_id: string | null;
}

interface Track {
  id: string;
  file_id: string;
  type: string;
  language: string | null;
  codec: string | null;
  forced: boolean;
  title: string | null;
  height?: number | null;
}

// Cuántas veces seguidas se reanuda sola una sesión caducada antes de rendirse:
// un servidor que las cierra al momento no debe provocar un bucle.
const MAX_AUTO_RESUMES = 3;

// Por debajo de esto, el progreso guardado no merece reanudar: se empieza
// desde el principio (§12.3).
const MIN_RESUME_MS = 10_000;

// savedPosition es la posición donde se dejó el elemento, o 0. El servidor no
// la elige por el cliente: es el cliente quien envía start_position_ms (§12.4).
async function savedPosition(itemId: string): Promise<number> {
  try {
    const list = await api.get<ContinueItem[]>("/playback/continue-watching?limit=100");
    const found = list.find((c) => c.id === itemId);
    return found && found.position_ms >= MIN_RESUME_MS ? found.position_ms : 0;
  } catch {
    return 0;
  }
}
const AUTO_RESUME_WINDOW_MS = 60_000;

interface ExternalSubtitle {
  id: string;
  file_id: string;
  language: string;
  format: string | null;
  forced: boolean;
}

const HEARTBEAT_MS = 10_000;
const NO_SUBTITLE = "none";

const reasonLabels: Record<string, string> = {
  container_unsupported: "el navegador no admite el contenedor",
  video_codec_unsupported: "el navegador no admite el códec de vídeo",
  video_bit_depth_unsupported: "el vídeo es de más de 8 bits",
  hdr_unsupported: "el vídeo es HDR",
  audio_codec_unsupported: "el navegador no admite el códec de audio",
  audio_channels_unsupported: "el audio tiene más de dos canales",
  resolution_limit: "supera la resolución máxima",
  bitrate_limit: "supera el bitrate máximo",
  subtitle_burn_in: "el subtítulo habría que grabarlo en la imagen",
  subtitle_requires_hls: "el subtítulo necesita HLS",
};

function trackLabel(t: { language: string | null; codec?: string | null; forced: boolean; title?: string | null }, index: number): string {
  const parts = [t.title || (t.language && t.language !== "und" ? t.language : `Pista ${index + 1}`)];
  if (t.codec) parts.push(t.codec);
  if (t.forced) parts.push("forzado");
  return parts.join(" · ");
}

export function Player({ itemId, title, onClose }: { itemId: string; title: string; onClose: () => void }) {
  const video = useRef<HTMLVideoElement>(null);
  const [session, setSession] = useState<Session | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [versions, setVersions] = useState<{ id: string; label: string }[]>([]);
  // Versión pedida (null: la que elija el servidor) y contador que fuerza una
  // sesión nueva, para cambiar de versión o reanudar tras caducar.
  const [fileId, setFileId] = useState<string | null>(null);
  const [epoch, setEpoch] = useState(0);
  const resumeMs = useRef<number | null>(null);
  const resumes = useRef<number[]>([]);
  // Verdadero tras elegir «desde el principio»: no se vuelve a reanudar.
  const fromStart = useRef(false);
  const [audios, setAudios] = useState<Track[]>([]);
  const [subtitles, setSubtitles] = useState<Track[]>([]);
  const [external, setExternal] = useState<ExternalSubtitle[]>([]);
  // Segundo desde el que hls.js debe empezar tras cambiar de pista.
  const resumeAt = useRef<number | null>(null);
  // La sesión actual para el cierre y los latidos, sin recrear el efecto.
  const current = useRef<Session | null>(null);

  useEffect(() => {
    let timer: number | undefined;
    let closed = false;
    setSession(null);
    setError(null);
    current.current = null;

    // La sesión caducó (el servidor la cerró por inactividad o se reinició):
    // se abre otra en el mismo punto, salvo que ocurra una y otra vez.
    const resume = (atSeconds: number) => {
      const now = Date.now();
      resumes.current = resumes.current.filter((t) => now - t < AUTO_RESUME_WINDOW_MS);
      if (resumes.current.length >= MAX_AUTO_RESUMES) {
        setError("La sesión caducó varias veces seguidas. Cierra y vuelve a abrir el reproductor.");
        return;
      }
      resumes.current.push(now);
      resumeMs.current = Math.round(atSeconds * 1000);
      setNotice("La sesión había caducado; se ha reanudado donde ibas.");
      setEpoch((n) => n + 1);
    };

    const beat = (state?: "playing" | "paused") => {
      const v = video.current;
      const s = current.current;
      if (!s || !v) return;
      api
        .patch(`/playback/sessions/${s.session_id}`, { state: state ?? (v.paused ? "paused" : "playing"), position_ms: Math.round(v.currentTime * 1000) })
        .catch((e) => {
          if (e instanceof ApiError && e.code === "SESSION_EXPIRED" && !closed) resume(v.currentTime);
        });
    };
    const close = () => {
      const s = current.current;
      if (!s || closed) return;
      closed = true;
      const csrf = decodeURIComponent((document.cookie.match(/(?:^|;\s*)mn_csrf=([^;]+)/) || [])[1] || "");
      // keepalive: la petición sale aunque se cierre la pestaña.
      void fetch(`/api/v1/playback/sessions/${s.session_id}`, {
        method: "DELETE", credentials: "same-origin", keepalive: true, headers: { "X-MediaNode-CSRF": csrf },
      });
    };

    const explicit = resumeMs.current;
    resumeMs.current = null;
    // Una posición explícita (cambio de versión, sesión caducada) manda; si no,
    // lo guardado, salvo que se haya pedido empezar de cero.
    const startMs = explicit !== null ? Promise.resolve(explicit) : fromStart.current ? Promise.resolve(0) : savedPosition(itemId);
    Promise.all([detectProfile(), startMs])
      .then(([profile, start]) => {
        if (closed) throw new Error("cerrado");
        if (explicit === null && start > 0) setNotice(`Continúas donde lo dejaste (${formatDuration(start)}).`);
        return api.post<Session>("/playback/sessions", {
          media_item_id: itemId,
          media_file_id: fileId ?? undefined,
          client_profile: profile,
          start_position_ms: start > 0 ? start : undefined,
        });
      })
      .then((s) => {
        if (closed) return;
        current.current = s;
        setSession(s);
        timer = window.setInterval(() => beat(), HEARTBEAT_MS);
        // Las pistas del archivo elegido, para poder cambiarlas.
        void Promise.all([
          api.get<Track[]>(`/media/${itemId}/streams`).catch(() => [] as Track[]),
          api.get<ExternalSubtitle[]>(`/media/${itemId}/subtitles`).catch(() => [] as ExternalSubtitle[]),
          api.get<MediaFile[]>(`/media/${itemId}/files`).catch(() => [] as MediaFile[]),
        ]).then(([streams, ext, files]) => {
          if (closed) return;
          setVersions(
            files
              .filter((f) => f.probe_status === "done" && !f.missing_since)
              .map((f) => ({ id: f.id, label: versionLabel(f, streams.find((t) => t.file_id === f.id && t.type === "video")) })),
          );
          setAudios(streams.filter((t) => t.file_id === s.media_file_id && t.type === "audio"));
          setSubtitles(streams.filter((t) => t.file_id === s.media_file_id && t.type === "subtitle"));
          setExternal(ext.filter((t) => t.file_id === s.media_file_id));
        });
      })
      .catch((e) => {
        if (closed) return;
        if (e instanceof ApiError && e.code === "PLAYBACK_UNSUPPORTED") {
          setError(e.message + explain(e));
        } else {
          setError(errorText(e));
        }
      });

    const v = video.current;
    const onPlay = () => beat("playing");
    const onPause = () => beat("paused");
    v?.addEventListener("play", onPlay);
    v?.addEventListener("pause", onPause);
    window.addEventListener("pagehide", close);
    return () => {
      window.clearInterval(timer);
      v?.removeEventListener("play", onPlay);
      v?.removeEventListener("pause", onPause);
      window.removeEventListener("pagehide", close);
      close();
      closed = true;
    };
  }, [itemId, fileId, epoch]);

  // Empezar desde el principio un elemento que se estaba reanudando.
  const restartFromBeginning = () => {
    fromStart.current = true;
    resumeMs.current = 0;
    setNotice(null);
    setEpoch((n) => n + 1);
  };

  // Otra versión del mismo elemento es otro archivo: sesión nueva, en el mismo
  // punto.
  const changeVersion = (id: string) => {
    resumeMs.current = Math.round((video.current?.currentTime ?? 0) * 1000);
    setNotice(null);
    setFileId(id);
  };

  // HLS: hls.js donde hay Media Source Extensions; Safari lo reproduce solo.
  const hlsUrl = session && session.stream_url.includes(".m3u8") ? session.stream_url : null;
  useEffect(() => {
    const v = video.current;
    if (!v || !session || !hlsUrl) return;
    let destroy: (() => void) | undefined;
    let cancelled = false;
    const startAt = resumeAt.current ?? session.start_position_ms / 1000;
    resumeAt.current = null;
    // Import dinámico: Direct Play no descarga hls.js.
    void import("hls.js").then(({ default: Hls }) => {
      if (cancelled) return;
      if (Hls.isSupported()) {
        const hls = new Hls({ startPosition: startAt });
        hls.on(Hls.Events.ERROR, (_e, data) => {
          if (data.fatal) setError(`Error de reproducción (${data.details}).`);
        });
        hls.loadSource(hlsUrl);
        hls.attachMedia(v);
        destroy = () => hls.destroy();
      } else if (v.canPlayType("application/vnd.apple.mpegurl")) {
        v.src = hlsUrl;
        v.currentTime = startAt;
      } else {
        setError("Este navegador no puede reproducir HLS.");
      }
    });
    return () => {
      cancelled = true;
      destroy?.();
    };
  }, [session?.stream_url, hlsUrl]); // eslint-disable-line react-hooks/exhaustive-deps

  // Cambia de audio o de subtítulos con la sesión en marcha. En HLS el servidor
  // reinicia FFmpeg y devuelve otra revisión del stream_url: se recarga desde
  // la posición actual.
  const change = useCallback(
    async (body: { selected_audio_stream_id?: string; selected_subtitle_id?: string }) => {
      const s = current.current;
      const v = video.current;
      if (!s || !v || busy) return;
      setBusy(true);
      setNotice(null);
      const at = v.currentTime;
      try {
        const next = await api.post<Session>(`/playback/sessions/${s.session_id}/tracks`, { ...body, position_ms: Math.round(at * 1000) });
        if (next.stream_url !== s.stream_url) resumeAt.current = at;
        const merged = { ...s, ...next, media_file_id: s.media_file_id };
        current.current = merged;
        setSession(merged);
      } catch (e) {
        if (e instanceof ApiError && e.code === "PLAYBACK_UNSUPPORTED") {
          setNotice(e.message + explain(e));
        } else {
          setNotice(errorText(e));
        }
      } finally {
        setBusy(false);
      }
    },
    [busy],
  );

  const direct = session?.playback_mode === "direct_play";
  const selectedSubtitle = session?.selected_subtitle_id ?? NO_SUBTITLE;

  return (
    <section className="card player" aria-label={`Reproduciendo ${title}`}>
      <div className="library-head">
        <h2>{title}</h2>
        <button className="btn btn-ghost" onClick={onClose}>
          Cerrar
        </button>
      </div>
      {error && <Alert>{error}</Alert>}
      {!session && !error && <Spinner label="Preparando la reproducción…" />}
      {session && (versions.length > 1 || audios.length > 1 || subtitles.length + external.length > 0) && (
        <div className="player-tracks">
          {versions.length > 1 && (
            <label>
              Versión
              <select value={session.media_file_id} disabled={busy} onChange={(e) => changeVersion(e.target.value)}>
                {versions.map((v) => (
                  <option key={v.id} value={v.id}>
                    {v.label}
                  </option>
                ))}
              </select>
            </label>
          )}
          {audios.length > 1 && (
            <label>
              Audio
              <select
                value={session.selected_audio_stream_id ?? ""}
                disabled={busy || direct}
                title={direct ? "En reproducción directa el audio lo elige el navegador" : undefined}
                onChange={(e) => void change({ selected_audio_stream_id: e.target.value })}
              >
                {audios.map((t, i) => (
                  <option key={t.id} value={t.id}>
                    {trackLabel(t, i)}
                  </option>
                ))}
              </select>
            </label>
          )}
          {subtitles.length + external.length > 0 && (
            <label>
              Subtítulos
              <select value={selectedSubtitle} disabled={busy} onChange={(e) => void change({ selected_subtitle_id: e.target.value })}>
                <option value={NO_SUBTITLE}>Sin subtítulos</option>
                {subtitles.map((t, i) => (
                  <option key={t.id} value={t.id}>
                    {trackLabel(t, i)}
                  </option>
                ))}
                {external.map((t, i) => (
                  <option key={t.id} value={t.id}>
                    {trackLabel({ language: t.language, codec: t.format, forced: t.forced }, i)} (archivo)
                  </option>
                ))}
              </select>
            </label>
          )}
          {busy && <Spinner label="Cambiando de pista…" />}
        </div>
      )}
      {notice && (
        <Alert kind="info">
          {notice}
          {notice.startsWith("Continúas") && (
            <>
              {" "}
              <button className="btn btn-link" onClick={restartFromBeginning}>
                Empezar desde el principio
              </button>
            </>
          )}
        </Alert>
      )}
      <video
        ref={video}
        className="player-video"
        controls
        autoPlay
        hidden={!session}
        src={hlsUrl ? undefined : session?.stream_url}
        onLoadedMetadata={(e) => {
          if (session && !hlsUrl && session.start_position_ms > 0) e.currentTarget.currentTime = session.start_position_ms / 1000;
        }}
      >
        {session?.subtitle_url && <track key={session.subtitle_url} kind="subtitles" src={session.subtitle_url} default label="Subtítulos" />}
      </video>
    </section>
  );
}

function versionLabel(f: MediaFile, video: Track | undefined): string {
  const parts: string[] = [];
  if (video?.height) parts.push(`${video.height}p`);
  if (f.container) parts.push(f.container.toUpperCase());
  if (f.file_size !== null) parts.push(formatBytes(f.file_size));
  if (parts.length === 0) parts.push(f.relative_path?.split(/[\\/]/).pop() ?? "Versión");
  return parts.join(" · ");
}

function explain(e: ApiError): string {
  const reasons = (e.details?.reasons ?? []) as { code: string }[];
  const codes = reasons.map((r) => reasonLabels[r.code] ?? r.code);
  return codes.length ? ` (${codes.join(", ")}).` : "";
}
