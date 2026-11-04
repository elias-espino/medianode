import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { ApiError } from "./api";

// Piezas compartidas por las pantallas.

export function errorText(e: unknown): string {
  if (e instanceof ApiError) return e.message;
  return "No se pudo contactar con el servidor";
}

// useLoad carga datos y expone recarga, error y estado de carga.
export function useLoad<T>(fn: () => Promise<T>, deps: unknown[]) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const seq = useRef(0);
  const load = useCallback(() => {
    const n = ++seq.current;
    setLoading(true);
    fn()
      .then((d) => {
        if (n === seq.current) {
          setData(d);
          setError(null);
        }
      })
      .catch((e) => {
        if (n === seq.current) setError(errorText(e));
      })
      .finally(() => {
        if (n === seq.current) setLoading(false);
      });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);
  useEffect(load, [load]);
  return { data, error, loading, reload: load, setData };
}

export function Alert({ children, kind = "error" }: { children: ReactNode; kind?: "error" | "info" | "ok" }) {
  return (
    <div className={`alert alert-${kind}`} role={kind === "error" ? "alert" : "status"}>
      {children}
    </div>
  );
}

export function Spinner({ label = "Cargando…" }: { label?: string }) {
  return (
    <div className="spinner" role="status">
      <span className="spinner-dot" aria-hidden="true" />
      {label}
    </div>
  );
}

export function Empty({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="empty">
      <p className="empty-title">{title}</p>
      {children && <div className="empty-body">{children}</div>}
    </div>
  );
}

export function PageHeader({ title, subtitle, actions }: { title: string; subtitle?: ReactNode; actions?: ReactNode }) {
  return (
    <header className="page-header">
      <div>
        <h1>{title}</h1>
        {subtitle && <p className="muted">{subtitle}</p>}
      </div>
      {actions && <div className="page-actions">{actions}</div>}
    </header>
  );
}

export function Field(props: { label: string; hint?: ReactNode; children: ReactNode }) {
  return (
    <label className="field">
      <span className="field-label">{props.label}</span>
      {props.children}
      {props.hint && <span className="field-hint">{props.hint}</span>}
    </label>
  );
}

const statusLabels: Record<string, string> = {
  pending: "Pendiente",
  done: "Identificado",
  unmatched: "Sin coincidencia",
  failed: "Error",
  running: "En curso",
  completed: "Completado",
  interrupted: "Interrumpido",
};

export function StatusBadge({ status }: { status: string }) {
  return <span className={`badge badge-${status}`}>{statusLabels[status] ?? status}</span>;
}

// Poster muestra la imagen o, si no la hay, un marcador con la inicial.
export function Poster({ url, title, wide }: { url: string | null; title: string; wide?: boolean }) {
  const [failed, setFailed] = useState(false);
  const cls = wide ? "poster poster-wide" : "poster";
  if (!url || failed) {
    return (
      <div className={`${cls} poster-empty`} aria-hidden="true">
        <span>{title.trim().charAt(0).toUpperCase() || "?"}</span>
      </div>
    );
  }
  return <img className={cls} src={url} alt="" loading="lazy" onError={() => setFailed(true)} />;
}

export function formatDuration(ms: number | null): string {
  if (!ms) return "";
  if (ms < 60000) return `${Math.max(1, Math.round(ms / 1000))} s`;
  const min = Math.round(ms / 60000);
  if (min < 60) return `${min} min`;
  return `${Math.floor(min / 60)} h ${min % 60} min`;
}

const layouts: Record<number, string> = { 1: "mono", 2: "estéreo", 6: "5.1", 8: "7.1" };

export function formatChannels(n: number | null): string {
  if (!n) return "";
  return layouts[n] ?? `${n} canales`;
}

export function formatBytes(n: number | null): string {
  if (n === null) return "";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

export function formatDate(iso: string | null): string {
  if (!iso) return "—";
  return new Date(iso).toLocaleString("es-ES", { dateStyle: "medium", timeStyle: "short" });
}

// usePolling llama a reload cada ms milisegundos mientras la pestaña está a la
// vista: una pantalla de administración abierta en segundo plano no debe
// consultar al servidor.
export function usePolling(reload: () => void, ms: number) {
  useEffect(() => {
    const id = window.setInterval(() => {
      if (!document.hidden) reload();
    }, ms);
    return () => window.clearInterval(id);
  }, [reload, ms]);
}

export function formatBitrate(bps: number | null | undefined): string {
  if (!bps) return "—";
  if (bps >= 1_000_000) return `${(bps / 1_000_000).toFixed(1)} Mb/s`;
  return `${Math.round(bps / 1000)} kb/s`;
}

export function formatUptime(s: number): string {
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d) return `${d} d ${h} h`;
  if (h) return `${h} h ${m} min`;
  return `${m} min`;
}

// Bar es una barra de uso: ratio entre 0 y 1.
export function Bar({ ratio, label }: { ratio: number; label: string }) {
  const pct = Math.min(Math.max(ratio, 0), 1) * 100;
  const tone = pct >= 90 ? "bar-fill bar-high" : "bar-fill";
  return (
    <div className="bar" role="meter" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(pct)}>
      <div className={tone} style={{ width: `${pct}%` }} />
    </div>
  );
}
