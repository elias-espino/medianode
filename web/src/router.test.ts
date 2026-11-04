import { beforeAll, describe, expect, it } from "vitest";

// router.tsx escucha popstate al importarse: se simula lo mínimo de window.
let match: (pattern: string, path: string) => Record<string, string> | null;

beforeAll(async () => {
  (globalThis as unknown as { window: unknown }).window = { addEventListener() {}, scrollTo() {} };
  ({ match } = await import("./router"));
});

describe("match", () => {
  it("extrae los parámetros", () => {
    expect(match("/media/:id", "/media/abc")).toEqual({ id: "abc" });
    expect(match("/admin/:tab", "/admin/logs")).toEqual({ tab: "logs" });
    expect(match("/a/:x/b/:y", "/a/1/b/2")).toEqual({ x: "1", y: "2" });
  });

  it("decodifica los segmentos", () => {
    expect(match("/media/:id", "/media/a%20b")).toEqual({ id: "a b" });
  });

  it("no coincide si la forma es otra", () => {
    expect(match("/media/:id", "/media")).toBeNull();
    expect(match("/media/:id", "/media/a/b")).toBeNull();
    expect(match("/media/:id", "/series/a")).toBeNull();
    expect(match("/libraries", "/libraries/x")).toBeNull();
  });

  it("ignora la barra final", () => {
    expect(match("/media/:id", "/media/abc/")).toEqual({ id: "abc" });
  });

  it("una ruta fija solo coincide consigo misma", () => {
    expect(match("/search", "/search")).toEqual({});
    expect(match("/search", "/other")).toBeNull();
  });
});
