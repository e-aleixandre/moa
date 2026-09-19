// owners3-fixtures — CATALOG ONLY. The owners and sessions iteration 3 draws.
//
// Written to be READ: the sessions are a real wine ERP and a real frontend
// repo, in the language the owner actually works in, with the lengths a real
// list has (titles that truncate, reasons that say something). Toy fixtures
// cannot show whether a column reads as work.
//
// The owner shape is the one the app actually holds: GET /api/owners
// (pkg/serve/owners.go — id, name, codebase_key, root, session_id, avatar,
// session_state) joined with its children and its own conversation's reason by
// data/owners-model.js ownerRows. The lab builds the JOINED shape directly,
// because it has no store to join from; the fields are exactly the ones
// ownerLine/ownerState read, so a row drawn here is a row drawn in the app.

const now = Date.now();
const min = (n) => now - n * 60000;

// owner_id is what the server stamps on every session of a codebase that has
// an owner; the fixtures do the same so By project can find the owner's group.
const OWNER_OF_ROOT = {};
const sess = (id, title, state, brief, briefTone, when, cwd, over = {}) => ({
  id, title, state, brief, briefTone, when, cwd, updated: over.updated ?? now, owner_id: OWNER_OF_ROOT[cwd], ...over,
});

const WINERIM_ROOT = "/home/ealeixandre/dev/winerim-backend/main";
const WINERIM_WEB_ROOT = "/home/ealeixandre/dev/winerim-web/main";
const MOA_ROOT = "/home/ealeixandre/dev/moa/main";

/* ── The owners ──────────────────────────────────────────────────────────

   Three, and the third one is the argument for the avatar: "Winerim" and
   "Winerim Web" have the same two initials, so a monogram tile calls them both
   "Wi" in the same hue family. Shape × colour separates them at a glance, and
   they are two genuinely different projects — the ERP's backend and the shop
   front — that a person switches between all day. */

// kid builds a child in the shape ownerRows emits: what childGroup reads.
const kid = (id, state, over = {}) => ({ id, title: id, state, unseen: false, ...over });

// Winerim: idle itself, six live children, two of them stopped.
export const WINERIM = {
  id: "own_9f2c1a7b",
  name: "Winerim",
  codebase_key: "winerim-backend",
  root: WINERIM_ROOT,
  session_id: "own-sess-winerim",
  avatar: { shape: "squircle", color: "peach" },
  session_state: "",
  children: [
    kid("w1", "permission"), kid("w2", "error"), kid("w3", "idle", { unseen: true }),
    kid("w4", "running"), kid("w5", "running"), kid("w6", "idle"),
  ],
};

// Winerim Web: doing something of its own, nothing of its blocked.
export const WINERIM_WEB = {
  id: "own_4b77e210",
  name: "Winerim Web",
  codebase_key: "winerim-web",
  root: WINERIM_WEB_ROOT,
  session_id: "own-sess-winerim-web",
  avatar: { shape: "blob", color: "mint" },
  session_state: "running",
  ownReason: "Running · 4m",
  children: [kid("v1", "running"), kid("v2", "idle"), kid("v3", "running")],
};

// moa: it wrote and nobody has read it.
export const MOA_OWNER = {
  id: "own_31d0e4aa",
  name: "moa",
  codebase_key: "moa",
  root: MOA_ROOT,
  session_id: "own-sess-moa",
  avatar: { shape: "hexagon", color: "lilac" },
  session_state: "",
  unseen: true,
  ownReason: "Answered · not read yet",
  children: [kid("m1", "running"), kid("m2", "idle")],
};

export const OWNERS = [WINERIM, WINERIM_WEB, MOA_OWNER];
OWNER_OF_ROOT[WINERIM_ROOT] = WINERIM.id;
OWNER_OF_ROOT[WINERIM_WEB_ROOT] = WINERIM_WEB.id;
OWNER_OF_ROOT[MOA_ROOT] = MOA_OWNER.id;

/* The owner states, as the preset switcher offers them. Each is the SAME
   owner wearing a different state, so what is being compared is the state and
   not three different rows. */
// Children that are all quiet, and the same six with two of them stopped: the
// waiting clause is a fact about the CHILDREN, so it is switched by swapping
// them rather than by a counter the row would have to be told.
const QUIET = [kid("q1", "running"), kid("q2", "running"), kid("q3", "idle"),
  kid("q4", "running"), kid("q5", "idle"), kid("q6", "running")];
const TWO_STOPPED = [...QUIET.slice(0, 4), kid("q7", "permission"), kid("q8", "error")];

export const WINERIM_IDLE = { ...WINERIM, session_state: "", children: QUIET };
export const WINERIM_WORKING = { ...WINERIM, session_state: "running", ownReason: "Running · 4m", children: QUIET };
export const WINERIM_ASKS = { ...WINERIM, session_state: "permission", ownReason: "Needs your answer", children: QUIET };
export const WINERIM_UNREAD = { ...WINERIM, session_state: "", unseen: true, ownReason: "Answered · not read yet", children: QUIET };
export const WINERIM_ASKS_WAITING = { ...WINERIM, session_state: "permission", ownReason: "Needs your answer", children: TWO_STOPPED };
export const WINERIM_WORKING_WAITING = { ...WINERIM, session_state: "running", ownReason: "Running · 4m", children: TWO_STOPPED };
export const WINERIM_IDLE_WAITING = { ...WINERIM, session_state: "", children: TWO_STOPPED };
export const WINERIM_SAVED = { ...WINERIM, session_state: "saved", children: QUIET };

export const STATE_ROWS = [
  { key: "idle", owner: WINERIM_IDLE, note: "Standing by. The only number is how many of its sessions are live." },
  { key: "working", owner: WINERIM_WORKING, note: "Doing something of its own. Blue, and it says what." },
  { key: "asks", owner: WINERIM_ASKS, note: "It asked YOU. Amber, and the question is quoted — a row that says \"asks you\" without saying what is a notification." },
  { key: "unread", owner: WINERIM_UNREAD, note: "It wrote and nobody read. Mauve, which is what mauve already means here." },
  { key: "idle+w", owner: WINERIM_IDLE_WAITING, note: "Idle itself, two children stopped. Two facts about two different conversations." },
  { key: "working+w", owner: WINERIM_WORKING_WAITING, note: "Working AND two children stopped: blue clause, amber clause, one line." },
  { key: "asks+w", owner: WINERIM_ASKS_WAITING, note: "Both amber, because both are you. The lead is the owner's own question." },
  { key: "saved", owner: WINERIM_SAVED, note: "Parked on purpose: eyes shut, the mark one step back, no dot — the saved session's own rule." },
];

/* ── The sessions ────────────────────────────────────────────────────────
   Winerim's six children, Winerim Web's three, moa's two. No owner is in this
   list: the backend hides an owner's conversation from GET /api/sessions
   (data/util/project-sessions.js isOrdinarySession), and an owner listed among
   the sessions it is responsible for would be one of its own rows. */

export const SESSIONS = [
  sess("w1", "Importar albaranes de Bodegas Torres", "permission", "Necesita tu respuesta: escribir en producción", "yellow", "4m", WINERIM_ROOT, { updated: min(4) }),
  sess("w2", "Cuadrar el stock de la añada 2023 con el inventario físico", "error", "Parada: el proveedor devolvió 409 tres veces", "red", "22m", WINERIM_ROOT, { updated: min(22) }),
  sess("w3", "Etiquetas DO Rioja en el PDF de expedición", "idle", "Listo: 14 plantillas regeneradas", "mauve", "1h", WINERIM_ROOT, { unseen: true, updated: min(63) }),
  sess("w4", "Migrar las tarifas de distribuidor a la tabla nueva", "running", "Ejecutando · go test ./internal/tarifas/...", "neutral", "now", WINERIM_ROOT, { updated: min(0) }),
  sess("w5", "Tests del cálculo de impuestos especiales", "running", "Ejecutando · escribiendo casos de IIEE", "neutral", "9m", WINERIM_ROOT, { updated: min(9) }),
  sess("w6", "Revisar el informe de trazabilidad de la cooperativa", "idle", "", "neutral", "3h", WINERIM_ROOT, { updated: min(180) }),

  sess("v1", "Ficha de vino con la cata y las notas de añada", "running", "Ejecutando · montando la galería", "neutral", "6m", WINERIM_WEB_ROOT, { updated: min(6) }),
  sess("v2", "Checkout con recogida en bodega", "idle", "Listo: el paso de recogida ya valida horarios", "neutral", "2h", WINERIM_WEB_ROOT, { updated: min(122) }),
  sess("v3", "Buscador de vinos por DO y añada", "running", "Ejecutando · indexando 1.240 referencias", "neutral", "14m", WINERIM_WEB_ROOT, { updated: min(14) }),

  sess("m1", "Sección de owners en el sidebar", "running", "Ejecutando · capturando el catálogo", "neutral", "2m", MOA_ROOT, { updated: min(2) }),
  sess("m2", "Revisar el bundle del frontend", "idle", "", "neutral", "5h", MOA_ROOT, { updated: min(300) }),
];

export const SAVED = [
  sess("s1", "Exportar el histórico de añadas a CSV", "saved", "", "neutral", "6d", WINERIM_ROOT, { saved: true, updated: now - 6 * 86400000 }),
  sess("s2", "Rediseño de la ficha de proveedor", "saved", "", "neutral", "11d", WINERIM_WEB_ROOT, { saved: true, updated: now - 11 * 86400000 }),
];

// The PROMOTED preset: one more session rises into Needs attention, so the
// section is three rows rather than two and the Active collapse below it does
// not look like the only thing in the column.
export const PROMOTED = SESSIONS.map((s) =>
  s.id === "v1"
    ? { ...s, state: "permission", brief: "Necesita tu respuesta: subir las fotos a Cloudinary", briefTone: "yellow", when: "1m", updated: min(1) }
    : s,
);

/* ── The child session in the pane, for the chip ───────────────────────── */

const said = (id, at, text) => ({ role: "assistant", msg_id: id, _msg_id: id, timestamp: at, content: [{ type: "text", text }] });
const asked = (id, at, text) => ({ role: "user", msg_id: id, _msg_id: id, timestamp: at, content: [{ type: "text", text }] });

export const CHILD_SESSION = {
  id: "w1",
  title: "Importar albaranes de Bodegas Torres",
  state: "permission",
  model: "GPT Terra",
  provider: "openai",
  thinking: "low",
  cwd: WINERIM_ROOT,
  owner_id: "own_9f2c1a7b",
  updated: min(4),
  permissionMode: "ask",
  contextPercent: 47,
  contextWindow: 400000,
  costUSD: 0.74,
  messages: [
    asked("c-u1", min(38), "Importa los albaranes de Torres de agosto y concíliala contra el conteo de Marta."),
    said("c-a1", min(37), "Leo el CSV de Torres, normalizo las líneas y concilio por número de albarán. Las cajas de regalo van con importe 0: cuentan para el stock y no para la factura."),
    {
      _type: "tool_start", msg_id: "c-t1", _msg_id: "c-t1", tool_call_id: "c-t1",
      tool_name: "read", args: { path: "internal/importacion/torres.go" }, status: "done", result: "214 lines",
    },
    {
      _type: "tool_start", msg_id: "c-t2", _msg_id: "c-t2", tool_call_id: "c-t2",
      tool_name: "bash", args: { command: "go run ./cmd/importar --proveedor torres --mes 2026-08 --dry-run" },
      status: "done", result: "1204 líneas conciliadas · 3 incidencias (cajas de regalo) · 0 diferencias > 5%",
    },
    said("c-a2", min(5), "Conciliado en seco: 1.204 líneas, 3 incidencias por cajas de regalo, ninguna diferencia mayor del 5 %. Para escribirlo en producción necesito tu permiso."),
  ],
  subagents: {},
};
