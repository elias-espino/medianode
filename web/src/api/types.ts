// Tipos de las respuestas de administración, tomados del esquema generado desde
// docs/openapi.yaml (npm run gen:api). Si la API cambia, el compilador marca
// aquí lo que el panel deja de cumplir.
import type { components } from "./schema";

type S = components["schemas"];

export type AdminSystem = S["AdminSystem"];
export type AdminMetrics = S["AdminMetrics"];
export type AdminSession = S["AdminSession"];
export type AdminNetwork = S["AdminNetwork"];
export type AdminStorage = S["AdminStorage"];
export type AdminVolume = S["AdminVolume"];
export type AdminLogEntry = S["AdminLogEntry"];
export type AdminCheck = S["AdminCheck"];
export type RemoteReport = S["RemoteReport"];
export type RemoteCheck = S["RemoteCheck"];
export type RemoteProbe = S["RemoteProbe"];
export type Transcodes = S["Transcodes"];
export type ContinueItem = S["ContinueItem"];
export type CreateSessionResponse = S["CreateSessionResponse"];
