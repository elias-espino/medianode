// Cliente de /api/v1 para el panel. La sesión vive en cookies HttpOnly; las
// escrituras llevan la cabecera CSRF con el valor de la cookie mn_csrf (§15.4).

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public details?: Record<string, unknown>,
  ) {
    super(message);
  }
}

const BASE = "/api/v1";

function csrfToken(): string {
  const m = document.cookie.match(/(?:^|;\s*)mn_csrf=([^;]+)/);
  return m ? decodeURIComponent(m[1]) : "";
}

let refreshing: Promise<boolean> | null = null;

// Una sola renovación a la vez: varias peticiones caducadas esperan la misma.
function refresh(): Promise<boolean> {
  refreshing ??= fetch(BASE + "/auth/refresh", {
    method: "POST",
    credentials: "same-origin",
    headers: { "X-MediaNode-CSRF": csrfToken() },
  })
    .then((r) => r.ok)
    .catch(() => false)
    .finally(() => {
      refreshing = null;
    });
  return refreshing;
}

// Rutas cuyo 401 no se arregla renovando: son las que crean o cierran sesión.
const noRenew = new Set(["/auth/login", "/auth/refresh", "/auth/setup", "/auth/logout"]);

let onUnauthorized: () => void = () => {};

export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn;
}

async function request<T>(method: string, path: string, body?: unknown, retry = true): Promise<T> {
  const headers: Record<string, string> = {};
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (method !== "GET") headers["X-MediaNode-CSRF"] = csrfToken();
  const res = await fetch(BASE + path, {
    method,
    headers,
    credentials: "same-origin",
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (res.status === 204 || res.status === 202) return undefined as T;
  const data = await res.json().catch(() => null);
  if (res.ok) return data as T;

  const code: string = data?.error?.code ?? "INTERNAL_ERROR";
  const message: string = data?.error?.message ?? `Error ${res.status}`;
  // La cookie de acceso caduca a la vez que su token, así que el navegador la
  // borra y el servidor suele responder AUTH_REQUIRED, no AUTH_TOKEN_EXPIRED.
  // En ambos casos se intenta renovar una vez con la cookie de refresco.
  const renewable = code === "AUTH_TOKEN_EXPIRED" || code === "AUTH_REQUIRED";
  if (res.status === 401 && renewable && retry && !noRenew.has(path) && (await refresh())) {
    return request<T>(method, path, body, false);
  }
  if (res.status === 401 && path !== "/auth/login") onUnauthorized();
  throw new ApiError(res.status, code, message, data?.error?.details);
}

export const api = {
  get: <T>(path: string) => request<T>("GET", path),
  post: <T>(path: string, body?: unknown) => request<T>("POST", path, body),
  put: <T>(path: string, body?: unknown) => request<T>("PUT", path, body),
  patch: <T>(path: string, body?: unknown) => request<T>("PATCH", path, body),
  del: <T>(path: string) => request<T>("DELETE", path),
};

export function qs(params: Record<string, string | number | undefined | null>): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== "") p.set(k, String(v));
  }
  const s = p.toString();
  return s ? "?" + s : "";
}

// Tipos de la API (docs/openapi.yaml).

export type Role = "owner" | "admin" | "user" | "restricted";

export interface User {
  id: string;
  username: string;
  role: Role;
  is_active: boolean;
  max_age_rating: number | null;
  created_at: string;
  last_login_at: string | null;
}

export interface ServerInfo {
  name: string;
  version: string;
  protocol_version: number;
  setup_required: boolean;
}

export interface ServerStatus {
  status: string;
  version: string;
  uptime_seconds: number;
  admin?: {
    ffprobe_available: boolean;
    libraries: number;
    scans_running: number;
    metadata: {
      provider: string | null;
      configured: boolean;
      pending: number;
      paused_until: string | null;
      last_error: string | null;
    };
  };
}

export interface LibraryPath {
  id: string;
  path: string;
  is_available: boolean;
  last_scan_at: string | null;
}

export interface Library {
  id: string;
  name: string;
  type: "movies" | "tv";
  enabled: boolean;
  dlna_enabled: boolean;
  created_at: string;
  updated_at: string;
  paths?: LibraryPath[];
}

export interface ScanJob {
  id: string;
  library_id: string;
  trigger: string;
  status: "running" | "completed" | "failed" | "interrupted";
  started_at: string | null;
  finished_at: string | null;
  files_discovered: number;
  files_processed: number;
  error_message: string | null;
}

export type MetadataStatus = "pending" | "done" | "unmatched" | "failed";

export interface ItemSummary {
  id: string;
  type: "movie" | "series";
  title: string;
  year: number | null;
  metadata_status: MetadataStatus;
  poster_url: string | null;
}

export interface ItemPage {
  items: ItemSummary[];
  next_cursor: string | null;
}

export interface Person {
  id: string;
  name: string;
  role: "actor" | "director" | "writer" | "creator";
  character: string | null;
}

export interface Item {
  id: string;
  library_id: string;
  parent_id: string | null;
  type: "movie" | "series" | "season" | "episode";
  title: string;
  original_title: string | null;
  year: number | null;
  release_date: string | null;
  overview: string | null;
  age_rating: string | null;
  min_age: number | null;
  original_language: string | null;
  duration_ms: number | null;
  season_number: number | null;
  episode_number: number | null;
  metadata_status: MetadataStatus;
  metadata_locked: boolean;
  genres: string[];
  people: Person[];
  external_ids: Record<string, string>;
  images: Record<string, string>;
}

export interface Child {
  id: string;
  type: "season" | "episode";
  title: string;
  year: number | null;
  season_number: number | null;
  episode_number: number | null;
  overview: string | null;
  release_date: string | null;
  duration_ms: number | null;
  image_url: string | null;
}

export interface MediaFile {
  id: string;
  relative_path?: string;
  container: string | null;
  file_size: number | null;
  duration_ms: number | null;
  bitrate: number | null;
  probe_status: string;
  missing_since: string | null;
}

export interface Stream {
  id: string;
  file_id: string;
  index: number;
  type: string;
  codec: string | null;
  language: string | null;
  channels: number | null;
  width: number | null;
  height: number | null;
  hdr_format: string | null;
  bit_depth: number | null;
  default: boolean;
  forced: boolean;
  title: string | null;
}

export interface SearchResult {
  id: string;
  type: "movie" | "series" | "episode";
  title: string;
  year: number | null;
  library_id: string;
  parent_id: string | null;
  parent_title: string | null;
}

export interface Match {
  provider: string;
  external_id: string;
  title: string;
  original_title: string;
  year: number | null;
  overview: string;
  poster_url: string | null;
}

export interface Grant {
  library_id: string;
  permissions: ("view" | "scan" | "manage")[];
}
