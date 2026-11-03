import { useEffect, useRef, useState } from "react";
import { api, ApiError } from "./api";
import { Alert, Spinner, errorText } from "./ui";

// Reproductor de prueba del panel: Direct Play en <video>. Envía heartbeats y
// cierra la sesión al salir, para que la URL deje de servir (§12, §16.4).

interface Session {
  session_id: string;
  stream_url: string;
  subtitle_url: string | null;
  playback_mode: string;
  start_position_ms: number;
}

const HEARTBEAT_MS = 10_000;

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

export function Player({ itemId, title, onClose }: { itemId: string; title: string; onClose: () => void }) {
  const video = useRef<HTMLVideoElement>(null);
  const [session, setSession] = useState<Session | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let current: Session | null = null;
    let timer: number | undefined;
    let closed = false;

    const beat = (state?: "playing" | "paused") => {
      const v = video.current;
      if (!current || !v) return;
      const s = state ?? (v.paused ? "paused" : "playing");
      api
        .patch(`/playback/sessions/${current.session_id}`, { state: s, position_ms: Math.round(v.currentTime * 1000) })
        .catch((e) => {
          if (e instanceof ApiError && e.code === "SESSION_EXPIRED") setError("La sesión ha caducado. Cierra y vuelve a abrir el reproductor.");
        });
    };
    const close = () => {
      if (!current || closed) return;
      closed = true;
      const csrf = decodeURIComponent((document.cookie.match(/(?:^|;\s*)mn_csrf=([^;]+)/) || [])[1] || "");
      // keepalive: la petición sale aunque se cierre la pestaña.
      void fetch(`/api/v1/playback/sessions/${current.session_id}`, {
        method: "DELETE", credentials: "same-origin", keepalive: true, headers: { "X-MediaNode-CSRF": csrf },
      });
    };

    api
      .post<Session>("/playback/sessions", { media_item_id: itemId, client_profile: { platform: "web" } })
      .then((s) => {
        if (closed) return;
        current = s;
        setSession(s);
        timer = window.setInterval(() => beat(), HEARTBEAT_MS);
      })
      .catch((e) => {
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
  }, [itemId]);

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
      <video
        ref={video}
        className="player-video"
        controls
        autoPlay
        hidden={!session}
        src={session?.stream_url}
        onLoadedMetadata={(e) => {
          if (session && session.start_position_ms > 0) e.currentTarget.currentTime = session.start_position_ms / 1000;
        }}
      >
        {session?.subtitle_url && <track kind="subtitles" src={session.subtitle_url} default label="Subtítulos" />}
      </video>
    </section>
  );
}

function explain(e: ApiError): string {
  const reasons = (e.details?.reasons ?? []) as { code: string }[];
  const codes = reasons.map((r) => reasonLabels[r.code] ?? r.code);
  return codes.length ? ` (${codes.join(", ")}).` : "";
}
