import { useState, type FormEvent } from "react";
import { api, type Library, type User } from "../api";
import { Alert, errorText, Field } from "../ui";
import { login } from "./Login";

// Asistente de primer arranque: código inicial y Owner, y después la primera
// biblioteca. El código aparece en la consola y en los logs del servidor.
export function Setup({ onDone }: { onDone: (u: User) => void }) {
  const [step, setStep] = useState<1 | 2>(1);
  const [user, setUser] = useState<User | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const [code, setCode] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");

  const [libName, setLibName] = useState("Películas");
  const [libType, setLibType] = useState<"movies" | "tv">("movies");
  const [libPath, setLibPath] = useState("");

  const createOwner = async (e: FormEvent) => {
    e.preventDefault();
    if (password !== confirm) {
      setError("Las contraseñas no coinciden");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await api.post("/auth/setup", { code: code.trim(), username, password });
      setUser(await login(username, password));
      setStep(2);
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };

  const createLibrary = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const lib = await api.post<Library>("/libraries", { name: libName, type: libType, paths: [libPath] });
      await api.post(`/libraries/${lib.id}/scan`);
      onDone(user!);
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="auth-screen">
      <div className="auth-card auth-card-wide">
        <div className="brand brand-lg">
          <span className="brand-mark" aria-hidden="true" />
          MediaNode
        </div>
        <ol className="steps" aria-label="Pasos">
          <li className={step === 1 ? "current" : "done"}>Propietario</li>
          <li className={step === 2 ? "current" : ""}>Primera biblioteca</li>
        </ol>
        {error && <Alert>{error}</Alert>}

        {step === 1 ? (
          <form onSubmit={createOwner} className="stack">
            <h1>Configura tu servidor</h1>
            <p className="muted">
              El código inicial aparece en la consola y en los logs del servidor al arrancar por primera vez. Deja de
              valer al usarlo.
            </p>
            <Field label="Código inicial">
              <input autoFocus required autoComplete="off" spellCheck={false} value={code} onChange={(e) => setCode(e.target.value)} />
            </Field>
            <Field label="Usuario del propietario">
              <input required autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} />
            </Field>
            <div className="grid-2">
              <Field label="Contraseña" hint="Al menos 8 caracteres">
                <input
                  required
                  type="password"
                  minLength={8}
                  autoComplete="new-password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                />
              </Field>
              <Field label="Repite la contraseña">
                <input
                  required
                  type="password"
                  autoComplete="new-password"
                  value={confirm}
                  onChange={(e) => setConfirm(e.target.value)}
                />
              </Field>
            </div>
            <button className="btn btn-primary btn-block" disabled={busy}>
              {busy ? "Creando…" : "Crear propietario"}
            </button>
          </form>
        ) : (
          <form onSubmit={createLibrary} className="stack">
            <h1>Añade tu primera biblioteca</h1>
            <p className="muted">Indica la carpeta del servidor donde guardas tus películas o series. Los archivos no se copian.</p>
            <div className="grid-2">
              <Field label="Nombre">
                <input required maxLength={100} value={libName} onChange={(e) => setLibName(e.target.value)} />
              </Field>
              <Field label="Tipo">
                <select
                  value={libType}
                  onChange={(e) => {
                    const t = e.target.value as "movies" | "tv";
                    setLibType(t);
                    setLibName(t === "movies" ? "Películas" : "Series");
                  }}
                >
                  <option value="movies">Películas</option>
                  <option value="tv">Series</option>
                </select>
              </Field>
            </div>
            <Field label="Carpeta" hint="Ruta absoluta en el servidor, por ejemplo D:\Películas o /srv/media/peliculas">
              <input required spellCheck={false} value={libPath} onChange={(e) => setLibPath(e.target.value)} />
            </Field>
            <div className="row-end">
              <button type="button" className="btn btn-ghost" onClick={() => onDone(user!)} disabled={busy}>
                Más tarde
              </button>
              <button className="btn btn-primary" disabled={busy}>
                {busy ? "Creando…" : "Crear y escanear"}
              </button>
            </div>
          </form>
        )}
      </div>
    </div>
  );
}
