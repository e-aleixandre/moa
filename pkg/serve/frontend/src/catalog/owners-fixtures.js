// owners-fixtures — CATALOG ONLY. The owners the lab draws, and the book one
// of them keeps.
//
// The shapes are the API's, not the lab's: an owner is
// {id,name,codebase_key,root,session_id,model,thinking,answer_asks,created,
// session_state} (pkg/serve/owners.go:36-44, pkg/owner/owner.go:53-60). What
// the API does NOT send today is `children` and the book — both are added here
// as the shape the surface would need, and both are listed in the report as
// backend work rather than pretended to exist.
//
// The sessions are a wine ERP because the point of a fixture is to be read:
// generic "session 1 / session 2" rows cannot show whether a title truncates
// or whether a list of six reads as work.

const now = Date.now();
const min = (n) => now - n * 60000;

const child = (id, title, state, over = {}) => ({ id, title, state, ...over });

// Winerim — the busy owner. Six children across the four groups, with the
// lengths a real list has: two that stop the work, one finished nobody read,
// two moving, one parked at the bottom.
export const WINERIM_CHILDREN = [
  child("w1", "Importar albaranes de Bodegas Torres", "permission", {
    when: "4m", brief: "Needs your answer: escribir en producción", updated: min(4),
  }),
  child("w2", "Cuadrar el stock de la añada 2023 con el inventario físico", "error", {
    when: "22m", brief: "Stopped: el proveedor devolvió 409 tres veces", updated: min(22),
  }),
  child("w3", "Etiquetas DO Rioja en el PDF de expedición", "idle", {
    unseen: true, when: "1h", brief: "Listo: 14 plantillas regeneradas", updated: min(63),
  }),
  child("w4", "Migrar las tarifas de distribuidor a la tabla nueva", "running", {
    when: "now", brief: "Running · go test ./internal/tarifas/...", updated: min(0),
  }),
  child("w5", "Tests del cálculo de impuestos especiales", "running", {
    when: "9m", brief: "Running · escribiendo casos de IIEE", updated: min(9),
  }),
  child("w6", "Revisar el informe de trazabilidad de la cooperativa", "idle", {
    when: "3h", brief: "", updated: min(180),
  }),
];

export const MOA_CHILDREN = [
  child("m1", "Panel de owners en el catálogo", "running", { when: "2m", brief: "Running · capturando el lab", updated: min(2) }),
  child("m2", "Revisar el bundle del frontend", "idle", { when: "5h", brief: "", updated: min(300) }),
];

export const WINERIM = {
  id: "own_9f2c1a7b",
  name: "Winerim",
  codebase_key: "winerim-backend",
  root: "/home/ealeixandre/dev/winerim-backend/main",
  session_id: "own-sess-winerim",
  model: "anthropic/claude-opus-4-8",
  thinking: "low",
  answer_asks: true,
  created: now - 26 * 86400000,
  session_state: "idle",
  children: WINERIM_CHILDREN,
};

export const MOA_OWNER = {
  id: "own_31d0e4aa",
  name: "moa",
  codebase_key: "moa",
  root: "/home/ealeixandre/dev/moa/main",
  session_id: "own-sess-moa",
  model: "anthropic/claude-opus-4-8",
  thinking: "low",
  answer_asks: true,
  created: now - 9 * 86400000,
  session_state: "running",
  children: MOA_CHILDREN,
};

export const OWNERS = [WINERIM, MOA_OWNER];

/* ── The ordinary session list ───────────────────────────────────────────
   The roster the sidebar's Recent / By project modes draw, in the row shape
   both densities take (Sidebar.jsx `active`).

   No owner is in here, and that is not a lab simplification: the backend
   hides an owner's conversation from GET /api/sessions, so a "Winerim owner"
   row in this list would draw something the product does not send. An owner
   is reached through the sidebar's Owners MODE, which is the whole point of
   there being a third mode. */

const listRow = (c, cwd) => ({
  id: c.id,
  title: c.title,
  state: c.state,
  unseen: c.unseen,
  when: c.when,
  brief: c.brief,
  briefTone: c.state === "permission" ? "yellow"
    : c.state === "error" ? "red"
      : c.unseen ? "mauve" : "neutral",
  cwd,
});

export const LIST_SESSIONS = [
  ...WINERIM_CHILDREN.map((c) => listRow(c, WINERIM.root)),
  ...MOA_CHILDREN.map((c) => listRow(c, MOA_OWNER.root)),
];

// The owner asking something of its own accord: the state the list has to be
// able to show without borrowing a child's colour.
export const WINERIM_ASKING = {
  ...WINERIM,
  session_state: "permission",
};

/* ── The book ────────────────────────────────────────────────────────────
   Four files in the shape the template seeds (pkg/owner/owner.go:296-316):
   the index, a dated decision, who asks for what, and one area. Written as
   an owner would keep them — short, dated, and pointing at each other. */

const PROJECT_MD = `# Winerim

ERP de bodega: entrada de uva, vinificación, stock por añada y expedición con
sus documentos (albarán, factura, DO, impuestos especiales).

## Estado actual

Migrando las tarifas de distribuidor a la tabla nueva. La importación de
albaranes va por proveedor: Torres y Faustino hechos, Muga a medias.

## Decisiones que atan

- El stock se cuadra contra el inventario físico, nunca contra el albarán.
  → decisions/2026-08-01-vale-basta.md
- Nada escribe en producción sin que lo apruebe Nuria. → people.md
- Los impuestos especiales se calculan en el backend, jamás en el PDF.
  → areas/importacion.md

## Mapa de este libro

- decisions/ — qué se decidió, cuándo y por qué
- people.md — quién pide qué, y cómo
- areas/importacion.md — la entrada de albaranes, proveedor a proveedor
`;

const DECISION_MD = `# 2026-08-01 — "Vale, basta": el stock manda sobre el albarán

Nuria, después de la tercera descuadre de la añada 2022.

## Qué se decidió

El inventario físico es la verdad. Si un albarán dice 240 cajas y el conteo
dice 236, el sistema guarda 236 y abre una incidencia con la diferencia.

## Por qué

Los albaranes de dos proveedores llegan con las cajas de regalo incluidas y de
otro sin ellas. Cuadrar por albarán significaba discutir con el proveedor cada
mes; cuadrar por conteo significa discutirlo una vez, con el dato delante.

## Qué NO se decidió

Qué hacer cuando la diferencia es mayor del 5 %. Hoy se abre incidencia igual.
`;

const PEOPLE_MD = `# Quién pide qué

## Nuria — dirección de bodega

Aprueba todo lo que escriba en producción. Pide resultados, no planes: si le
mandas tres opciones te devuelve "elige tú y me lo enseñas".

## Jorge — administración

Todo lo de facturación y DO. Escribe por correo, siempre con el PDF adjunto.
Nunca pide algo urgente sin decir la fecha límite.

## Marta — almacén

La que hace el conteo físico. Si algo del stock no cuadra, se le pregunta a
ella ANTES de tocar la base de datos.
`;

const AREA_MD = `# Importación de albaranes

Un albarán entra por \`internal/importacion\`, se normaliza por proveedor y se
concilia contra el conteo (ver la decisión de 2026-08-01).

## Proveedores

| Proveedor | Formato | Estado |
|---|---|---|
| Bodegas Torres | CSV con cabecera | hecho |
| Faustino | XML propio | hecho |
| Muga | PDF escaneado | a medias, va por OCR |

## Trampas conocidas

- Torres manda las cajas de regalo como línea con importe 0. Cuentan para el
  stock y no para la factura.
- Los impuestos especiales se calculan aquí, nunca en el generador de PDF.
`;

export const WINERIM_BOOK = [
  { path: "PROJECT.md", label: "PROJECT.md", bytes: PROJECT_MD.length, updated: min(90), body: PROJECT_MD },
  { path: "decisions/2026-08-01-vale-basta.md", label: "2026-08-01-vale-basta.md", bytes: DECISION_MD.length, updated: now - 46 * 86400000, body: DECISION_MD },
  { path: "people.md", label: "people.md", bytes: PEOPLE_MD.length, updated: now - 12 * 86400000, body: PEOPLE_MD },
  { path: "areas/importacion.md", label: "importacion.md", bytes: AREA_MD.length, updated: now - 2 * 86400000, body: AREA_MD },
];

/* ── The owner's conversation ────────────────────────────────────────────
   Raw server-shaped messages, fed to the production projectStream: a batch of
   reports arrives as a user-role message carrying custom.source="report", and
   the stream projects it as an EVENT block rather than a waypoint
   (data/stream-model.js:453-467, pkg/serve/reports.go:349). Nobody typed it,
   so it must not wear the peach bar that means "you said this". */

const reportBatch = (id, at, count, text) => ({
  role: "user",
  msg_id: id,
  _msg_id: id,
  timestamp: at,
  custom: { source: "report", batch: `rep_${id}`, count },
  content: [{ type: "text", text }],
});

const said = (id, at, text) => ({
  role: "assistant",
  msg_id: id,
  _msg_id: id,
  timestamp: at,
  content: [{ type: "text", text }],
});

const asked = (id, at, text) => ({
  role: "user",
  msg_id: id,
  _msg_id: id,
  timestamp: at,
  content: [{ type: "text", text }],
});

export const WINERIM_SESSION = {
  id: "own-sess-winerim",
  title: "Winerim",
  kind: "owner",
  state: "idle",
  model: "Claude Opus 4.8",
  provider: "anthropic",
  thinking: "low",
  cwd: "/home/ealeixandre/dev/winerim-backend/main",
  updated: min(3),
  permissionMode: "ask",
  contextPercent: 34,
  contextWindow: 1000000,
  costUSD: 2.61,
  messages: [
    asked("o-u1", min(210), "Arranca la importación de Muga, pero antes dime qué decidimos sobre el conteo."),
    said("o-a1", min(209), "El inventario físico manda sobre el albarán: si el conteo dice 236 y el albarán 240, se guarda 236 y se abre incidencia. Está en el libro, decisions/2026-08-01-vale-basta.md, y viene de la tercera descuadre de la 2022.\n\nAbro una sesión para el OCR de Muga con ese criterio escrito en el encargo."),
    reportBatch("o-r1", min(64), 2, `Reports from 2 sessions of your project:

- w3 — Etiquetas DO Rioja en el PDF de expedición
  status: done
  directory: /home/ealeixandre/dev/winerim-backend/main
  result: 14 plantillas regeneradas; la DO se lee de la tabla, ya no del nombre del fichero.

- w2 — Cuadrar el stock de la añada 2023 con el inventario físico
  status: failed
  directory: /home/ealeixandre/dev/winerim-backend/main
  result: el proveedor devolvió 409 tres veces al reconciliar; no he tocado la base de datos.`),
    said("o-a2", min(63), "Dos cosas.\n\nLas etiquetas están bien y cierran lo que pedía Jorge: la DO sale de la tabla, no del nombre del fichero. Lo anoto en `areas/importacion.md`.\n\nEl 409 de la reconciliación **no** es un fallo nuestro: ese proveedor rechaza un segundo POST con el mismo número de albarán, que es exactamente lo que hace un reintento. He devuelto a esa sesión el criterio que ya está en el libro — reconciliar es idempotente por número de albarán — y que pregunte a Marta antes de tocar stock."),
    reportBatch("o-r2", min(5), 1, `Report from a session of your project:

- w1 — Importar albaranes de Bodegas Torres
  status: needs_input
  directory: /home/ealeixandre/dev/winerim-backend/main
  waiting on: permission
  question: ¿Escribo las 1.204 líneas conciliadas en producción?`),
    said("o-a3", min(4), "Esa no la contesto yo: escribir en producción lo aprueba Nuria, y eso está en `people.md` como decisión que ata. Le he dicho a la sesión que se quede parada y te lo he subido a ti.\n\nLo que sí puedo confirmar es que la conciliación es correcta: 1.204 líneas, 3 incidencias por cajas de regalo, ninguna diferencia mayor del 5 %."),
  ],
  subagents: {},
};

// A child of Winerim, for the chip: the same session the owner is talking
// about, seen from the inside.
export const CHILD_SESSION = {
  id: "w1",
  title: "Importar albaranes de Bodegas Torres",
  state: "permission",
  model: "GPT Terra",
  provider: "openai",
  thinking: "low",
  cwd: "/home/ealeixandre/dev/winerim-backend/main",
  updated: min(4),
  permissionMode: "ask",
  contextPercent: 47,
  contextWindow: 400000,
  costUSD: 0.74,
  messages: [
    asked("c-u1", min(38), "Importa los albaranes de Torres de agosto y concíliala contra el conteo de Marta."),
    said("c-a1", min(37), "Leo el CSV de Torres, normalizo las líneas y concilio por número de albarán. Las cajas de regalo van con importe 0: cuentan para el stock y no para la factura."),
    {
      _type: "tool_start",
      msg_id: "c-t1",
      _msg_id: "c-t1",
      tool_call_id: "c-t1",
      tool_name: "read",
      args: { path: "internal/importacion/torres.go" },
      status: "done",
      result: "214 lines",
    },
    {
      _type: "tool_start",
      msg_id: "c-t2",
      _msg_id: "c-t2",
      tool_call_id: "c-t2",
      tool_name: "bash",
      args: { command: "go run ./cmd/importar --proveedor torres --mes 2026-08 --dry-run" },
      status: "done",
      result: "1204 líneas conciliadas · 3 incidencias (cajas de regalo) · 0 diferencias > 5%",
    },
    said("c-a2", min(5), "Conciliado en seco: 1.204 líneas, 3 incidencias por cajas de regalo, ninguna diferencia mayor del 5 %. Para escribirlo en producción necesito tu permiso."),
  ],
  subagents: {},
};
