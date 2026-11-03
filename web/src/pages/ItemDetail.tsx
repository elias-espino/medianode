import { useState, type FormEvent } from "react";
import { api, qs, type Child, type Item, type Match, type MediaFile, type Stream } from "../api";
import { useSession } from "../App";
import { Link } from "../router";
import { Player } from "../Player";
import {
  Alert,
  Field,
  Poster,
  Spinner,
  StatusBadge,
  errorText,
  formatBytes,
  formatChannels,
  formatDuration,
  useLoad,
} from "../ui";

const typeLabels: Record<Item["type"], string> = {
  movie: "Película",
  series: "Serie",
  season: "Temporada",
  episode: "Episodio",
};

const roleLabels: Record<string, string> = {
  director: "Dirección",
  writer: "Guion",
  creator: "Creación",
};

export function ItemDetail({ id }: { id: string }) {
  const { isAdmin } = useSession();
  const [playing, setPlaying] = useState(false);
  const item = useLoad(() => api.get<Item>(`/media/${id}`), [id]);
  const hasChildren = item.data?.type === "series" || item.data?.type === "season";
  const children = useLoad(
    () => (hasChildren ? api.get<Child[]>(`/media/${id}/children`) : Promise.resolve([] as Child[])),
    [id, hasChildren],
  );

  if (item.loading && !item.data) return <Spinner />;
  if (item.error) return <Alert>{item.error}</Alert>;
  const it = item.data!;
  const cast = it.people.filter((p) => p.role === "actor");
  const crew = it.people.filter((p) => p.role !== "actor");
  const poster = it.images.poster ?? it.images.thumbnail ?? null;

  return (
    <>
      <section className="hero">
        {it.images.backdrop && <img className="hero-backdrop" src={it.images.backdrop} alt="" />}
        <div className="hero-body">
          <Poster url={poster} title={it.title} wide={it.type === "episode"} />
          <div className="hero-info">
            {it.parent_id && (
              <Link to={`/media/${it.parent_id}`} className="muted small">
                ← Volver
              </Link>
            )}
            <span className="eyebrow">{typeLabels[it.type]}</span>
            <h1>{it.title}</h1>
            {it.original_title && it.original_title !== it.title && <p className="muted">{it.original_title}</p>}
            <p className="facts">
              {[
                it.year,
                it.age_rating && `Clasificación ${it.age_rating.replace(":", " ")}`,
                formatDuration(it.duration_ms),
                it.genres.join(", "),
              ]
                .filter(Boolean)
                .join(" · ")}
            </p>
            {it.overview && <p className="overview">{it.overview}</p>}
            {(it.type === "movie" || it.type === "episode") && !playing && (
              <div>
                <button className="btn btn-primary" onClick={() => setPlaying(true)}>
                  Reproducir
                </button>
              </div>
            )}
            {crew.length > 0 && (
              <dl className="credits">
                {Object.entries(groupBy(crew, (p) => p.role)).map(([role, people]) => (
                  <div key={role}>
                    <dt>{roleLabels[role] ?? role}</dt>
                    <dd>{people.map((p) => p.name).join(", ")}</dd>
                  </div>
                ))}
              </dl>
            )}
          </div>
        </div>
      </section>

      {playing && <Player itemId={id} title={it.title} onClose={() => setPlaying(false)} />}

      {cast.length > 0 && (
        <section>
          <h2 className="section-title">Reparto</h2>
          <ul className="cast">
            {cast.map((p) => (
              <li key={p.id}>
                <span className="cast-name">{p.name}</span>
                {p.character && <span className="muted small">{p.character}</span>}
              </li>
            ))}
          </ul>
        </section>
      )}

      {hasChildren && (
        <section>
          <h2 className="section-title">{it.type === "series" ? "Temporadas" : "Episodios"}</h2>
          {children.error && <Alert>{children.error}</Alert>}
          {it.type === "series" ? (
            <div className="poster-grid">
              {children.data?.map((c) => (
                <Link key={c.id} to={`/media/${c.id}`} className="poster-card">
                  <Poster url={c.image_url} title={c.title} />
                  <span className="poster-title">{c.title}</span>
                </Link>
              ))}
            </div>
          ) : (
            <ol className="episodes">
              {children.data?.map((c) => (
                <li key={c.id}>
                  <Link to={`/media/${c.id}`} className="episode">
                    <Poster url={c.image_url} title={c.title} wide />
                    <div>
                      <span className="episode-title">
                        {c.episode_number}. {c.title}
                      </span>
                      <span className="muted small">
                        {[c.release_date, formatDuration(c.duration_ms)].filter(Boolean).join(" · ")}
                      </span>
                      {c.overview && <p className="episode-overview">{c.overview}</p>}
                    </div>
                  </Link>
                </li>
              ))}
            </ol>
          )}
        </section>
      )}

      {(it.type === "movie" || it.type === "episode") && <Technical id={id} />}
      {isAdmin && <MetadataPanel item={it} onChange={item.reload} />}
    </>
  );
}

function groupBy<T>(list: T[], key: (t: T) => string): Record<string, T[]> {
  const out: Record<string, T[]> = {};
  for (const x of list) (out[key(x)] ??= []).push(x);
  return out;
}

function Technical({ id }: { id: string }) {
  const [open, setOpen] = useState(false);
  const files = useLoad(() => (open ? api.get<MediaFile[]>(`/media/${id}/files`) : Promise.resolve([])), [id, open]);
  const streams = useLoad(() => (open ? api.get<Stream[]>(`/media/${id}/streams`) : Promise.resolve([])), [id, open]);

  return (
    <section className="card">
      <button className="disclosure" aria-expanded={open} onClick={() => setOpen(!open)}>
        Archivos y pistas
      </button>
      {open && (
        <div className="stack">
          {files.error && <Alert>{files.error}</Alert>}
          {files.data?.map((f) => (
            <div key={f.id} className="file">
              <div className="file-head">
                <strong>{f.relative_path ?? f.container?.toUpperCase() ?? "Archivo"}</strong>
                {f.missing_since && <span className="badge badge-failed">No encontrado</span>}
                {f.probe_status === "failed" && <span className="badge badge-failed">Análisis fallido</span>}
              </div>
              <span className="muted small">
                {[f.container?.toUpperCase(), formatBytes(f.file_size), formatDuration(f.duration_ms),
                  f.bitrate && `${Math.round(f.bitrate / 1000)} kb/s`]
                  .filter(Boolean)
                  .join(" · ")}
              </span>
              <table className="streams">
                <tbody>
                  {streams.data
                    ?.filter((s) => s.file_id === f.id && s.type !== "attachment" && s.type !== "data")
                    .map((s) => (
                      <tr key={s.id}>
                        <td>{s.type === "video" ? "Vídeo" : s.type === "audio" ? "Audio" : "Subtítulos"}</td>
                        <td>{s.codec?.toUpperCase()}</td>
                        <td>
                          {[
                            s.width && s.height && `${s.width}×${s.height}`,
                            s.bit_depth && s.type === "video" && `${s.bit_depth} bits`,
                            s.hdr_format,
                            formatChannels(s.channels),
                            s.language,
                            s.title,
                            s.default && "por defecto",
                            s.forced && "forzado",
                          ]
                            .filter(Boolean)
                            .join(" · ")}
                        </td>
                      </tr>
                    ))}
                </tbody>
              </table>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

function MetadataPanel({ item, onChange }: { item: Item; onChange: () => void }) {
  const [mode, setMode] = useState<"idle" | "identify" | "edit">("idle");
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const topLevel = item.type === "movie" || item.type === "series";

  const act = async (fn: () => Promise<unknown>, ok?: string) => {
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      await fn();
      if (ok) setNotice(ok);
      onChange();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="card stack">
      <div className="library-head">
        <div className="row">
          <h2>Metadatos</h2>
          <StatusBadge status={item.metadata_status} />
          {item.metadata_locked && <span className="badge badge-locked">Bloqueado</span>}
          {item.external_ids.tmdb && <span className="muted small">TMDB {item.external_ids.tmdb}</span>}
        </div>
        <div className="row">
          {topLevel && (
            <>
              <button className="btn" disabled={busy || item.metadata_locked} onClick={() => setMode("identify")}>
                Identificar
              </button>
              <button
                className="btn"
                disabled={busy || item.metadata_locked}
                onClick={() => act(() => api.post(`/media/${item.id}/refresh`), "Actualización en cola")}
              >
                Actualizar
              </button>
            </>
          )}
          <button className="btn" disabled={busy} onClick={() => setMode("edit")}>
            Editar
          </button>
          <button
            className="btn btn-ghost"
            disabled={busy}
            onClick={() => act(() => api.patch(`/media/${item.id}/metadata`, { locked: !item.metadata_locked }))}
          >
            {item.metadata_locked ? "Desbloquear" : "Bloquear"}
          </button>
        </div>
      </div>
      {item.metadata_locked && (
        <p className="muted small">El proveedor no sobrescribirá estos datos mientras estén bloqueados.</p>
      )}
      {error && <Alert>{error}</Alert>}
      {notice && <Alert kind="ok">{notice}</Alert>}
      {mode === "identify" && (
        <Identify
          item={item}
          onClose={() => setMode("idle")}
          onChosen={() => {
            setMode("idle");
            onChange();
          }}
        />
      )}
      {mode === "edit" && (
        <EditForm
          item={item}
          onClose={() => setMode("idle")}
          onSaved={() => {
            setMode("idle");
            onChange();
          }}
        />
      )}
    </section>
  );
}

function Identify({ item, onClose, onChosen }: { item: Item; onClose: () => void; onChosen: () => void }) {
  const [query, setQuery] = useState(item.title);
  const [year, setYear] = useState(item.year?.toString() ?? "");
  const [matches, setMatches] = useState<Match[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const search = async (e?: FormEvent) => {
    e?.preventDefault();
    setBusy(true);
    setError(null);
    try {
      setMatches(await api.get<Match[]>(`/media/${item.id}/matches` + qs({ query, year })));
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };

  const choose = async (m: Match) => {
    setBusy(true);
    setError(null);
    try {
      await api.post(`/media/${item.id}/identify`, { provider: m.provider, external_id: m.external_id });
      onChosen();
    } catch (err) {
      setError(errorText(err));
      setBusy(false);
    }
  };

  return (
    <div className="stack">
      <form className="row" onSubmit={search}>
        <input className="grow" aria-label="Título" value={query} onChange={(e) => setQuery(e.target.value)} required />
        <input
          className="input-year"
          aria-label="Año"
          placeholder="Año"
          inputMode="numeric"
          pattern="[0-9]{4}"
          value={year}
          onChange={(e) => setYear(e.target.value)}
        />
        <button className="btn btn-primary" disabled={busy}>
          Buscar
        </button>
        <button type="button" className="btn btn-ghost" onClick={onClose}>
          Cerrar
        </button>
      </form>
      {error && <Alert>{error}</Alert>}
      {busy && <Spinner label="Consultando al proveedor…" />}
      {matches?.length === 0 && <p className="muted">Sin resultados. Prueba con otro título o sin año.</p>}
      <ul className="matches">
        {matches?.map((m) => (
          <li key={m.external_id} className="match">
            <Poster url={m.poster_url} title={m.title} />
            <div className="grow">
              <strong>{m.title}</strong> {m.year && <span className="muted">({m.year})</span>}
              {m.original_title && m.original_title !== m.title && <div className="muted small">{m.original_title}</div>}
              {m.overview && <p className="match-overview">{m.overview}</p>}
            </div>
            <button className="btn" disabled={busy} onClick={() => void choose(m)}>
              Elegir
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}

function EditForm({ item, onClose, onSaved }: { item: Item; onClose: () => void; onSaved: () => void }) {
  const [form, setForm] = useState({
    title: item.title,
    original_title: item.original_title ?? "",
    year: item.year?.toString() ?? "",
    release_date: item.release_date ?? "",
    age_rating: item.age_rating ?? "",
    min_age: item.min_age?.toString() ?? "",
    overview: item.overview ?? "",
  });
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const set = (k: keyof typeof form) => (e: { target: { value: string } }) => setForm({ ...form, [k]: e.target.value });

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    const num = (s: string) => (s.trim() === "" ? null : Number(s));
    const txt = (s: string) => (s.trim() === "" ? null : s);
    try {
      await api.patch(`/media/${item.id}/metadata`, {
        title: form.title,
        original_title: txt(form.original_title),
        year: num(form.year),
        release_date: txt(form.release_date),
        age_rating: txt(form.age_rating),
        min_age: num(form.min_age),
        overview: txt(form.overview),
      });
      onSaved();
    } catch (err) {
      setError(errorText(err));
      setBusy(false);
    }
  };

  return (
    <form className="stack" onSubmit={submit}>
      {error && <Alert>{error}</Alert>}
      <div className="grid-2">
        <Field label="Título">
          <input required maxLength={500} value={form.title} onChange={set("title")} />
        </Field>
        <Field label="Título original">
          <input maxLength={500} value={form.original_title} onChange={set("original_title")} />
        </Field>
      </div>
      <div className="grid-4">
        <Field label="Año">
          <input type="number" min={1800} max={2200} value={form.year} onChange={set("year")} />
        </Field>
        <Field label="Estreno">
          <input type="date" value={form.release_date} onChange={set("release_date")} />
        </Field>
        <Field label="Clasificación" hint="Para mostrar, p. ej. ES:12">
          <input maxLength={50} value={form.age_rating} onChange={set("age_rating")} />
        </Field>
        <Field label="Edad mínima" hint="Filtra a los usuarios restringidos">
          <input type="number" min={0} max={21} value={form.min_age} onChange={set("min_age")} />
        </Field>
      </div>
      <Field label="Sinopsis">
        <textarea rows={4} maxLength={10000} value={form.overview} onChange={set("overview")} />
      </Field>
      <p className="muted small">Guardar bloquea los metadatos para que el proveedor no deshaga tus cambios.</p>
      <div className="row-end">
        <button type="button" className="btn btn-ghost" onClick={onClose}>
          Cancelar
        </button>
        <button className="btn btn-primary" disabled={busy}>
          Guardar
        </button>
      </div>
    </form>
  );
}
