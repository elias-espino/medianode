import { api, qs, type SearchResult } from "../api";
import { Link } from "../router";
import { Alert, Empty, PageHeader, Spinner, useLoad } from "../ui";

const typeLabels: Record<SearchResult["type"], string> = {
  movie: "Película",
  series: "Serie",
  episode: "Episodio",
};

export function Search({ q }: { q: string }) {
  const res = useLoad(
    () => (q.trim() ? api.get<SearchResult[]>("/search" + qs({ q, limit: 50 })) : Promise.resolve([])),
    [q],
  );
  return (
    <>
      <PageHeader title="Búsqueda" subtitle={q ? `Resultados para «${q}»` : undefined} />
      {res.loading && <Spinner />}
      {res.error && <Alert>{res.error}</Alert>}
      {res.data?.length === 0 && !res.loading && <Empty title="Sin resultados">Prueba con menos palabras o sin tildes.</Empty>}
      <ul className="results">
        {res.data?.map((r) => (
          <li key={r.id}>
            <Link to={`/media/${r.id}`} className="result">
              <span className="eyebrow">{typeLabels[r.type]}</span>
              <span className="result-title">
                {r.title} {r.year && <span className="muted">({r.year})</span>}
              </span>
              {r.parent_title && <span className="muted small">{r.parent_title}</span>}
            </Link>
          </li>
        ))}
      </ul>
    </>
  );
}
