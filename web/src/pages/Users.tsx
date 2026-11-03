import { useState, type FormEvent } from "react";
import { api, type Grant, type Library, type Role, type User } from "../api";
import { useSession } from "../App";
import { Alert, Field, PageHeader, Spinner, errorText, formatDate, useLoad } from "../ui";

const roleLabels: Record<Role, string> = {
  owner: "Propietario",
  admin: "Administrador",
  user: "Usuario",
  restricted: "Restringido",
};

const rank: Record<Role, number> = { owner: 4, admin: 3, user: 2, restricted: 1 };

export function Users() {
  const { user: me } = useSession();
  const users = useLoad(() => api.get<User[]>("/admin/users"), []);
  const libs = useLoad(() => api.get<Library[]>("/libraries"), []);
  const [creating, setCreating] = useState(false);
  const [open, setOpen] = useState<string | null>(null);

  return (
    <>
      <PageHeader
        title="Usuarios"
        subtitle="Cuentas, roles y acceso a cada biblioteca"
        actions={
          <button className="btn btn-primary" onClick={() => setCreating(true)}>
            Nuevo usuario
          </button>
        }
      />
      {creating && (
        <CreateUser
          myRole={me.role}
          onCancel={() => setCreating(false)}
          onCreated={(u) => {
            setCreating(false);
            users.reload();
            setOpen(u.id);
          }}
        />
      )}
      {users.loading && !users.data && <Spinner />}
      {users.error && <Alert>{users.error}</Alert>}
      <div className="stack">
        {users.data?.map((u) => (
          <UserRow
            key={u.id}
            user={u}
            libraries={libs.data ?? []}
            manageable={rank[me.role] > rank[u.role]}
            open={open === u.id}
            onToggle={() => setOpen(open === u.id ? null : u.id)}
            onChange={users.reload}
          />
        ))}
      </div>
    </>
  );
}

function roleOptions(myRole: Role): Role[] {
  return (["admin", "user", "restricted"] as Role[]).filter((r) => rank[myRole] > rank[r]);
}

function CreateUser({ myRole, onCancel, onCreated }: { myRole: Role; onCancel: () => void; onCreated: (u: User) => void }) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<Role>("user");
  const [maxAge, setMaxAge] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const u = await api.post<User>("/admin/users", {
        username,
        password,
        role,
        max_age_rating: role === "restricted" && maxAge !== "" ? Number(maxAge) : null,
      });
      onCreated(u);
    } catch (err) {
      setError(errorText(err));
      setBusy(false);
    }
  };

  return (
    <form className="card stack" onSubmit={submit}>
      <h2>Nuevo usuario</h2>
      {error && <Alert>{error}</Alert>}
      <div className="grid-4">
        <Field label="Usuario">
          <input required autoFocus autoComplete="off" value={username} onChange={(e) => setUsername(e.target.value)} />
        </Field>
        <Field label="Contraseña" hint="Al menos 8 caracteres">
          <input required type="password" minLength={8} autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} />
        </Field>
        <Field label="Rol">
          <select value={role} onChange={(e) => setRole(e.target.value as Role)}>
            {roleOptions(myRole).map((r) => (
              <option key={r} value={r}>
                {roleLabels[r]}
              </option>
            ))}
          </select>
        </Field>
        {role === "restricted" && (
          <Field label="Edad máxima" hint="Vacío: solo contenido para todos">
            <input type="number" min={0} max={21} value={maxAge} onChange={(e) => setMaxAge(e.target.value)} />
          </Field>
        )}
      </div>
      <div className="row-end">
        <button type="button" className="btn btn-ghost" onClick={onCancel}>
          Cancelar
        </button>
        <button className="btn btn-primary" disabled={busy}>
          Crear
        </button>
      </div>
    </form>
  );
}

function UserRow(props: {
  user: User;
  libraries: Library[];
  manageable: boolean;
  open: boolean;
  onToggle: () => void;
  onChange: () => void;
}) {
  const { user: u } = props;
  const isAdmin = u.role === "owner" || u.role === "admin";
  return (
    <section className="card">
      <div className="library-head">
        <div>
          <strong>{u.username}</strong> <span className="muted small">· {roleLabels[u.role]}</span>
          {!u.is_active && <span className="badge badge-failed">Desactivado</span>}
          {u.role === "restricted" && (
            <span className="muted small"> · hasta {u.max_age_rating ?? 0} años</span>
          )}
          <div className="muted small">Último acceso: {formatDate(u.last_login_at)}</div>
        </div>
        {props.manageable && (
          <button className="btn btn-ghost" aria-expanded={props.open} onClick={props.onToggle}>
            {props.open ? "Cerrar" : "Gestionar"}
          </button>
        )}
      </div>
      {props.open && props.manageable && (
        <div className="stack">
          <EditUser user={u} onChange={props.onChange} />
          {isAdmin ? (
            <p className="muted small">Los administradores acceden a todas las bibliotecas.</p>
          ) : (
            <Permissions userId={u.id} libraries={props.libraries} />
          )}
        </div>
      )}
    </section>
  );
}

function EditUser({ user, onChange }: { user: User; onChange: () => void }) {
  const { user: me } = useSession();
  const [role, setRole] = useState<Role>(user.role);
  const [active, setActive] = useState(user.is_active);
  const [maxAge, setMaxAge] = useState(user.max_age_rating?.toString() ?? "");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      const body: Record<string, unknown> = {
        role,
        is_active: active,
        max_age_rating: role === "restricted" && maxAge !== "" ? Number(maxAge) : null,
      };
      if (password) body.password = password;
      await api.patch(`/admin/users/${user.id}`, body);
      setPassword("");
      setNotice("Cambios guardados");
      onChange();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!confirm(`¿Eliminar a ${user.username}? Se cerrarán sus sesiones.`)) return;
    setBusy(true);
    try {
      await api.del(`/admin/users/${user.id}`);
      onChange();
    } catch (err) {
      setError(errorText(err));
      setBusy(false);
    }
  };

  return (
    <form className="stack" onSubmit={save}>
      {error && <Alert>{error}</Alert>}
      {notice && <Alert kind="ok">{notice}</Alert>}
      <div className="grid-4">
        <Field label="Rol">
          <select value={role} onChange={(e) => setRole(e.target.value as Role)}>
            {roleOptions(me.role).map((r) => (
              <option key={r} value={r}>
                {roleLabels[r]}
              </option>
            ))}
          </select>
        </Field>
        {role === "restricted" && (
          <Field label="Edad máxima">
            <input type="number" min={0} max={21} value={maxAge} onChange={(e) => setMaxAge(e.target.value)} />
          </Field>
        )}
        <Field label="Nueva contraseña" hint="Vacío: sin cambios">
          <input type="password" minLength={8} autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} />
        </Field>
        <label className="check">
          <input type="checkbox" checked={active} onChange={(e) => setActive(e.target.checked)} /> Cuenta activa
        </label>
      </div>
      <div className="row-end">
        <button type="button" className="btn btn-danger" onClick={() => void remove()} disabled={busy}>
          Eliminar
        </button>
        <button className="btn btn-primary" disabled={busy}>
          Guardar
        </button>
      </div>
    </form>
  );
}

type Level = "" | "view" | "scan" | "manage";

function Permissions({ userId, libraries }: { userId: string; libraries: Library[] }) {
  const perms = useLoad(() => api.get<{ libraries: Grant[] }>(`/admin/users/${userId}/permissions`), [userId]);
  const [draft, setDraft] = useState<Record<string, Level> | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  if (perms.loading && !perms.data) return <Spinner />;
  if (perms.error) return <Alert>{perms.error}</Alert>;

  // Cada permiso incluye a los anteriores: basta con guardar el más alto.
  const current: Record<string, Level> = {};
  for (const g of perms.data!.libraries) {
    current[g.library_id] = g.permissions.includes("manage") ? "manage" : g.permissions.includes("scan") ? "scan" : "view";
  }
  const levels = draft ?? current;

  const save = async () => {
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      await api.put(`/admin/users/${userId}/permissions`, {
        libraries: Object.entries(levels)
          .filter(([, l]) => l !== "")
          .map(([library_id, l]) => ({ library_id, permissions: [l] })),
      });
      setDraft(null);
      setNotice("Permisos guardados");
      perms.reload();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="stack">
      <h3>Acceso a bibliotecas</h3>
      {error && <Alert>{error}</Alert>}
      {notice && <Alert kind="ok">{notice}</Alert>}
      <table className="table">
        <tbody>
          {libraries.map((l) => (
            <tr key={l.id}>
              <td>{l.name}</td>
              <td>
                <select
                  aria-label={`Acceso a ${l.name}`}
                  value={levels[l.id] ?? ""}
                  onChange={(e) => setDraft({ ...levels, [l.id]: e.target.value as Level })}
                >
                  <option value="">Sin acceso</option>
                  <option value="view">Ver</option>
                  <option value="scan">Ver y escanear</option>
                  <option value="manage">Gestionar</option>
                </select>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <div className="row-end">
        <button className="btn btn-primary" onClick={() => void save()} disabled={busy || draft === null}>
          Guardar permisos
        </button>
      </div>
    </div>
  );
}
