import { useState, type FormEvent } from "react";
import { api, qs } from "../api";
import type { AdminCheck, AdminLogEntry, AdminNetwork, AdminStorage, AdminSystem, AdminVolume } from "../api/types";
import { Alert, Bar, Empty, Field, Spinner, formatBytes, formatDate, useLoad } from "../ui";

// Pestañas de administración que muestran el equipo y su configuración.

export function Hardware() {
  const sys = useLoad(() => api.get<AdminSystem>("/admin/system"), []);
  if (sys.error) return <Alert>{sys.error}</Alert>;
  if (!sys.data) return <Spinner />;
  const s = sys.data;
  const hw = s.hardware;
  return (
    <>
      <section className="cards">
        <div className="card stat">
          <span className="stat-label">Codificador en uso</span>
          <span className="stat-value">{hw.encoder || "—"}</span>
          <span className="muted small">{hw.encoder === "libx264" ? "En CPU" : hw.encoder ? "Por hardware" : "Sin FFmpeg"}</span>
        </div>
        <div className="card stat">
          <span className="stat-label">Procesador</span>
          <span className="stat-value">{s.cpus} núcleos</span>
          <span className="muted small">
            {s.os} · {s.arch}
          </span>
        </div>
        <div className="card stat">
          <span className="stat-label">Conversión de HDR a SDR</span>
          <span className="stat-value">{!hw.tone_mapping ? "No disponible" : hw.tone_mapping_speed ? `${hw.tone_mapping_speed.toFixed(2)}× tiempo real` : "Disponible"}</span>
          <span className="muted small">
            {hw.tone_mapping_speed !== null && hw.tone_mapping_speed < 1 ? "Este equipo no llega a tiempo real: la imagen se cortaría" : "720p, por software"}
          </span>
        </div>
        <div className="card stat">
          <span className="stat-label">Subtítulos grabados</span>
          <span className="stat-value">{hw.subtitle_burn_in ? "Disponibles" : "No disponibles"}</span>
          <span className="muted small">Texto e imagen</span>
        </div>
      </section>
      <h2 className="section-title">Programas</h2>
      <table className="table">
        <tbody>
          <tr>
            <td>FFmpeg</td>
            <td>{s.ffmpeg.available ? s.ffmpeg.version : "No está instalado"}</td>
          </tr>
          <tr>
            <td>FFprobe</td>
            <td>{s.ffprobe.available ? s.ffprobe.version || "Instalado" : "No está instalado"}</td>
          </tr>
        </tbody>
      </table>
      <h2 className="section-title">Codificadores de vídeo</h2>
      {hw.encoders_compiled.length === 0 ? (
        <Empty title="Esta build de FFmpeg no trae codificadores H.264 conocidos" />
      ) : (
        <table className="table">
          <thead>
            <tr>
              <th>Codificador</th>
              <th>En esta build</th>
              <th>Funciona en este equipo</th>
            </tr>
          </thead>
          <tbody>
            {hw.encoders_compiled.map((e) => (
              <tr key={e}>
                <td className="mono">{e}</td>
                <td>Sí</td>
                <td>{hw.encoders_working.includes(e) ? "Sí" : "No inicia"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  );
}

export function Network() {
  const net = useLoad(() => api.get<AdminNetwork>("/admin/network"), []);
  if (net.error) return <Alert>{net.error}</Alert>;
  if (!net.data) return <Spinner />;
  const n = net.data;
  const all = n.bind_address === "" || n.bind_address === "0.0.0.0" || n.bind_address === "::";
  return (
    <>
      <h2 className="section-title">Direcciones</h2>
      {n.addresses.length === 0 ? (
        <Empty title="No se encontraron direcciones de red">El servidor escucha en {n.bind_address || "todas las interfaces"}.</Empty>
      ) : (
        <ul className="plain-list">
          {n.addresses.map((a) => (
            <li key={a}>
              <a href={a} className="mono">
                {a}
              </a>
            </li>
          ))}
        </ul>
      )}
      {n.bind_address === "127.0.0.1" && (
        <Alert kind="info">El servidor solo escucha en esta máquina. Para entrar desde otros dispositivos, cambia server.bind_address a 0.0.0.0.</Alert>
      )}
      <h2 className="section-title">Configuración</h2>
      <table className="table">
        <tbody>
          <tr>
            <td>Dirección de escucha</td>
            <td className="mono">{n.bind_address || "todas"}{all ? " (todas las interfaces)" : ""}</td>
          </tr>
          <tr>
            <td>Puerto HTTP</td>
            <td className="mono">{n.http_port}</td>
          </tr>
          <tr>
            <td>HTTPS</td>
            <td>{n.https_enabled ? "Activado (todavía no implementado)" : "Desactivado"}</td>
          </tr>
          <tr>
            <td>Proxies de confianza</td>
            <td className="mono">{n.trusted_proxies.join(", ") || "ninguno"}</td>
          </tr>
          <tr>
            <td>Orígenes CORS</td>
            <td className="mono">{n.cors_origins.join(", ") || "solo el propio servidor"}</td>
          </tr>
          <tr>
            <td>Conexiones activas</td>
            <td>
              {n.connections_local} de la red local · {n.connections_remote} remotas
            </td>
          </tr>
        </tbody>
      </table>
    </>
  );
}

const folderLabels: Record<string, string> = {
  metadata: "Metadatos",
  images: "Imágenes",
  cache: "Caché (índices y subtítulos)",
  transcodes: "Transcodificación",
  logs: "Registros",
  backups: "Copias de seguridad",
};

function Volume({ v }: { v: AdminVolume }) {
  if (!v.readable) return <span className="badge badge-failed">No accesible</span>;
  const used = v.total_bytes ? 1 - v.free_bytes / v.total_bytes : 0;
  return (
    <div className="volume">
      <Bar ratio={used} label={`Disco de ${v.path}`} />
      <span className="muted small">
        {formatBytes(v.free_bytes)} libres de {formatBytes(v.total_bytes)}
      </span>
    </div>
  );
}

export function Storage() {
  const st = useLoad(() => api.get<AdminStorage>("/admin/storage"), []);
  if (st.error) return <Alert>{st.error}</Alert>;
  if (!st.data) return <Spinner label="Calculando el uso de disco…" />;
  const s = st.data;
  const total = s.folders.reduce((n, f) => n + f.size_bytes, 0) + s.database_bytes;
  return (
    <>
      <h2 className="section-title">Datos del servidor</h2>
      <p className="muted">
        Ocupan {formatBytes(total)} en <span className="mono">{s.data.path}</span>.
      </p>
      <Volume v={s.data} />
      <table className="table">
        <thead>
          <tr>
            <th>Carpeta</th>
            <th>Tamaño</th>
            <th>Archivos</th>
            <th>Ruta</th>
          </tr>
        </thead>
        <tbody>
          <tr>
            <td>Base de datos</td>
            <td>{formatBytes(s.database_bytes)}</td>
            <td>—</td>
            <td />
          </tr>
          {s.folders.map((f) => (
            <tr key={f.key}>
              <td>{folderLabels[f.key] ?? f.key}</td>
              <td>
                {f.partial ? "más de " : ""}
                {formatBytes(f.size_bytes)}
              </td>
              <td>{f.files.toLocaleString("es-ES")}</td>
              <td className="mono">{f.path}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <h2 className="section-title">Bibliotecas</h2>
      {s.libraries.length === 0 ? (
        <Empty title="Todavía no hay carpetas de bibliotecas" />
      ) : (
        <table className="table">
          <thead>
            <tr>
              <th>Biblioteca</th>
              <th>Carpeta</th>
              <th>Espacio</th>
            </tr>
          </thead>
          <tbody>
            {s.libraries.map((v) => (
              <tr key={v.library + v.path}>
                <td>{v.library}</td>
                <td className="mono">{v.path}</td>
                <td>
                  <Volume v={v} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  );
}

const levels = ["", "debug", "info", "warn", "error"] as const;
const levelNames: Record<string, string> = { "": "Todos", debug: "Depuración y más", info: "Info y más", warn: "Avisos y errores", error: "Solo errores" };

export function Logs() {
  const [level, setLevel] = useState<string>("info");
  const [q, setQ] = useState("");
  const [applied, setApplied] = useState({ level: "info", q: "" });
  const logs = useLoad(() => api.get<AdminLogEntry[]>("/admin/logs" + qs({ lines: 300, level: applied.level, q: applied.q })), [applied]);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setApplied({ level, q: q.trim() });
  };

  return (
    <>
      <form className="filters" onSubmit={submit}>
        <Field label="Nivel">
          <select value={level} onChange={(e) => setLevel(e.target.value)}>
            {levels.map((l) => (
              <option key={l} value={l}>
                {levelNames[l]}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Buscar">
          <input type="search" value={q} maxLength={200} onChange={(e) => setQ(e.target.value)} placeholder="Texto del mensaje, módulo o campos" />
        </Field>
        <button className="btn" type="submit">
          Filtrar
        </button>
        <button className="btn btn-ghost" type="button" onClick={logs.reload}>
          Actualizar
        </button>
      </form>
      <p className="muted small">Las URL de reproducción y los campos sensibles aparecen ocultos.</p>
      {logs.error && <Alert>{logs.error}</Alert>}
      {logs.loading && !logs.data && <Spinner />}
      {logs.data && logs.data.length === 0 && <Empty title="No hay líneas con ese filtro" />}
      {logs.data && logs.data.length > 0 && (
        <ol className="log-list">
          {logs.data.map((e, i) => (
            <li key={i} className={`log log-${e.level}`}>
              <span className="log-time">{formatDate(e.time)}</span>
              <span className="log-level">{e.level}</span>
              {e.module && <span className="log-module">{e.module}</span>}
              <span className="log-message">{e.message}</span>
              {e.fields && <code className="log-fields">{Object.entries(e.fields).map(([k, v]) => `${k}=${typeof v === "string" ? v : JSON.stringify(v)}`).join(" ")}</code>}
            </li>
          ))}
        </ol>
      )}
    </>
  );
}

function flatten(value: unknown, prefix = ""): [string, string][] {
  if (value && typeof value === "object" && !Array.isArray(value)) {
    return Object.entries(value as Record<string, unknown>).flatMap(([k, v]) => flatten(v, prefix ? `${prefix}.${k}` : k));
  }
  if (Array.isArray(value)) return [[prefix, value.length ? value.join(", ") : "(vacío)"]];
  return [[prefix, value === "" || value === null ? "(vacío)" : String(value)]];
}

export function Settings() {
  const cfg = useLoad(() => api.get<Record<string, unknown>>("/admin/settings"), []);
  if (cfg.error) return <Alert>{cfg.error}</Alert>;
  if (!cfg.data) return <Spinner />;
  const rows = flatten(cfg.data);
  return (
    <>
      <Alert kind="info">
        Es la configuración que el servidor está usando, sin secretos. Para cambiarla, edita <span className="mono">server.yaml</span> y reinicia.
      </Alert>
      <table className="table">
        <tbody>
          {rows.map(([k, v]) => (
            <tr key={k}>
              <td className="mono">{k}</td>
              <td className="mono">{v}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </>
  );
}

const checkLabels: Record<AdminCheck["status"], string> = { ok: "Correcto", warn: "Aviso", fail: "Falla", skipped: "No aplica" };

export function Check() {
  const checks = useLoad(() => api.get<AdminCheck[]>("/admin/system-check"), []);
  const failing = checks.data?.filter((c) => c.status === "fail").length ?? 0;
  const warning = checks.data?.filter((c) => c.status === "warn").length ?? 0;
  return (
    <>
      <div className="row-between">
        <p className="muted">
          {checks.data
            ? failing + warning === 0
              ? "Todo está en orden."
              : `${failing} fallo(s) y ${warning} aviso(s).`
            : "Ejecutando las comprobaciones…"}
        </p>
        <button className="btn" onClick={checks.reload} disabled={checks.loading}>
          Volver a comprobar
        </button>
      </div>
      {checks.error && <Alert>{checks.error}</Alert>}
      {checks.loading && !checks.data && <Spinner />}
      {checks.data && (
        <ul className="checks">
          {checks.data.map((c) => (
            <li key={c.id} className={`check check-${c.status}`}>
              <span className={`check-status status-${c.status}`}>{checkLabels[c.status]}</span>
              <div>
                <strong>{c.name}</strong>
                <p className="muted">{c.message}</p>
              </div>
            </li>
          ))}
        </ul>
      )}
    </>
  );
}
