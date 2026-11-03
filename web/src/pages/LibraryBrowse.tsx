import { useCallback, useEffect, useState } from "react";
import { api, qs, type ItemPage, type ItemSummary, type Library, type MetadataStatus } from "../api";
import { Link } from "../router";
import { Alert, Empty, PageHeader, Poster, Spinner, errorText, useLoad } from "../ui";

// usePagedItems carga páginas por cursor y acumula los elementos.
export function usePagedItems(libraryId: string, status?: MetadataStatus) {
  const [items, setItems] = useState<ItemSummary[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [done, setDone] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(
    async (after: string | null, reset: boolean) => {
      setLoading(true);
      try {
        const page = await api.get<ItemPage>(
          `/libraries/${libraryId}/items` + qs({ cursor: after, limit: 60, metadata_status: status }),
        );
        setItems((prev) => (reset ? page.items : [...prev, ...page.items]));
        setCursor(page.next_cursor);
        setDone(page.next_cursor === null);
        setError(null);
      } catch (e) {
        setError(errorText(e));
      } finally {
        setLoading(false);
      }
    },
    [libraryId, status],
  );

  useEffect(() => {
    void load(null, true);
  }, [load]);

  return { items, loading, error, done, more: () => load(cursor, false), reload: () => load(null, true) };
}

export function LibraryBrowse({ id }: { id: string }) {
  const lib = useLoad(() => api.get<Library>(`/libraries/${id}`), [id]);
  const page = usePagedItems(id);

  return (
    <>
      <PageHeader
        title={lib.data?.name ?? "Biblioteca"}
        subtitle={lib.data ? (lib.data.type === "movies" ? "Películas" : "Series") : undefined}
      />
      {lib.error && <Alert>{lib.error}</Alert>}
      {page.error && <Alert>{page.error}</Alert>}
      {!page.loading && page.items.length === 0 && !page.error && (
        <Empty title="Esta biblioteca está vacía">Escanéala desde Bibliotecas para que aparezca su contenido.</Empty>
      )}
      <div className="poster-grid">
        {page.items.map((it) => (
          <Link key={it.id} to={`/media/${it.id}`} className="poster-card">
            <Poster url={it.poster_url} title={it.title} />
            <span className="poster-title">{it.title}</span>
            <span className="poster-meta">{it.year ?? " "}</span>
          </Link>
        ))}
      </div>
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
