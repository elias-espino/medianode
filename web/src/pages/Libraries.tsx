import { useEffect, useState, type FormEvent } from "react";
import { api, type Library, type ScanJob } from "../api";
import { useSession } from "../App";
import { Link } from "../router";
import { Alert, Empty, Field, PageHeader, Spinner, StatusBadge, errorText, formatDate, useLoad } from "../ui";

export function Libraries() {
  const { isAdmin } = useSession();
  const libs = useLoad(() => api.get<Library[]>("/libraries"), []);
  const [creating, setCreating] = useState(false);

  return (
    <>
      <PageHeader
        title="Bibliotecas"
        subtitle="Carpetas del servidor con películas o series"
        actions={
          isAdmin && (
            <button className="btn btn-primary" onClick={() => setCreating(true)}>
              Nueva biblioteca
            </button>
          )
        }
      />
      {creating && (
        <CreateLibrary
          onCancel={() => setCreating(false)}
          onCreated={() => {
            setCreating(false);
            libs.reload();
          }}
        />
      )}
      {libs.loading && !libs.data && <Spinner />}
      {libs.error && <Alert>{libs.error}</Alert>}
      {libs.data?.length === 0 && <Empty title="No hay bibliotecas visibles" />}
      <div className="stack">
        {libs.data?.map((l) => <LibraryCard key={l.id} lib={l} onChange={libs.reload} />)}
      </div>
    </>
  );
}

function CreateLibrary({ onCancel, onCreated }: { onCancel: () => void; onCreated: () => void }) {
  const [name, setName] = useState("");
  const [type, setType] = useState<"movies" | "tv">("movies");
  const [path, setPath] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const lib = await api.post<Library>("/libraries", { name, type, paths: path.trim() ? [path.trim()] : [] });
      if (path.trim()) await api.post(`/libraries/${lib.id}/scan`);
      onCreated();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form className="card stack" onSubmit={submit}>
      <h2>Nueva biblioteca</h2>
      {error && <Alert>{error}</Alert>}
      <div className="grid-3">
        <Field label="Nombre">
          <input required autoFocus maxLength={100} value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label="Tipo" hint="No se puede cambiar después">
          <select value={type} onChange={(e) => setType(e.target.value as "movies" | "tv")}>
            <option value="movies">Películas</option>
            <option value="tv">Series</option>
          </select>
        </Field>
        <Field label="Carpeta" hint="Ruta absoluta en el servidor">
          <input spellCheck={false} value={path} onChange={(e) => setPath(e.target.value)} />
        </Field>
      </div>
      <div className="row-end">
        <button type="button" className="btn btn-ghost" onClick={onCancel}>
          Cancelar
        </button>
        <button className="btn btn-primary" disabled={busy}>
          {busy ? "Creando…" : "Crear"}
        </button>
      </div>
    </form>
  );
}

// Un televisor no se autentica: lo que se publica por DLNA lo ve cualquier equipo de
// la red local (§13.2). El aviso va antes de activarlo, no después.
export function dlnaWarning(name: string): string {
  return (
    `¿Publicar «${name}» por DLNA?\n\n` +
    "Los televisores y reproductores DLNA no usan contraseña: cualquier equipo conectado a tu red local podrá ver y reproducir esta biblioteca, " +
    "sin cuenta y sin límites por usuario. No se publica fuera de tu red local.\n\n" +
    "Además, DLNA tiene que estar activado en la configuración del servidor (discovery.dlna_enabled)."
  );
}

function LibraryCard({ lib, onChange }: { lib: Library; onChange: () => void }) {
  const { isAdmin } = useSession();
  const [scan, setScan] = useState<ScanJob | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [newPath, setNewPath] = useState("");
  const [busy, setBusy] = useState(false);

  // El progreso se consulta por REST mientras haya un escaneo en curso; los
  // eventos por WebSocket llegarán en una fase posterior.
  useEffect(() => {
    let stop = false;
    let timer: number | undefined;
    const poll = async () => {
      try {
        const job = await api.get<ScanJob | undefined>(`/libraries/${lib.id}/scan`);
        if (stop) return;
        setScan(job ?? null);
        if (job?.status === "running") timer = window.setTimeout(poll, 1500);
      } catch {
        // Sin permiso o borrada: no se muestra el estado.
      }
    };
    void poll();
    return () => {
      stop = true;
      window.clearTimeout(timer);
    };
  }, [lib.id, scan?.status === "running" ? "running" : scan?.id]);

  const act = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      await fn();
      onChange();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };

  const startScan = () =>
    act(async () => {
      setScan(await api.post<ScanJob>(`/libraries/${lib.id}/scan`));
    });

  const addPath = (e: FormEvent) => {
    e.preventDefault();
    void act(async () => {
      await api.post(`/libraries/${lib.id}/paths`, { path: newPath.trim() });
      setNewPath("");
    });
  };

  const running = scan?.status === "running";

  return (
    <section className="card library">
      <div className="library-head">
        <div>
          <Link to={`/libraries/${lib.id}`} className="library-name">
            {lib.name}
          </Link>
          <span className="muted small"> · {lib.type === "movies" ? "Películas" : "Series"}</span>
          {!lib.enabled && <span className="badge badge-failed">Desactivada</span>}
          {lib.dlna_enabled && (
            <span className="badge badge-warn" title="Cualquier equipo de la red local la ve sin contraseña">
              DLNA
            </span>
          )}
        </div>
        <div className="row">
          <button className="btn" onClick={startScan} disabled={busy || running}>
            {running ? "Escaneando…" : "Escanear"}
          </button>
          {isAdmin && (
            <>
              <button
                className="btn btn-ghost"
                onClick={() => act(() => api.patch(`/libraries/${lib.id}`, { enabled: !lib.enabled }))}
                disabled={busy}
              >
                {lib.enabled ? "Desactivar" : "Activar"}
              </button>
              <button
                className="btn btn-ghost"
                disabled={busy}
                onClick={() => {
                  if (!lib.dlna_enabled && !confirm(dlnaWarning(lib.name))) return;
                  void act(() => api.patch(`/libraries/${lib.id}`, { dlna_enabled: !lib.dlna_enabled }));
                }}
              >
                {lib.dlna_enabled ? "Dejar de publicar por DLNA" : "Publicar por DLNA"}
              </button>
              <button
                className="btn btn-danger"
                disabled={busy}
                onClick={() => {
                  if (confirm(`¿Eliminar «${lib.name}» y todo su catálogo? Los archivos del disco no se tocan.`)) {
                    void act(() => api.del(`/libraries/${lib.id}`));
                  }
                }}
              >
                Eliminar
              </button>
            </>
          )}
        </div>
      </div>
      {error && <Alert>{error}</Alert>}

      {scan && (
        <div className="scan-status">
          <StatusBadge status={scan.status} />
          {running ? (
            <>
              <span>
                {scan.files_processed} de {scan.files_discovered || "?"} archivos
              </span>
              <progress
                max={Math.max(scan.files_discovered, 1)}
                value={scan.files_processed}
                aria-label="Progreso del escaneo"
              />
            </>
          ) : (
            <span className="muted small">
              {scan.files_discovered} archivos · {formatDate(scan.finished_at)}
              {scan.error_message && ` · ${scan.error_message}`}
            </span>
          )}
        </div>
      )}

      {isAdmin && lib.paths && (
        <div className="paths">
          {lib.paths.length === 0 && <p className="muted small">Sin carpetas: añade una para escanear.</p>}
          <ul>
            {lib.paths.map((p) => (
              <li key={p.id}>
                <code>{p.path}</code>
                {!p.is_available && <span className="badge badge-failed">No disponible</span>}
                <button
                  className="btn btn-link"
                  disabled={busy}
                  onClick={() => {
                    if (confirm("¿Quitar esta carpeta? Sus elementos desaparecen del catálogo al momento.")) {
                      void act(() => api.del(`/libraries/${lib.id}/paths/${p.id}`));
                    }
                  }}
                >
                  Quitar
                </button>
              </li>
            ))}
          </ul>
          <form className="row" onSubmit={addPath}>
            <input
              className="grow"
              placeholder="Añadir carpeta (ruta absoluta)"
              aria-label="Nueva carpeta"
              spellCheck={false}
              value={newPath}
              onChange={(e) => setNewPath(e.target.value)}
              required
            />
            <button className="btn" disabled={busy}>
              Añadir
            </button>
          </form>
        </div>
      )}
    </section>
  );
}
