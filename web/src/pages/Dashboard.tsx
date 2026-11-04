import { api, type Library, type ServerStatus } from "../api";
import type { ContinueItem } from "../api/types";
import { useSession } from "../App";
import { Link } from "../router";
import { Alert, Empty, PageHeader, Poster, Spinner, formatDate, useLoad } from "../ui";

function uptime(s: number): string {
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d) return `${d} d ${h} h`;
  if (h) return `${h} h ${m} min`;
  return `${m} min`;
}

// ContinueShelf lista lo empezado y sin terminar, lo más reciente primero.
function ContinueShelf() {
  const items = useLoad(() => api.get<ContinueItem[]>("/playback/continue-watching?limit=12"), []);
  if (items.error) return <Alert>{items.error}</Alert>;
  if (!items.data || items.data.length === 0) return null;
  return (
    <>
      <h2 className="section-title">Continuar viendo</h2>
      <div className="shelf">
        {items.data.map((it) => {
          const label =
            it.type === "episode" && it.series_title
              ? `${it.series_title} · T${it.season_number ?? "?"} E${it.episode_number ?? "?"}`
              : it.year
                ? String(it.year)
                : "";
          const ratio = it.duration_ms ? Math.min(it.position_ms / it.duration_ms, 1) : 0;
          return (
            <Link key={it.id} to={`/media/${it.id}`} className="poster-card">
              <Poster url={it.image_url ?? null} title={it.title} />
              <span className="poster-title">{it.title}</span>
              <span className="poster-meta">{label}</span>
              <div className="progress" role="meter" aria-label="Progreso" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(ratio * 100)}>
                <span style={{ width: `${ratio * 100}%` }} />
              </div>
            </Link>
          );
        })}
      </div>
    </>
  );
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

      <ContinueShelf />

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
