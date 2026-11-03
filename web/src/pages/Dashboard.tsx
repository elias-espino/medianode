import { api, type Library, type ServerStatus } from "../api";
import { useSession } from "../App";
import { Link } from "../router";
import { Alert, Empty, PageHeader, Spinner, formatDate, useLoad } from "../ui";

function uptime(s: number): string {
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d) return `${d} d ${h} h`;
  if (h) return `${h} h ${m} min`;
  return `${m} min`;
}

export function Dashboard() {
  const { isAdmin, user } = useSession();
  const status = useLoad(() => api.get<ServerStatus>("/server/status"), []);
  const libs = useLoad(() => api.get<Library[]>("/libraries"), []);

  const admin = status.data?.admin;
  const md = admin?.metadata;

  return (
    <>
      <PageHeader title={`Hola, ${user.username}`} subtitle="Estado del servidor y tus bibliotecas" />
      {status.error && <Alert>{status.error}</Alert>}

      {isAdmin && admin && md && (
        <section className="cards">
          <div className="card stat">
            <span className="stat-label">Servidor</span>
            <span className="stat-value">En marcha</span>
            <span className="muted small">desde hace {uptime(status.data!.uptime_seconds)}</span>
          </div>
          <div className="card stat">
            <span className="stat-label">Escaneos</span>
            <span className="stat-value">{admin.scans_running ? `${admin.scans_running} en curso` : "Ninguno en curso"}</span>
            <span className="muted small">{admin.libraries} bibliotecas</span>
          </div>
          <div className="card stat">
            <span className="stat-label">Metadatos</span>
            <span className="stat-value">
              {!md.configured ? "Sin proveedor" : md.pending ? `${md.pending} pendientes` : "Al día"}
            </span>
            <span className="muted small">
              {md.configured ? `Proveedor: ${md.provider?.toUpperCase()}` : "Configura metadata.tmdb_api_key"}
            </span>
          </div>
        </section>
      )}
      {isAdmin && admin && !admin.ffprobe_available && (
        <Alert>FFprobe no está disponible: los escaneos fallarán hasta instalarlo y reiniciar el servidor.</Alert>
      )}
      {isAdmin && md?.last_error && (
        <Alert>
          {md.last_error}
          {md.paused_until && <> · La cola se reanuda a las {formatDate(md.paused_until)}</>}
        </Alert>
      )}

      <h2 className="section-title">Bibliotecas</h2>
      {libs.loading && !libs.data && <Spinner />}
      {libs.error && <Alert>{libs.error}</Alert>}
      {libs.data && libs.data.length === 0 && (
        <Empty title="Todavía no hay bibliotecas">
          {isAdmin ? <Link to="/libraries">Crea la primera</Link> : "Pide a un administrador que te dé acceso a alguna."}
        </Empty>
      )}
      <div className="library-tiles">
        {libs.data?.map((l) => (
          <Link key={l.id} to={`/libraries/${l.id}`} className="card tile">
            <span className="tile-kind">{l.type === "movies" ? "Películas" : "Series"}</span>
            <span className="tile-name">{l.name}</span>
            {!l.enabled && <span className="badge badge-failed">Desactivada</span>}
          </Link>
        ))}
      </div>
    </>
  );
}
