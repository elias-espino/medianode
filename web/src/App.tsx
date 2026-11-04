import { createContext, useCallback, useContext, useEffect, useState, type FormEvent } from "react";
import { api, ApiError, setUnauthorizedHandler, type ServerInfo, type User } from "./api";
import { Link, match, navigate, useLocation } from "./router";
import { Alert, Spinner } from "./ui";
import { Login } from "./pages/Login";
import { Setup } from "./pages/Setup";
import { Dashboard } from "./pages/Dashboard";
import { Libraries } from "./pages/Libraries";
import { LibraryBrowse } from "./pages/LibraryBrowse";
import { ItemDetail } from "./pages/ItemDetail";
import { Users } from "./pages/Users";
import { Metadata } from "./pages/Metadata";
import { Search } from "./pages/Search";
import { Catalog } from "./pages/Catalog";
import { Admin } from "./pages/Admin";

interface Session {
  user: User;
  server: ServerInfo;
  isAdmin: boolean;
  logout: () => void;
}

const SessionContext = createContext<Session | null>(null);

export function useSession(): Session {
  const s = useContext(SessionContext);
  if (!s) throw new Error("sin sesión");
  return s;
}

type Phase = { kind: "loading" } | { kind: "error"; message: string } | { kind: "setup" } | { kind: "login" } | { kind: "ready"; user: User };

export function App() {
  const [server, setServer] = useState<ServerInfo | null>(null);
  const [phase, setPhase] = useState<Phase>({ kind: "loading" });

  const boot = useCallback(async () => {
    try {
      const info = await api.get<ServerInfo>("/server/info");
      setServer(info);
      if (info.setup_required) {
        setPhase({ kind: "setup" });
        return;
      }
      const user = await api.get<User>("/auth/me");
      setPhase({ kind: "ready", user });
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) setPhase({ kind: "login" });
      else setPhase({ kind: "error", message: "No se pudo contactar con el servidor" });
    }
  }, []);

  useEffect(() => {
    setUnauthorizedHandler(() => setPhase({ kind: "login" }));
    void boot();
  }, [boot]);

  const logout = useCallback(async () => {
    try {
      await api.post("/auth/logout");
    } catch {
      // La sesión ya podía estar cerrada.
    }
    setPhase({ kind: "login" });
    navigate("/", true);
  }, []);

  switch (phase.kind) {
    case "loading":
      return (
        <div className="center-screen">
          <Spinner />
        </div>
      );
    case "error":
      return (
        <div className="center-screen">
          <Alert>{phase.message}</Alert>
          <button className="btn" onClick={() => void boot()}>
            Reintentar
          </button>
        </div>
      );
    case "setup":
      return <Setup onDone={(user) => setPhase({ kind: "ready", user })} />;
    case "login":
      return <Login serverName={server?.name ?? "MediaNode"} onDone={(user) => setPhase({ kind: "ready", user })} />;
    case "ready": {
      const isAdmin = phase.user.role === "owner" || phase.user.role === "admin";
      return (
        <SessionContext.Provider value={{ user: phase.user, server: server!, isAdmin, logout }}>
          <Shell />
        </SessionContext.Provider>
      );
    }
  }
}

const nav = [
  { to: "/", label: "Inicio", admin: false },
  { to: "/movies", label: "Películas", admin: false },
  { to: "/series", label: "Series", admin: false },
  { to: "/libraries", label: "Bibliotecas", admin: false },
  { to: "/metadata", label: "Metadatos", admin: true },
  { to: "/users", label: "Usuarios", admin: true },
  { to: "/admin", label: "Administración", admin: true },
];

function Shell() {
  const { user, server, isAdmin, logout } = useSession();
  const { path, query } = useLocation();
  const [q, setQ] = useState(path === "/search" ? (query.get("q") ?? "") : "");
  const [provider, setProvider] = useState<string | null>(null);
  useEffect(() => {
    api
      .get<{ metadata_provider: string | null }>("/server/capabilities")
      .then((c) => setProvider(c.metadata_provider))
      .catch(() => {});
  }, []);

  const onSearch = (e: FormEvent) => {
    e.preventDefault();
    if (q.trim()) navigate("/search?q=" + encodeURIComponent(q.trim()));
  };

  let page;
  let params: Record<string, string> | null;
  if (path === "/") page = <Dashboard />;
  else if (path === "/movies") page = <Catalog kind="movies" />;
  else if (path === "/series") page = <Catalog kind="tv" />;
  else if (path === "/libraries") page = <Libraries />;
  else if ((params = match("/libraries/:id", path))) page = <LibraryBrowse key={params.id} id={params.id} />;
  else if ((params = match("/media/:id", path))) page = <ItemDetail key={params.id} id={params.id} />;
  else if (path === "/search") page = <Search key={query.get("q")} q={query.get("q") ?? ""} />;
  else if (path === "/users" && isAdmin) page = <Users />;
  else if (path === "/metadata" && isAdmin) page = <Metadata />;
  else if (path === "/admin" && isAdmin) page = <Admin tab="overview" />;
  else if ((params = match("/admin/:tab", path)) && isAdmin) page = <Admin tab={params.tab} />;
  else page = <Alert kind="info">Esta página no existe.</Alert>;

  const active = (to: string) => (to === "/" ? path === "/" : path === to || path.startsWith(to + "/"));

  return (
    <div className="shell">
      <aside className="sidebar">
        <Link to="/" className="brand">
          <span className="brand-mark" aria-hidden="true" />
          {server.name}
        </Link>
        <nav aria-label="Principal">
          {nav
            .filter((n) => !n.admin || isAdmin)
            .map((n) => (
              <Link key={n.to} to={n.to} className={active(n.to) ? "nav-link active" : "nav-link"}>
                {n.label}
              </Link>
            ))}
        </nav>
        <div className="sidebar-foot">
          {provider === "tmdb" && (
            // Texto que exigen las condiciones de uso de la API de TMDB (§3).
            <p className="muted small attribution">
              This product uses TMDB and the TMDB APIs but is not endorsed, certified, or otherwise approved by TMDB.
            </p>
          )}
          <span className="muted small">v{server.version}</span>
        </div>
      </aside>
      <div className="main">
        <header className="topbar">
          <form className="search" role="search" onSubmit={onSearch}>
            <input
              type="search"
              placeholder="Buscar películas y series"
              aria-label="Buscar"
              value={q}
              onChange={(e) => setQ(e.target.value)}
              maxLength={200}
            />
          </form>
          <div className="user-menu">
            <span className="user-name">{user.username}</span>
            <button className="btn btn-ghost" onClick={logout}>
              Salir
            </button>
          </div>
        </header>
        <main className="content">{page}</main>
      </div>
    </div>
  );
}
