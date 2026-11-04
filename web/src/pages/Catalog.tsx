import { api, type Library } from "../api";
import { Link } from "../router";
import { Alert, Empty, PageHeader, Poster, Spinner, useLoad } from "../ui";
import { usePagedItems } from "./LibraryBrowse";

// Películas y Series: el contenido de todas las bibliotecas de ese tipo a las
// que el usuario tiene acceso.

function Section({ library, showTitle }: { library: Library; showTitle: boolean }) {
  const page = usePagedItems(library.id);
  return (
    <section>
      {showTitle && (
        <div className="shelf-head">
          <h2 className="section-title">{library.name}</h2>
          <Link to={`/libraries/${library.id}`}>Abrir biblioteca</Link>
        </div>
      )}
      {page.error && <Alert>{page.error}</Alert>}
      {!page.loading && page.items.length === 0 && !page.error && <Empty title="Esta biblioteca está vacía" />}
      <div className="poster-grid">
        {page.items.map((it) => (
          <Link key={it.id} to={`/media/${it.id}`} className="poster-card">
            <Poster url={it.poster_url} title={it.title} />
            <span className="poster-title">{it.title}</span>
            <span className="poster-meta">{it.year ?? " "}</span>
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
    </section>
  );
}

export function Catalog({ kind }: { kind: "movies" | "tv" }) {
  const libs = useLoad(() => api.get<Library[]>("/libraries"), []);
  const mine = libs.data?.filter((l) => l.type === kind && l.enabled) ?? [];
  const title = kind === "movies" ? "Películas" : "Series";
  return (
    <>
      <PageHeader title={title} subtitle={mine.length > 1 ? `${mine.length} bibliotecas` : undefined} />
      {libs.error && <Alert>{libs.error}</Alert>}
      {libs.loading && !libs.data && <Spinner />}
      {libs.data && mine.length === 0 && (
        <Empty title={`Todavía no hay bibliotecas de ${title.toLowerCase()}`}>Un administrador puede crearlas desde Bibliotecas.</Empty>
      )}
      {mine.map((l) => (
        <Section key={l.id} library={l} showTitle={mine.length > 1} />
      ))}
    </>
  );
}
