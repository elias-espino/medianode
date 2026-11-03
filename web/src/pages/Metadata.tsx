import { useState } from "react";
import { api, type Library, type MetadataStatus, type ServerStatus } from "../api";
import { Link } from "../router";
import { Alert, Empty, PageHeader, Poster, Spinner, StatusBadge, formatDate, useLoad } from "../ui";
import { usePagedItems } from "./LibraryBrowse";

// Elementos que necesitan una identificación manual (§8.3).
export function Metadata() {
  const status = useLoad(() => api.get<ServerStatus>("/server/status"), []);
  const libs = useLoad(() => api.get<Library[]>("/libraries"), []);
  const [libId, setLibId] = useState<string>("");
  const [filter, setFilter] = useState<MetadataStatus>("unmatched");
  const md = status.data?.admin?.metadata;
  const selected = libId || libs.data?.[0]?.id || "";

  return (
    <>
      <PageHeader title="Metadatos" subtitle="Identificación y corrección del catálogo" />
      {md && !md.configured && (
        <Alert kind="info">
          No hay proveedor configurado: el catálogo usa los títulos de los archivos. Añade una clave de TMDB en{" "}
          <code>metadata.tmdb_api_key</code> o en la variable <code>MEDIANODE_METADATA_TMDB_API_KEY</code> y reinicia el
          servidor. Puedes editar los datos a mano igualmente.
        </Alert>
      )}
      {md?.configured && (
        <p className="muted">
          Proveedor {md.provider?.toUpperCase()} · {md.pending} pendientes
          {md.paused_until && ` · en pausa hasta ${formatDate(md.paused_until)}`}
        </p>
      )}
      {md?.last_error && <Alert>{md.last_error}</Alert>}

      <div className="row filters">
        <select aria-label="Biblioteca" value={selected} onChange={(e) => setLibId(e.target.value)}>
          {libs.data?.map((l) => (
            <option key={l.id} value={l.id}>
              {l.name}
            </option>
          ))}
        </select>
        <div className="segmented" role="group" aria-label="Estado">
          {(["unmatched", "failed", "pending"] as MetadataStatus[]).map((s) => (
            <button key={s} className={filter === s ? "active" : ""} aria-pressed={filter === s} onClick={() => setFilter(s)}>
              <StatusBadge status={s} />
            </button>
          ))}
        </div>
      </div>
      {libs.data?.length === 0 && <Empty title="No hay bibliotecas" />}
      {selected && <StatusList key={selected + filter} libraryId={selected} status={filter} />}
    </>
  );
}

function StatusList({ libraryId, status }: { libraryId: string; status: MetadataStatus }) {
  const page = usePagedItems(libraryId, status);
  if (page.error) return <Alert>{page.error}</Alert>;
  return (
    <>
      {!page.loading && page.items.length === 0 && <Empty title="Nada en este estado" />}
      <ul className="matches">
        {page.items.map((it) => (
          <li key={it.id} className="match">
            <Poster url={it.poster_url} title={it.title} />
            <div className="grow">
              <strong>{it.title}</strong> {it.year && <span className="muted">({it.year})</span>}
            </div>
            <Link to={`/media/${it.id}`} className="btn">
              Revisar
            </Link>
          </li>
        ))}
      </ul>
      {page.loading && <Spinner />}
      {!page.done && !page.loading && page.items.length > 0 && (
        <div className="row-center">
          <button className="btn" onClick={() => void page.more()}>
            Cargar más
          </button>
        </div>
      )}
    </>
  );
}
