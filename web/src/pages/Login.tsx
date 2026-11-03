import { useState, type FormEvent } from "react";
import { api, type User } from "../api";
import { Alert, errorText, Field } from "../ui";

// deviceIdentifier es estable por navegador: el servidor reutiliza la fila del
// dispositivo en cada inicio de sesión en vez de crear una nueva.
export function deviceIdentifier(): string {
  const key = "medianode.device";
  try {
    let id = localStorage.getItem(key);
    if (!id) {
      id = crypto.randomUUID();
      localStorage.setItem(key, id);
    }
    return id;
  } catch {
    return "";
  }
}

export async function login(username: string, password: string): Promise<User> {
  const res = await api.post<{ user: User }>("/auth/login", {
    username,
    password,
    device_name: "Panel web",
    platform: "web",
    device_identifier: deviceIdentifier(),
  });
  return res.user;
}

export function Login({ serverName, onDone }: { serverName: string; onDone: (u: User) => void }) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      onDone(await login(username, password));
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="auth-screen">
      <form className="auth-card" onSubmit={submit}>
        <div className="brand brand-lg">
          <span className="brand-mark" aria-hidden="true" />
          {serverName}
        </div>
        <h1>Iniciar sesión</h1>
        {error && <Alert>{error}</Alert>}
        <Field label="Usuario">
          <input autoFocus required autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} />
        </Field>
        <Field label="Contraseña">
          <input
            required
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </Field>
        <button className="btn btn-primary btn-block" disabled={busy}>
          {busy ? "Entrando…" : "Entrar"}
        </button>
      </form>
    </div>
  );
}
