import { useEffect, useState, type FormEvent } from "react";
import { api, qs } from "../api";
import type { AdminCheck, AdminLogEntry, AdminNetwork, AdminStorage, AdminSystem, AdminVolume, RemoteProbe, RemoteReport } from "../api/types";
import { Alert, Bar, Empty, Field, Spinner, errorText, formatBytes, formatDate, useLoad } from "../ui";

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
            <td>{n.tls_enabled ? "Activado, con el certificado propio" : "Desactivado: el servidor solo habla HTTP, sin cifrar"}</td>
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
            <td>DLNA</td>
            <td>{n.dlna_enabled ? "Activado: solo responde a la red local" : "Desactivado"}</td>
          </tr>
          <tr>
            <td>Redes extra tratadas como locales</td>
            <td className="mono">{n.local_networks.join(", ") || "ninguna"}</td>
          </tr>
          <tr>
            <td>Conexiones activas</td>
            <td>
              {n.connections_local} de la red local · {n.connections_remote} remotas
            </td>
          </tr>
        </tbody>
      </table>
      <RemoteAccess enabled={n.remote_enabled} adminOpen={n.remote_admin_enabled} publicURL={n.public_url} />
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

const probeHelp: Record<RemoteProbe["state"], string> = {
  none: "Genera una dirección de un solo uso y ábrela desde otra red, por ejemplo con los datos del móvil con el wifi apagado.",
  pending: "Abre la dirección desde otra red: desde esta no demuestra nada. Vale 10 minutos y una sola vez.",
  local_only: "Se abrió desde la red local, y eso no prueba nada. Ábrela desde otra red.",
  reachable: "El servidor es alcanzable desde fuera de la red local.",
  expired: "La prueba caducó sin que nadie la abriera desde fuera. Genera otra.",
};

// RemoteAccess muestra las comprobaciones de conectividad del acceso remoto
// (§14.2) y lanza la prueba desde fuera de la red local.
function RemoteAccess({ enabled, adminOpen, publicURL }: { enabled: boolean; adminOpen: boolean; publicURL: string }) {
  const report = useLoad(() => api.get<RemoteReport>("/admin/remote"), []);
  const [probe, setProbe] = useState<RemoteProbe | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const state = probe?.state ?? report.data?.probe.state ?? "none";

  // Mientras la prueba espera, se consulta el informe: quien abre la dirección
  // lo hace desde otro aparato.
  useEffect(() => {
    if (state !== "pending" && state !== "local_only") return;
    const t = window.setInterval(() => {
      void api
        .get<RemoteReport>("/admin/remote")
        .then((r) => setProbe(r.probe))
        .catch(() => undefined);
    }, 3000);
    return () => window.clearInterval(t);
  }, [state]);

  const start = async () => {
    setBusy(true);
    setError(null);
    try {
      setProbe(await api.post<RemoteProbe>("/admin/remote/probe"));
      report.reload();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  const current = probe ?? report.data?.probe;
  return (
    <>
      <h2 className="section-title">Acceso desde Internet</h2>
      {!enabled && (
        <Alert kind="info">
          Desactivado: el servidor solo responde a la red local. Se activa con remote.enabled y remote.direct_enabled, y exige HTTPS propio
          (server.https_enabled) o un reverse proxy declarado en server.trusted_proxies.
        </Alert>
      )}
      {enabled && adminOpen && (
        <Alert>La administración está abierta a Internet (remote.admin_enabled). Desactívala salvo que la necesites.</Alert>
      )}
      {report.error && <Alert>{report.error}</Alert>}
      {report.loading && !report.data && <Spinner label="Comprobando la conectividad…" />}
      {report.data && (
        <ul className="checks">
          {report.data.checks.map((c) => (
            <li key={c.id} className={`check check-${c.status}`}>
              <span className={`check-status status-${c.status}`}>{checkLabels[c.status]}</span>
              <div>
                <strong>{c.name}</strong>
                <p className="muted">{c.detail}</p>
              </div>
            </li>
          ))}
        </ul>
      )}
      {enabled && (
        <div className="card">
          <div className="row-between">
            <div>
              <strong>Prueba desde fuera de la red local</strong>
              <p className="muted">{probeHelp[state]}</p>
              {current?.state === "reachable" && current.from && (
                <p className="muted small">
                  Llegó una petición desde <span className="mono">{current.from}</span>.
                </p>
              )}
            </div>
            <div className="row">
              <button className="btn" onClick={() => report.reload()} disabled={report.loading}>
                Volver a comprobar
              </button>
              <button className="btn btn-primary" onClick={() => void start()} disabled={busy || !publicURL}>
                Generar prueba
              </button>
            </div>
          </div>
          {!publicURL && <p className="muted small">Falta remote.public_url: sin la dirección pública no hay a dónde llamar.</p>}
          {error && <Alert>{error}</Alert>}
          {current?.url && (state === "pending" || state === "local_only") && (
            <p>
              <input className="mono" readOnly value={current.url} onFocus={(e) => e.currentTarget.select()} aria-label="Dirección de la prueba" />
            </p>
          )}
        </div>
      )}
    </>
  );
}
