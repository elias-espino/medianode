import { describe, expect, it } from "vitest";
import { formatBitrate, formatBytes, formatChannels, formatDuration, formatUptime } from "./ui";

describe("formatBitrate", () => {
  it("elige la unidad", () => {
    expect(formatBitrate(0)).toBe("—");
    expect(formatBitrate(null)).toBe("—");
    expect(formatBitrate(undefined)).toBe("—");
    expect(formatBitrate(800_000)).toBe("800 kb/s");
    expect(formatBitrate(1_000_000)).toBe("1.0 Mb/s");
    expect(formatBitrate(4_250_000)).toBe("4.3 Mb/s");
  });
});

describe("formatUptime", () => {
  it("usa días, horas o minutos", () => {
    expect(formatUptime(30)).toBe("0 min");
    expect(formatUptime(125 * 60)).toBe("2 h 5 min");
    expect(formatUptime(3 * 86400 + 4 * 3600)).toBe("3 d 4 h");
  });
});

describe("formatBytes", () => {
  it("escala por potencias de 1024", () => {
    expect(formatBytes(null)).toBe("");
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(1023)).toBe("1023 B");
    expect(formatBytes(1536)).toBe("1.5 KB");
    expect(formatBytes(5 * 1024 ** 3)).toBe("5.0 GB");
  });
});

describe("formatDuration", () => {
  it("formatea segundos, minutos y horas", () => {
    expect(formatDuration(null)).toBe("");
    expect(formatDuration(0)).toBe("");
    expect(formatDuration(500)).toBe("1 s");
    expect(formatDuration(45_000)).toBe("45 s");
    expect(formatDuration(90 * 60_000)).toBe("1 h 30 min");
    expect(formatDuration(20 * 60_000)).toBe("20 min");
  });
});

describe("formatChannels", () => {
  it("nombra las disposiciones habituales", () => {
    expect(formatChannels(null)).toBe("");
    expect(formatChannels(2)).toBe("estéreo");
    expect(formatChannels(6)).toBe("5.1");
    expect(formatChannels(3)).toBe("3 canales");
  });
});
