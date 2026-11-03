import { useEffect, useState, type MouseEvent, type ReactNode } from "react";

// Enrutador mínimo sobre la History API: el panel tiene pocas pantallas y no
// justifica una dependencia. El servidor devuelve index.html en cualquier ruta.

const listeners = new Set<() => void>();

export function navigate(to: string, replace = false) {
  if (to === location.pathname + location.search) return;
  if (replace) history.replaceState(null, "", to);
  else history.pushState(null, "", to);
  listeners.forEach((fn) => fn());
  window.scrollTo(0, 0);
}

window.addEventListener("popstate", () => listeners.forEach((fn) => fn()));

export function useLocation(): { path: string; query: URLSearchParams } {
  const [, force] = useState(0);
  useEffect(() => {
    const fn = () => force((n) => n + 1);
    listeners.add(fn);
    return () => {
      listeners.delete(fn);
    };
  }, []);
  return { path: location.pathname, query: new URLSearchParams(location.search) };
}

// match compara un patrón como «/media/:id» y devuelve sus parámetros.
export function match(pattern: string, path: string): Record<string, string> | null {
  const p = pattern.split("/").filter(Boolean);
  const s = path.split("/").filter(Boolean);
  if (p.length !== s.length) return null;
  const out: Record<string, string> = {};
  for (let i = 0; i < p.length; i++) {
    if (p[i].startsWith(":")) out[p[i].slice(1)] = decodeURIComponent(s[i]);
    else if (p[i] !== s[i]) return null;
  }
  return out;
}

export function Link(props: { to: string; className?: string; children: ReactNode; title?: string }) {
  const onClick = (e: MouseEvent<HTMLAnchorElement>) => {
    if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    e.preventDefault();
    navigate(props.to);
  };
  return (
    <a href={props.to} className={props.className} onClick={onClick} title={props.title}>
      {props.children}
    </a>
  );
}
