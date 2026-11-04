import { useState } from "react";
import { api, ApiError } from "../api";
import type { AdminMetrics, AdminSession, AdminSystem, Transcodes } from "../api/types";
import { Link, navigate } from "../router";
import { Alert, Bar, Empty, PageHeader, Spinner, StatusBadge, errorText, formatBitrate, formatBytes, formatDate, formatDuration, formatUptime, useLoad, usePolling } from "../ui";
import { Check, Hardware, Logs, Network, Settings, Storage } from "./AdminSystem";

// Administración: una sola pantalla con pestañas (§19, §22.2). Solo lectura,
// salvo cancelar un trabajo de FFmpeg.

export const adminTabs = [
  { id: "overview", label: "Resumen" },
  { id: "sessions", label: "Sesiones" },
  { id: "transcoding", label: "Transcodificación" },
  { id: "hardware", label: "Hardware" },
  { id: "network", label: "Red" },
  { id: "storage", label: "Almacenamiento" },
  { id: "logs", label: "Registros" },
  { id: "settings", label: "Ajustes" },
  { id: "check", label: "Diagnóstico" },
] as const;

export type AdminTab = (typeof adminTabs)[number]["id"];

export function Admin({ tab }: { tab: string }) {
  const current = adminTabs.find((t) => t.id === tab)?.id ?? "overview";
  return (
    <>
      <PageHeader title="Administración" subtitle="Estado del servidor, sesiones, almacenamiento y diagnóstico" />
      <nav className="tabs" aria-label="Administración">
        {adminTabs.map((t) => (
          <Link key={t.id} to={`/admin/${t.id}`} className={t.id === current ? "tab active" : "tab"}>
            {t.label}
          </Link>
        ))}
      </nav>
      <section aria-live="polite">
        {current === "overview" && <Overview />}
        {current === "sessions" && <Sessions />}
        {current === "transcoding" && <Transcoding />}
        {current === "hardware" && <Hardware />}
        {current === "network" && <Network />}
        {current === "storage" && <Storage />}
        {current === "logs" && <Logs />}
        {current === "settings" && <Settings />}
        {current === "check" && <Check />}
      </section>
    </>
  );
}

export function goToAdmin(tab: AdminTab = "overview") {
  navigate(`/admin/${tab}`);
}

const REFRESH_MS = 5000;

function Overview() {
  const sys = useLoad(() => api.get<AdminSystem>("/admin/system"), []);
  const mt = useLoad(() => api.get<AdminMetrics>("/admin/metrics"), []);
  usePolling(mt.reload, REFRESH_MS);
  const m = mt.data;
  const s = sys.data;
  return (
    <>
      {sys.error && <Alert>{sys.error}</Alert>}
      {mt.error && <Alert>{mt.error}</Alert>}
      {!m && !mt.error && <Spinner />}
      {m && s && (
        <>
          <section className="cards">
            <div className="card stat">
              <span className="stat-label">CPU del servidor</span>
              <span className="stat-value">{m.cpu_percent.toFixed(1)} %</span>
              <Bar ratio={m.cpu_percent / 100} label="Uso de CPU" />
              <span className="muted small">{s.cpus} núcleos</span>
            </div>
            <div className="card stat">
              <span className="stat-label">Memoria</span>
              <span className="stat-value">{formatBytes(m.memory_bytes)}</span>
              <span className="muted small">{m.goroutines} tareas internas</span>
            </div>
            <div className="card stat">
              <span className="stat-label">Espacio libre</span>
              <span className="stat-value">{formatBytes(m.disk_free_bytes)}</span>
              <Bar ratio={m.disk_total_bytes ? 1 - m.disk_free_bytes / m.disk_total_bytes : 0} label="Disco usado" />
              <span className="muted small">de {formatBytes(m.disk_total_bytes)}</span>
            </div>
            <div className="card stat">
              <span className="stat-label">Archivos indexados</span>
              <span className="stat-value">{m.files_indexed.toLocaleString("es-ES")}</span>
              <span className="muted small">{s.counts.libraries ?? 0} bibliotecas</span>
            </div>
            <div className="card stat">
              <span className="stat-label">Reproducciones</span>
              <span className="stat-value">{m.active_sessions}</span>
              <span className="muted small">
                {m.connections_local} locales · {m.connections_remote} remotas · {formatBitrate(m.bitrate_bps)}
              </span>
            </div>
            <div className="card stat">
              <span className="stat-label">FFmpeg</span>
              <span className="stat-value">{m.active_jobs} trabajos</span>
              <span className="muted small">{m.transcode_failures} con error desde el arranque</span>
            </div>
          </section>

          <h2 className="section-title">Servidor</h2>
          <table className="table">
            <tbody>
              <tr>
                <td>Versión</td>
                <td>{s.version}</td>
              </tr>
              <tr>
                <td>Sistema</td>
                <td>
                  {s.os} · {s.arch}
                </td>
              </tr>
              <tr>
                <td>En marcha desde hace</td>
                <td>{formatUptime(s.uptime_seconds)}</td>
              </tr>
              <tr>
                <td>Base de datos</td>
                <td>
                  {formatBytes(s.database.size_bytes)} · {s.database.journal_mode.toUpperCase()}
                </td>
              </tr>
              <tr>
                <td>Configuración</td>
                <td className="mono">{s.config_path || "valores por defecto"}</td>
              </tr>
            </tbody>
          </table>

          <h2 className="section-title">Últimos escaneos</h2>
          {m.last_scans.length === 0 ? (
            <Empty title="Todavía no se ha escaneado nada" />
          ) : (
            <table className="table">
              <thead>
                <tr>
                  <th>Biblioteca</th>
                  <th>Estado</th>
                  <th>Duración</th>
                  <th>Archivos</th>
                  <th>Terminó</th>
                </tr>
              </thead>
              <tbody>
                {m.last_scans.map((x, i) => (
                  <tr key={i}>
                    <td>{x.library}</td>
                    <td>
                      <StatusBadge status={x.status} />
                    </td>
                    <td>{formatDuration(x.duration_seconds * 1000)}</td>
                    <td>{x.files_processed.toLocaleString("es-ES")}</td>
                    <td>{formatDate(x.finished_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}

          <h2 className="section-title">Latencia de los endpoints</h2>
          {m.endpoints.length === 0 ? (
            <Empty title="Todavía no hay peticiones medidas" />
          ) : (
            <table className="table">
              <thead>
                <tr>
                  <th>Ruta</th>
                  <th>Peticiones</th>
                  <th>Errores</th>
                  <th>p50</th>
                  <th>p95</th>
                  <th>Máximo</th>
                </tr>
              </thead>
              <tbody>
                {m.endpoints.map((e) => (
                  <tr key={e.route}>
                    <td className="mono">{e.route}</td>
                    <td>{e.count.toLocaleString("es-ES")}</td>
                    <td>{e.errors || "—"}</td>
                    <td>{e.p50_ms.toFixed(1)} ms</td>
                    <td>{e.p95_ms.toFixed(1)} ms</td>
                    <td>{e.max_ms.toFixed(1)} ms</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </>
      )}
    </>
  );
}

const modeLabels: Record<string, string> = { direct_play: "Directa", direct_stream: "Remux", transcode: "Transcodificación" };

function Sessions() {
  const list = useLoad(() => api.get<AdminSession[]>("/admin/sessions"), []);
  usePolling(list.reload, REFRESH_MS);
  if (list.error) return <Alert>{list.error}</Alert>;
  if (!list.data) return <Spinner />;
  if (list.data.length === 0) return <Empty title="No hay nadie reproduciendo ahora mismo" />;
  return (
    <table className="table">
      <thead>
        <tr>
          <th>Usuario</th>
          <th>Contenido</th>
          <th>Modo</th>
          <th>Progreso</th>
          <th>Bitrate</th>
          <th>Cliente</th>
        </tr>
      </thead>
      <tbody>
        {list.data.map((s) => (
          <tr key={s.id}>
            <td>{s.user || "—"}</td>
            <td>{s.title}</td>
            <td>
              {modeLabels[s.mode] ?? s.mode}
              {s.job && <span className="muted small"> · {s.job === "heavy" ? "vídeo" : "audio/remux"}</span>}
            </td>
            <td>
              {formatDuration(s.position_ms) || "0 s"}
              {s.duration_ms ? ` de ${formatDuration(s.duration_ms)}` : ""}
            </td>
            <td>{formatBitrate(s.bitrate_bps)}</td>
            <td>
              <span className="mono">{s.client_ip || "—"}</span>
              <span className="muted small"> · {s.local ? "red local" : "remoto"}</span>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function Transcoding() {
  const jobs = useLoad(() => api.get<Transcodes>("/admin/transcodes"), []);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  usePolling(jobs.reload, REFRESH_MS);

  const cancel = async (id: string) => {
    if (!window.confirm("Cancelar este trabajo cierra la reproducción de su sesión. ¿Seguro?")) return;
    setBusy(id);
    setError(null);
    try {
      await api.post(`/admin/transcodes/${id}/cancel`);
      jobs.reload();
    } catch (e) {
      setError(e instanceof ApiError && e.status === 404 ? "El trabajo ya había terminado." : errorText(e));
    } finally {
      setBusy(null);
    }
  };

  if (jobs.error) return <Alert>{jobs.error}</Alert>;
  if (!jobs.data) return <Spinner />;
  const caps = jobs.data.capacity;
  return (
    <>
      <section className="cards">
        {(["heavy", "light"] as const).map((k) => {
          const c = caps[k];
          return (
            <div className="card stat" key={k}>
              <span className="stat-label">{k === "heavy" ? "Recodificar vídeo" : "Remux y audio"}</span>
              <span className="stat-value">
                {c ? `${c.used} de ${c.limit}` : "—"}
              </span>
              {c && <Bar ratio={c.limit ? c.used / c.limit : 0} label={`Cupo ${k}`} />}
              <span className="muted small">
                {k === "heavy" ? "transcoding.max_concurrent_jobs" : "transcoding.max_concurrent_light_jobs"}
              </span>
            </div>
          );
        })}
      </section>
      {error && <Alert>{error}</Alert>}
      {jobs.data.jobs.length === 0 ? (
        <Empty title="No hay trabajos de FFmpeg en marcha" />
      ) : (
        <table className="table">
          <thead>
            <tr>
              <th>Tipo</th>
              <th>Codificador</th>
              <th>Posición</th>
              <th>Segmento</th>
              <th>Desde</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {jobs.data.jobs.map((j) => (
              <tr key={j.id}>
                <td>{j.kind === "heavy" ? "Vídeo" : "Remux"}</td>
                <td>{j.encoder ?? "copia"}</td>
                <td>{formatDuration(j.position_ms) || "0 s"}</td>
                <td>
                  {j.segment >= 0 ? j.segment : "—"} (empezó en {j.start_segment})
                </td>
                <td>{formatDate(j.started_at)}</td>
                <td>
                  <button className="btn btn-danger" disabled={busy === j.id} onClick={() => void cancel(j.id)}>
                    Cancelar
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  );
}
