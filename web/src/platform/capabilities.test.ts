import { describe, expect, it } from "vitest";
import { buildProfile } from "./capabilities";

const base = {
  canPlayVideo: () => true,
  hls: true,
  hdr10Display: false,
  hevc10: false,
  screen: { width: 1920, height: 1080 },
  audioChannels: 2,
};

describe("buildProfile", () => {
  it("declara lo que el navegador dice reproducir", () => {
    const only = (mimes: string[]) => (m: string) => mimes.includes(m);
    const p = buildProfile({ ...base, canPlayVideo: only(['video/mp4; codecs="avc1.640028"', 'audio/mp4; codecs="mp4a.40.2"', "video/mp4"]) });
    expect(p.video_codecs).toEqual(["h264"]);
    expect(p.audio_codecs).toContain("aac");
    expect(p.containers).toEqual(["mp4"]);
    expect(p.platform).toBe("web");
  });

  it("siempre incluye H.264, AAC y MP4 aunque la sonda falle", () => {
    const p = buildProfile({ ...base, canPlayVideo: () => false });
    expect(p.video_codecs).toContain("h264");
    expect(p.audio_codecs).toContain("aac");
    expect(p.containers).toContain("mp4");
  });

  it("no declara Matroska aunque el navegador diga que 'quizá'", () => {
    const p = buildProfile({ ...base, canPlayVideo: () => true });
    expect(p.containers).not.toContain("mkv");
    expect(p.containers).toEqual(expect.arrayContaining(["mp4", "webm"]));
  });

  it("anuncia HLS solo si el navegador puede reproducirlo", () => {
    expect(buildProfile({ ...base, hls: true }).protocols).toEqual(["http", "hls"]);
    expect(buildProfile({ ...base, hls: false }).protocols).toEqual(["http"]);
  });

  it("declara HDR solo con pantalla HDR y decodificación de 10 bits", () => {
    expect(buildProfile({ ...base, hdr10Display: true, hevc10: true })).toMatchObject({ hdr: ["hdr10"], max_bit_depth: 10 });
    // Sin pantalla HDR: lista vacía explícita, para que el servidor haga tone mapping.
    expect(buildProfile({ ...base, hdr10Display: false, hevc10: true })).toMatchObject({ hdr: [], max_bit_depth: 8 });
    expect(buildProfile({ ...base, hdr10Display: true, hevc10: false })).toMatchObject({ hdr: [], max_bit_depth: 8 });
  });

  it("da la resolución de la pantalla en píxeles reales, formato ancho x alto", () => {
    expect(buildProfile({ ...base, screen: { width: 2560.4, height: 1439.6 } }).max_resolution).toBe("2560x1440");
    expect(buildProfile({ ...base, screen: { width: 0, height: 0 } }).max_resolution).toBe("1x1");
    expect(buildProfile(base).max_resolution).toMatch(/^\d+x\d+$/);
  });

  it("declara audio envolvente solo con seis canales o más", () => {
    expect(buildProfile({ ...base, audioChannels: 2 }).max_audio_channels).toBe(2);
    expect(buildProfile({ ...base, audioChannels: 5 }).max_audio_channels).toBe(2);
    expect(buildProfile({ ...base, audioChannels: 6 }).max_audio_channels).toBe(6);
    expect(buildProfile({ ...base, audioChannels: 8 }).max_audio_channels).toBe(6);
  });

  it("no repite códecs", () => {
    const p = buildProfile(base);
    for (const list of [p.video_codecs, p.audio_codecs, p.containers]) {
      expect(new Set(list).size).toBe(list.length);
    }
  });
});
