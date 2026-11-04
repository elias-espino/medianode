// Perfil de capacidades del navegador (§9.2): lo que este navegador, en este
// equipo, reproduce de verdad. Se declara solo lo que se ha comprobado: lo que
// no se sabe que funciona no se anuncia, y el servidor recodifica el resto.

export interface ClientProfile {
  platform: "web";
  protocols: string[];
  containers: string[];
  video_codecs: string[];
  audio_codecs: string[];
  hdr: string[];
  max_bit_depth: number;
  max_resolution: string;
  max_audio_channels: number;
}

// Sondas de <video>: canPlayType responde "", "maybe" o "probably".
const videoProbes: Record<string, string> = {
  h264: 'video/mp4; codecs="avc1.640028"',
  hevc: 'video/mp4; codecs="hvc1.1.6.L120.90"',
  vp9: 'video/webm; codecs="vp09.00.10.08"',
  vp8: 'video/webm; codecs="vp8"',
  av1: 'video/mp4; codecs="av01.0.08M.08"',
};

const audioProbes: Record<string, string> = {
  aac: 'audio/mp4; codecs="mp4a.40.2"',
  mp3: "audio/mpeg",
  opus: 'audio/webm; codecs="opus"',
  vorbis: 'audio/webm; codecs="vorbis"',
  flac: 'audio/mp4; codecs="flac"',
  ac3: 'audio/mp4; codecs="ac-3"',
  eac3: 'audio/mp4; codecs="ec-3"',
};

// Matroska no está: Chrome y Edge contestan "maybe" y lo reproducen a veces,
// pero sin garantías con el salto y las pistas. Es mejor entregarlo por HLS.
const containerProbes: Record<string, string> = {
  mp4: "video/mp4",
  webm: "video/webm",
  mov: "video/quicktime",
};

type Probe = (mime: string) => boolean;

// Una sonda positiva es "probably" o "maybe": Chrome y Firefox contestan "maybe"
// a casi todo lo que reproducen, y Safari "probably".
function canPlay(video: HTMLVideoElement): Probe {
  return (mime) => video.canPlayType(mime) !== "";
}

// supportsHLS indica si el navegador puede reproducir HLS: con Media Source
// (hls.js) o de forma nativa (Safari).
export function supportsHLS(video: HTMLVideoElement): boolean {
  const w = window as unknown as { MediaSource?: unknown; ManagedMediaSource?: unknown };
  return Boolean(w.MediaSource || w.ManagedMediaSource) || video.canPlayType("application/vnd.apple.mpegurl") !== "";
}

// detectProfile construye el perfil. Es la parte pura: recibe las sondas, así
// que se prueba sin navegador.
export function buildProfile(input: {
  canPlayVideo: Probe;
  hls: boolean;
  hdr10Display: boolean;
  hevc10: boolean;
  screen: { width: number; height: number };
  audioChannels: number;
}): ClientProfile {
  const pick = (probes: Record<string, string>) =>
    Object.entries(probes)
      .filter(([, mime]) => input.canPlayVideo(mime))
      .map(([name]) => name);
  const video = pick(videoProbes);
  // H.264 y AAC dentro de MP4 se reproducen en cualquier navegador moderno: si
  // la sonda fallara por un navegador raro, el servidor recodificaría a ellos.
  if (!video.includes("h264")) video.push("h264");
  const audio = pick(audioProbes);
  if (!audio.includes("aac")) audio.push("aac");
  const containers = pick(containerProbes);
  if (!containers.includes("mp4")) containers.push("mp4");
  // HDR solo con pantalla HDR y decodificación de 10 bits: declararlo sin eso
  // dejaría pasar un vídeo de 10 bits sin tone mapping, con colores lavados.
  const hdr = input.hdr10Display && input.hevc10 ? ["hdr10"] : [];
  const w = Math.max(1, Math.round(input.screen.width));
  const h = Math.max(1, Math.round(input.screen.height));
  return {
    platform: "web",
    protocols: input.hls ? ["http", "hls"] : ["http"],
    containers,
    video_codecs: video,
    audio_codecs: audio,
    hdr,
    max_bit_depth: hdr.length > 0 ? 10 : 8,
    max_resolution: `${w}x${h}`,
    max_audio_channels: input.audioChannels >= 6 ? 6 : 2,
  };
}

let cached: Promise<ClientProfile> | null = null;

// detectProfile detecta una vez y recuerda el resultado.
export function detectProfile(): Promise<ClientProfile> {
  cached ??= detect();
  return cached;
}

async function detect(): Promise<ClientProfile> {
  const video = document.createElement("video");
  const dpr = window.devicePixelRatio || 1;
  return buildProfile({
    canPlayVideo: canPlay(video),
    hls: supportsHLS(video),
    hdr10Display: window.matchMedia?.("(dynamic-range: high)").matches ?? false,
    hevc10: await hevc10Decodes(),
    screen: { width: window.screen.width * dpr, height: window.screen.height * dpr },
    audioChannels: maxAudioChannels(),
  });
}

// hevc10Decodes pregunta a MediaCapabilities si se decodifica HEVC de 10 bits
// con HDR10: canPlayType no distingue la profundidad de color.
async function hevc10Decodes(): Promise<boolean> {
  const caps = navigator.mediaCapabilities;
  if (!caps?.decodingInfo) return false;
  try {
    const info = await caps.decodingInfo({
      type: "file",
      video: {
        contentType: 'video/mp4; codecs="hvc1.2.4.L153.B0"',
        width: 3840,
        height: 2160,
        bitrate: 20_000_000,
        framerate: 24,
        transferFunction: "pq",
        colorGamut: "rec2020",
        hdrMetadataType: "smpteSt2086",
      } as VideoConfiguration,
    });
    return info.supported && info.powerEfficient;
  } catch {
    return false;
  }
}

// maxAudioChannels es cuántos canales admite la salida de audio: más de dos
// solo con altavoces envolventes.
function maxAudioChannels(): number {
  try {
    const Ctx = window.AudioContext ?? (window as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext;
    if (!Ctx) return 2;
    const ctx = new Ctx();
    const n = ctx.destination.maxChannelCount;
    void ctx.close();
    return n;
  } catch {
    return 2;
  }
}
