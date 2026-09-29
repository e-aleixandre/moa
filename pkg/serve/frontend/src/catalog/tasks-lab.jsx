import { createPortal } from "preact/compat";
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "preact/hooks";
import { ArrowRight, Check, ChevronDown, Link2, Plus, Search, Trash2 } from "lucide-preact";
import { DesktopShell, ConversationScreen, MobileConversationScreen } from "../layout/index.js";
import { ScreenLab, DESKTOP_LAB_WIDTH, DESKTOP_LAB_HEIGHT, PHONE_LAB_WIDTH, PHONE_LAB_HEIGHT } from "./desktop-lab.jsx";
import { MobileSheet } from "../layout/mobile/MobileSheet/MobileSheet.jsx";
import { setState, store } from "../data/store.js";
import { setTileSession } from "../data/tileTree.js";
import { closeSessionPanel, openSessionPanel } from "../data/session-panel.js";
import { openDrawer } from "../data/drawer.js";
import { getToasts, removeToast, subscribeToasts } from "../data/notifications.js";
import { projectName } from "../data/util/format.js";
import { formatShortcut } from "../data/util/shortcut.js";
import { OWNERS } from "./owners-fixtures.js";
import { TasksLabA } from "./tasks-lab-a.jsx";
import "./tasks-lab.css";

/* Global tasks, second pass (?view=tasks, model B).

   The chrome is production: DesktopShell (the real sidebar and dossier),
   ConversationScreen, MobileConversationScreen, the real drawer, MobileSheet
   and SessionPanel, fed by the store. What is new is mounted INTO that chrome
   through portals — the Tasks door in the sidebar foot, the line pinned in the
   dock, the Tasks row in the dossier — and wears the classes of the thing it
   sits next to. Nothing in production is edited. One screen per URL, because
   the store is global: scene × device × foot are query parameters. */

// ── Data ─────────────────────────────────────────────────────────────────

const now = Date.now();
const min = (n) => now - n * 60000;
const MOA = "/home/ealeixandre/dev/moa/main";
const WINERIM = "/home/ealeixandre/dev/winerim-backend/main";
const OUROWN = "/home/ealeixandre/dev/ourown-studio/main";

const PROD_VERSION = { current: "v0.42.0-19-gfc45cedc", latest: "v0.44.0", update_available: true };

const user = (id, text, ago) => ({ msg_id: id, role: "user", timestamp: min(ago), content: [{ type: "text", text }] });
const say = (text, ago = 1) => ({ role: "assistant", timestamp: min(ago), content: [{ type: "text", text }] });
const tool = (id, name, args, status = "done", result = "ok", extra = {}) => ({
  _type: "tool_start", tool_call_id: id, tool_name: name, args, status, result, ...extra,
});

function session(id, title, cwd, state, ago, extra = {}) {
  return {
    id, title, cwd, state, updated: min(ago),
    model: "Claude Opus 5.5", provider: "anthropic", thinking: "medium",
    permissionMode: "yolo", contextPercent: 34, contextWindow: 200000,
    costUSD: 2.1, runTokensUp: 48000, runTokensDown: 6100,
    messages: [], subagents: {},
    ...(state === "running" ? { runStartedAtMs: min(ago + 2), briefProgress: "Working" } : null),
    ...extra,
  };
}

const PULSE_BASE = [
  user("p-u1", "Monta el pipeline de deploy de pulse-api a staging con GitHub Actions.", 42),
  say("Empiezo por el workflow y el job de deploy.", 41),
  tool("p-w", "write", { path: ".github/workflows/deploy.yml" }, "done", "58 lines"),
  tool("p-e", "edit", { path: "deploy/staging.env" }, "done", "ok"),
  tool("p-t", "tasks", { action: "ask", title: "Añadir el secret GH_DEPLOY_KEY en GitHub" }, "done", "Task added for you"),
  say("El workflow está escrito. Para desplegar necesito la clave de deploy en los secrets del repo: te lo he dejado como tarea. Mientras, sigo con la caché de módulos Go.", 12),
];

function sessions(scene) {
  const pulseMsgs = scene === "sent"
    ? [
      ...PULSE_BASE,
      tool("p-c", "edit", { path: ".github/workflows/deploy.yml" }, "done", "ok"),
      {
        msg_id: "p-ev", role: "user", timestamp: min(1),
        custom: { source: "event", id: "ev_task_1", source_name: "tasks", title: "Done · Añadir el secret GH_DEPLOY_KEY en GitHub" },
        content: [{ type: "text", text: "Se llama GH_DEPLOY_KEY_PULSE, no GH_DEPLOY_KEY." }],
      },
      say("Entendido: uso `GH_DEPLOY_KEY_PULSE`. Actualizo el workflow y lanzo el deploy a staging.", 0),
      tool("p-r", "bash", { command: "gh workflow run deploy.yml -f env=staging" }, "running", null, { startedAt: now - 8000 }),
    ]
    : [...PULSE_BASE, tool("p-g", "bash", { command: "go build ./... && go test ./deploy/..." }, "running", null, { startedAt: now - 20000 })];
  const list = [
    session("pulse", "Pipeline de deploy de pulse-api", MOA, "running", 0, { messages: pulseMsgs, briefProgress: "Caché de módulos Go" }),
    session("race", "Carrera en el borrado de adjuntos", MOA, "running", 3, { briefProgress: "go test -race ./pkg/attach" }),
    session("pw", "Playwright por sesión", MOA, "idle", 60 * 20),
    session("torres", "Importar albaranes de Bodegas Torres", WINERIM, "running", 50, { briefProgress: "Conciliando líneas" }),
    session("tarifas", "Migrar las tarifas de distribuidor", WINERIM, "idle", 60 * 5),
    session("checkout", "Checkout con Stripe", OUROWN, "idle", 60 * 2),
  ];
  return Object.fromEntries(list.map((s) => [s.id, s]));
}

const SESSION_NAMES = {
  pulse: "Pipeline de deploy de pulse-api",
  race: "Carrera en el borrado de adjuntos",
  pw: "Playwright por sesión",
  torres: "Importar albaranes de Bodegas Torres",
  tarifas: "Migrar las tarifas de distribuidor",
  checkout: "Checkout con Stripe",
};
const SESSION_CWD = { pulse: MOA, race: MOA, pw: MOA, torres: WINERIM, tarifas: WINERIM, checkout: OUROWN };
const PROJECTS = [MOA, WINERIM, OUROWN].map((cwd) => ({ id: projectName(cwd), cwd }));
const WORKING = new Set(["pulse", "race", "torres"]);

const T = (id, title, place, extra = {}) => ({ id, title, place, status: "open", desc: "", subs: [], waits: [], age: "1d", ...extra });
const SEED = [
  T("t1", "Añadir el secret GH_DEPLOY_KEY en GitHub", "you", { from: "pulse", project: "moa", age: "12m", isNew: true,
    desc: "Repo ealeixandre/pulse-api → Settings → Secrets and variables → Actions. La clave privada está en 1Password, «pulse-api deploy»." }),
  T("t2", "Confirmar con Marta el conteo de cajas de regalo", "you", { from: "torres", project: "winerim-backend", age: "1h", isNew: true,
    desc: "Albarán 2231: tres líneas con importe 0. ¿Cuentan para el stock y no para la factura?" }),
  T("t3", "Aprobar el texto legal del checkout", "you", { from: "checkout", project: "ourown-studio", age: "3h",
    desc: "Borrador en docs/legal/checkout.md. Falta el párrafo de devoluciones." }),
  T("t4", "Fijar la versión de @playwright/mcp", "you", { project: "moa", age: "2d" }),
  T("t5", "docker builder prune (~25 GB)", "you", { project: "moa", age: "1d" }),
  T("t6", "Volver a emparejar el iPhone", "you", { age: "1d" }),
  T("t7", "Alarma de disco al 85 %", "backlog", { project: "moa", age: "1d",
    desc: "El disco se llenó el 28/09 y falló el emparejamiento del iPhone.",
    subs: [{ t: "Script que mida el uso de /", done: true }, { t: "Timer de systemd cada 15 min", done: false }, { t: "Aviso por Pulse", done: false }] }),
  T("t8", "El orden de «más reciente» es falso tras reiniciar", "backlog", { project: "moa", age: "4d" }),
  T("t9", "Exportar la trazabilidad a PDF por lote", "backlog", { project: "winerim-backend", age: "2d", waits: ["t2"] }),
  T("t9b", "Optimizar las imágenes de la galería", "backlog", { project: "ourown-studio", age: "5d" }),
  T("t11", "Escribir el workflow de deploy", "session", { session: "pulse", project: "moa", status: "done", age: "40m" }),
  T("t12", "Configurar el job de deploy en GitHub Actions", "session", { session: "pulse", project: "moa", status: "working", age: "20m",
    subs: [{ t: "Job con environment staging", done: true }, { t: "Caché de módulos Go", done: false }] }),
  T("t10", "Probar el despliegue en staging", "session", { session: "pulse", project: "moa", age: "20m", waits: ["t1"] }),
  T("t13", "Reproducir con 100 borrados en paralelo", "session", { session: "race", project: "moa", status: "done", age: "1h" }),
  T("t14", "Mover la comprobación dentro del lock", "session", { session: "race", project: "moa", status: "working", age: "30m" }),
  T("t15", "Pasar go vet ./...", "session", { session: "race", project: "moa", age: "30m", waits: ["t14"] }),
  T("t16", "Normalizar las líneas del CSV de Torres", "session", { session: "torres", project: "winerim-backend", status: "done", age: "2h" }),
  T("t17", "Conciliar contra el conteo de Marta", "session", { session: "torres", project: "winerim-backend", age: "2h", waits: ["t2"] }),
  T("t18", "Renovar el token de Sentry", "you", { from: "checkout", project: "ourown-studio", status: "done", age: "5h", note: "Nuevo token en 1Password, «sentry-ourown»." }),
];

// ── Task store (one per page) ────────────────────────────────────────────

let tasks = [];
const listeners = new Set();
const emit = () => listeners.forEach((fn) => fn(tasks));
const setTasks = (next) => { tasks = typeof next === "function" ? next(tasks) : next; emit(); };
const patchTask = (id, p) => setTasks((ts) => ts.map((t) => (t.id === id ? { ...t, ...p } : t)));
const byId = (id) => tasks.find((t) => t.id === id);
const completeTask = (id, note = "") => patchTask(id, { status: "done", note, isNew: false, age: "now" });
const reopenTask = (id) => patchTask(id, { status: "open", note: "" });
const removeTask = (id) => setTasks((ts) => ts.filter((t) => t.id !== id));
function moveTask(id, dest) {
  if (dest.place === "session") patchTask(id, { place: "session", session: dest.id, project: projectName(SESSION_CWD[dest.id]), from: undefined });
  else if (dest.place === "backlog") patchTask(id, { place: "backlog", project: dest.id, session: undefined, from: undefined });
  else patchTask(id, { place: "you", session: undefined });
}
function useTasks() {
  const [, force] = useState(0);
  useEffect(() => { const fn = () => force((n) => n + 1); listeners.add(fn); return () => listeners.delete(fn); }, []);
  return tasks;
}

const isOpen = (t) => t.status !== "done";
const isRequest = (t) => t.place === "you" && !!t.from;
const openRequests = (ts) => ts.filter((t) => isRequest(t) && isOpen(t));

function placeLabel(t) {
  if (t.place === "you") return "You";
  if (t.place === "backlog") return `Backlog · ${t.project}`;
  return SESSION_NAMES[t.session];
}
// The session that hears about a change: the one it is assigned to, or the one
// that asked for it. A note and a backlog item tell nobody.
function listenerOf(t) {
  if (!t) return null;
  if (t.place === "session") return t.session;
  if (isRequest(t)) return t.from;
  return null;
}

function groupsFor(ts, { agents, project, session: only }) {
  const scoped = ts.filter((t) => !project || t.project === project);
  const out = [];
  if (only) {
    const reqs = scoped.filter((t) => isRequest(t) && t.from === only);
    if (reqs.length) out.push({ id: "for-you", title: "For you", rows: reqs, n: reqs.filter(isOpen).length });
    const mine = scoped.filter((t) => t.place === "session" && t.session === only);
    out.push({ id: "session", title: "This session", rows: mine, drop: { place: "session", id: only }, progress: `${mine.filter((t) => !isOpen(t)).length}/${mine.length}`, add: true });
    return out;
  }
  const you = scoped.filter((t) => t.place === "you" && isOpen(t)).sort((a, b) => (isRequest(b) ? 1 : 0) - (isRequest(a) ? 1 : 0));
  out.push({ id: "you", title: "You", rows: you, drop: { place: "you" }, n: you.length });
  for (const p of PROJECTS) {
    if (project && project !== p.id) continue;
    const rows = scoped.filter((t) => t.place === "backlog" && t.project === p.id && isOpen(t));
    out.push({ id: `bl-${p.id}`, title: "Backlog", sub: p.id, rows, drop: { place: "backlog", id: p.id }, n: rows.length });
  }
  if (agents) {
    for (const sid of Object.keys(SESSION_NAMES)) {
      const rows = scoped.filter((t) => t.place === "session" && t.session === sid);
      if (!rows.length) continue;
      out.push({ id: `s-${sid}`, title: SESSION_NAMES[sid], state: WORKING.has(sid) ? "Working" : "", sub: projectName(SESSION_CWD[sid]), rows, drop: { place: "session", id: sid }, progress: `${rows.filter((t) => !isOpen(t)).length}/${rows.length}` });
    }
  }
  const done = scoped.filter((t) => !isOpen(t) && t.place !== "session");
  if (done.length) out.push({ id: "done", title: "Done", rows: done, n: done.length, collapsed: true });
  return out;
}

// ── Small pieces ─────────────────────────────────────────────────────────

const Kbd = ({ children }) => <kbd class="tl-kbd">{children}</kbd>;
const MOD_ENTER = formatShortcut("↵", { mod: true });

function TasksGlyph() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M2.5 4.2l1.3 1.3 2.2-2.4M2.5 10.2l1.3 1.3 2.2-2.4M8.5 4.5H14M8.5 10.5H14" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  );
}
function GoIcon() {
  return (
    <svg class="zl-go" viewBox="0 0 12 12" aria-hidden="true">
      <path d="M4 2.5L7.5 6 4 9.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  );
}
function BackIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M10 3.5L5.5 8l4.5 4.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  );
}
function CloseIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" />
    </svg>
  );
}

function Check16({ task, onToggle, big }) {
  const done = task.status === "done";
  return (
    <button
      type="button"
      class={`tl-check${done ? " is-done" : ""}${task.status === "working" ? " is-working" : ""}${big ? " is-big" : ""}`}
      aria-label={done ? `Reopen ${task.title}` : `Complete ${task.title}`}
      onClick={(e) => { e.stopPropagation(); onToggle(); }}
    >
      <Check size={10} strokeWidth={3.2} aria-hidden="true" />
    </button>
  );
}

// Mounts children into an element of the production chrome, found by selector.
// The host is re-attached if the chrome re-mounts its node (drawer, sheet).
function usePortalHost(selector, { before, prepend, display = "contents", cls = "" } = {}) {
  const [host] = useState(() => {
    const el = document.createElement("div");
    el.style.display = display;
    el.className = `tl-portal ${cls}`;
    return el;
  });
  const [target, setTarget] = useState(null);
  useLayoutEffect(() => {
    const place = () => {
      const t = document.querySelector(selector);
      if (!t) { if (host.parentNode) host.remove(); setTarget(null); return; }
      const ref = before ? t.querySelector(`:scope > ${before}`) : prepend ? t.firstChild : null;
      if (host.parentNode !== t || (ref && host.nextSibling !== ref && ref !== host) || (!ref && !prepend && host.nextSibling)) {
        t.insertBefore(host, ref && ref !== host ? ref : null);
      }
      setTarget(t);
    };
    place();
    const mo = new MutationObserver(place);
    mo.observe(document.querySelector(".tl-stage") || document.body, { childList: true, subtree: true });
    return () => { mo.disconnect(); host.remove(); };
  }, [selector]);
  return target ? host : null;
}

// ── Lab state across the tree ─────────────────────────────────────────────

const lab = {
  params: new URLSearchParams(typeof location === "undefined" ? "" : location.search),
  get scene() { return this.params.get("scene") || "global"; },
  get device() { return this.params.get("device") || "desktop"; },
  get foot() { return this.params.get("foot") || "rows"; },
};

// ── The sidebar foot: the Tasks door ────────────────────────────────────

function TasksDoor({ on, onOpen }) {
  const ts = useTasks();
  const n = openRequests(ts).length;
  const host = usePortalHost(".tl-stage .zl-side-foot", { before: ".zl-side-app" });
  if (!host) return null;
  return createPortal(
    <button
      type="button"
      class={`zl-inbox tl-door${on ? " is-on" : ""}`}
      aria-pressed={!!on}
      aria-label={n ? `Tasks, ${n} for you` : "Tasks"}
      title={`Tasks  ${formatShortcut("T", { mod: true, shift: true })}`}
      onClick={onOpen}
    >
      <TasksGlyph />
      Tasks
      {n > 0 && <span class="tl-door-n zl-data">{n}</span>}
    </button>,
    host,
  );
}

// Proposal B/C of the foot replace the version node, which production renders
// as one text run; a lab cannot restyle half of a text node.
function FootVersionProposal() {
  const host = usePortalHost(".tl-stage .zl-side-app", { prepend: true });
  const mode = lab.foot;
  if (!host || (mode !== "short" && mode !== "settings")) return null;
  if (mode === "short") {
    return createPortal(
      <a class="zl-ver zl-data is-update tl-ver-short" href="#" onClick={(e) => e.preventDefault()} title={`${PROD_VERSION.current} → ${PROD_VERSION.latest}. Update available`}>
        ↑ {PROD_VERSION.latest}
      </a>,
      host,
    );
  }
  return createPortal(<span class="tl-gear-dot" aria-label="Update available" />, host);
}

// ── Global view (desktop) ────────────────────────────────────────────────

function Row({ task, selected, onSelect, completing, setCompleting, phone, context, dragging, setDragging }) {
  const toggle = () => {
    if (task.status === "done") return reopenTask(task.id);
    if (isRequest(task)) return setCompleting(completing === task.id ? null : task.id);
    completeTask(task.id);
  };
  const waiting = task.waits.map(byId).find((b) => b && isOpen(b));
  const meta = [];
  if (task.status === "working") meta.push(<span class="tl-word-working">Working</span>);
  if (waiting && isOpen(task)) meta.push(<span class="tl-meta-wait"><Link2 size={12} aria-hidden="true" />Waits for {waiting.title}</span>);
  if (task.status === "done" && task.note) meta.push(<span class="tl-meta-note">“{task.note}”</span>);
  if (isRequest(task) && context !== "session") meta.push(<span class="tl-meta-from">{SESSION_NAMES[task.from]}</span>);
  if (task.project && !context && task.place === "you") meta.push(<span class="tl-meta-proj">{task.project}</span>);
  if (task.subs.length) meta.push(<span class="tl-data">{task.subs.filter((s) => s.done).length}/{task.subs.length}</span>);
  const open = completing === task.id;
  return (
    <div
      class={`tl-row${selected ? " is-selected" : ""}${task.status === "done" ? " is-done" : ""}${open ? " is-completing" : ""}${phone ? " is-phone" : ""}${dragging === task.id ? " is-dragging" : ""}`}
      data-task={task.id}
      role="option"
      aria-selected={!!selected}
      draggable={!phone && !open}
      onDragStart={(e) => { e.dataTransfer.setData("text/plain", task.id); e.dataTransfer.effectAllowed = "move"; setDragging?.(task.id); }}
      onDragEnd={() => setDragging?.(null)}
      onClick={() => !open && onSelect(task.id)}
    >
      <div class="tl-row-line">
        {task.isNew && isOpen(task) && <span class="tl-new" aria-label="New" />}
        <Check16 task={task} onToggle={toggle} big={phone} />
        <span class="tl-row-main">
          <span class="tl-row-title">{task.title}</span>
          {phone && meta.length > 0 && <span class="tl-row-meta">{meta.map((m, i) => <>{i > 0 && <span class="tl-sep">·</span>}{m}</>)}</span>}
        </span>
        {!phone && meta.length > 0 && <span class="tl-row-meta">{meta.map((m, i) => <>{i > 0 && <span class="tl-sep">·</span>}{m}</>)}</span>}
        <span class="tl-row-age tl-data">{task.age}</span>
      </div>
      {open && <CompleteNote task={task} onCancel={() => setCompleting(null)} onDone={(note) => { completeTask(task.id, note); setCompleting(null); }} inline phone={phone} />}
    </div>
  );
}

function CompleteNote({ task, onCancel, onDone, inline, phone, preset = "" }) {
  const [note, setNote] = useState(preset);
  const ref = useRef(null);
  useEffect(() => { ref.current?.focus({ preventScroll: true }); }, []);
  const who = SESSION_NAMES[task.from];
  return (
    <div class={`tl-note${inline ? " is-inline" : ""}${phone ? " is-phone" : ""}`} onClick={(e) => e.stopPropagation()}>
      <textarea
        ref={ref}
        class="tl-field tl-note-field"
        rows={2}
        placeholder={`Note for ${who} (optional)`}
        value={note}
        onInput={(e) => setNote(e.currentTarget.value)}
        onKeyDown={(e) => { if (e.key === "Enter" && (e.metaKey || e.altKey)) { e.preventDefault(); onDone(note); } if (e.key === "Escape") onCancel(); }}
      />
      <div class="tl-note-acts">
        <button type="button" class="zl-ask-btn is-quiet" onClick={onCancel}>Cancel</button>
        <button type="button" class="zl-ask-btn is-primary" onClick={() => onDone(note)}>
          Done{!phone && <Kbd>{MOD_ENTER}</Kbd>}
        </button>
      </div>
    </div>
  );
}

function GroupHead({ g, open, onToggle, drop, dragging, setDragging, forceOver }) {
  const [over, setOver] = useState(false);
  const live = !!g.drop && !!dragging;
  const hot = forceOver || (live && over);
  return (
    <div
      class={`tl-group${live ? " is-drop" : ""}${hot ? " is-over" : ""}${g.collapsed ? " is-toggle" : ""}`}
      onClick={g.collapsed ? onToggle : undefined}
      onDragOver={(e) => { if (live) { e.preventDefault(); setOver(true); } }}
      onDragLeave={() => setOver(false)}
      onDrop={(e) => { e.preventDefault(); setOver(false); if (live) { moveTask(dragging, g.drop); setDragging(null); } }}
    >
      {g.collapsed && <ChevronDown size={13} class={`tl-group-chev${open ? "" : " is-closed"}`} aria-hidden="true" />}
      <span class="tl-group-t">{g.title}</span>
      {g.sub && <span class="tl-group-sub">{g.sub}</span>}
      {g.state && <span class="tl-word-working">{g.state}</span>}
      {g.n > 0 && <span class="tl-group-n tl-data">{g.n}</span>}
      {g.progress && <span class="tl-group-n tl-data">{g.progress}</span>}
      {hot && <span class="tl-drop-hint">{g.drop.place === "session" ? "Assign · notifies" : g.drop.place === "backlog" ? "Move to backlog" : "Move to You"}</span>}
    </div>
  );
}

function TaskList({ groups, selected, onSelect, completing, setCompleting, phone, context, onAdd, demoDrag }) {
  const [dragging, setDragging] = useState(demoDrag?.task || null);
  const [doneOpen, setDoneOpen] = useState(false);
  const visible = groups.filter((g) => g.rows.length || (dragging && g.drop) || g.add);
  if (!visible.length || visible.every((g) => g.id === "done")) return null;
  return (
    <div class={`tl-list${phone ? " is-phone" : ""}`} role="listbox" aria-label="Tasks">
      {visible.map((g) => {
        const open = !g.collapsed || doneOpen;
        return (
          <section class="tl-sec" key={g.id}>
            <GroupHead g={g} open={open} onToggle={() => setDoneOpen(!doneOpen)} dragging={dragging} setDragging={setDragging} forceOver={demoDrag?.over === g.id} />
            {open && g.rows.map((t) => (
              <Row key={t.id} task={t} selected={selected === t.id} onSelect={onSelect} completing={completing} setCompleting={setCompleting} phone={phone} context={context} dragging={dragging} setDragging={setDragging} />
            ))}
            {g.add && (
              <button type="button" class={`tl-add${phone ? " is-phone" : ""}`} onClick={onAdd}>
                <Plus size={14} aria-hidden="true" />Add a task
              </button>
            )}
          </section>
        );
      })}
    </div>
  );
}

function Filters({ agents, setAgents, project, setProject, phone }) {
  const [menu, setMenu] = useState(false);
  return (
    <div class={`tl-filters${phone ? " is-phone" : ""}`}>
      <div class="tl-anchor">
        <button type="button" class={`tl-chip${project ? " is-on" : ""}`} aria-expanded={menu} onClick={() => setMenu(!menu)}>
          {project || "All projects"}<ChevronDown size={13} aria-hidden="true" />
        </button>
        {menu && (
          <div class="tl-pop is-narrow" role="menu">
            {[null, ...PROJECTS.map((p) => p.id)].map((p) => (
              <button type="button" role="menuitemradio" aria-checked={project === p} class="tl-pop-item" onClick={() => { setProject(p); setMenu(false); }}>
                <span class="tl-pop-t">{p || "All projects"}</span>
                {project === p && <Check size={14} class="tl-pop-on" aria-hidden="true" />}
              </button>
            ))}
          </div>
        )}
      </div>
      <button type="button" class={`tl-chip${agents ? " is-on" : ""}`} aria-pressed={agents} onClick={() => setAgents(!agents)}>
        Agents' tasks
      </button>
    </div>
  );
}

function EmptyState({ onNew, phone, text = "Nothing pending." }) {
  return (
    <div class={`tl-empty${phone ? " is-phone" : ""}`}>
      <span class="tl-empty-mark" aria-hidden="true"><TasksGlyph /></span>
      <p class="tl-empty-t">{text}</p>
      {onNew && (
        <button type="button" class="zl-ask-btn" onClick={onNew}>
          <Plus size={15} aria-hidden="true" />New task{!phone && <Kbd>C</Kbd>}
        </button>
      )}
    </div>
  );
}

// ── Detail ───────────────────────────────────────────────────────────────

function MoveMenu({ task, onPick, phone, pending: pend0 }) {
  const [q, setQ] = useState("");
  const [pending, setPending] = useState(pend0 || null);
  const here = (d) => task && task.place === d.place && (d.place === "you" || (d.place === "backlog" ? task.project === d.id : task.session === d.id));
  const Item = ({ d, label, sub }) => (
    <button
      type="button"
      class={`tl-pop-item${sub ? " is-two" : ""}${pending === d.id && d.place === "session" ? " is-pending" : ""}${phone ? " is-phone" : ""}`}
      onClick={() => (d.place === "session" ? setPending(d.id) : onPick(d))}
    >
      <span class="tl-pop-t">{label}</span>
      {sub && <span class="tl-pop-sub">{sub}</span>}
      {here(d) && <Check size={14} class="tl-pop-on" aria-hidden="true" />}
    </button>
  );
  const sess = Object.keys(SESSION_NAMES).filter((id) => !q || SESSION_NAMES[id].toLowerCase().includes(q.toLowerCase()));
  return (
    <div class={`tl-move${phone ? " is-phone" : ""}`} onClick={(e) => e.stopPropagation()}>
      {!phone && (
        <label class="tl-search">
          <Search size={14} aria-hidden="true" />
          <input class="tl-field" placeholder="Move to…" value={q} onInput={(e) => setQ(e.currentTarget.value)} />
        </label>
      )}
      {!q && <Item d={{ place: "you" }} label="You" />}
      {!q && <div class="tl-pop-label">Backlog</div>}
      {!q && PROJECTS.map((p) => <Item d={{ place: "backlog", id: p.id }} label={p.id} />)}
      <div class="tl-pop-label">Assign to a session</div>
      {phone && (
        <label class="tl-search is-phone">
          <Search size={15} aria-hidden="true" />
          <input class="tl-field" placeholder="Find a session" value={q} onInput={(e) => setQ(e.currentTarget.value)} />
        </label>
      )}
      {sess.map((id) => (
        <Item d={{ place: "session", id }} label={SESSION_NAMES[id]} sub={<>{projectName(SESSION_CWD[id])}{WORKING.has(id) && <> · <span class="tl-word-working">Working</span></>}</>} />
      ))}
      {pending && (
        <div class="tl-move-confirm">
          <button type="button" class="zl-ask-btn is-primary tl-wide" onClick={() => onPick({ place: "session", id: pending })}>
            Assign and notify{!phone && <Kbd>↵</Kbd>}
          </button>
        </div>
      )}
    </div>
  );
}

function Detail({ task, phone, onClose, onOpen, init = {}, isNew, newPlace, onCreated, onPushMove }) {
  const blank = { title: "", desc: "", subs: [], waits: [] };
  const [draft, setDraft] = useState(() => ({ ...(isNew ? blank : task), ...(init.dirty || {}) }));
  const [menu, setMenu] = useState(!!init.menu);
  const [completing, setCompleting] = useState(!!init.completing);
  const [sub, setSub] = useState("");
  const [dep, setDep] = useState(false);
  const [place, setPlace] = useState(newPlace || { place: "you" });
  const shown = useRef(task?.id);
  useEffect(() => {
    if (isNew || shown.current === task?.id) return;
    shown.current = task?.id;
    setDraft({ ...task }); setMenu(false); setCompleting(false);
  }, [task?.id]);
  const keys = useRef({});
  keys.current = {
    move: () => (phone ? onPushMove?.() : setMenu(true)),
    done: () => (isRequest(task) ? setCompleting(true) : completeTask(task.id)),
  };
  useEffect(() => {
    if (phone || isNew) return undefined;
    const onKey = (e) => {
      const typing = /INPUT|TEXTAREA/.test(e.target.tagName);
      if (e.key === "Enter" && (e.metaKey || e.altKey) && !typing && task && isOpen(task)) { e.preventDefault(); keys.current.done(); }
      if (!typing && !e.metaKey && !e.altKey && e.key.toLowerCase() === "m") { e.preventDefault(); keys.current.move(); }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [phone, isNew, task?.id]);

  if (!isNew && !task) return null;
  const dirty = !isNew && ["title", "desc", "subs", "waits"].some((k) => JSON.stringify(draft[k]) !== JSON.stringify(task[k]));
  const who = isNew ? (place.place === "session" ? place.id : null) : listenerOf(task);
  const done = !isNew && task.status === "done";
  const setSubAt = (i, p) => setDraft({ ...draft, subs: draft.subs.map((s, j) => (j === i ? { ...s, ...p } : s)) });
  const save = () => patchTask(task.id, { title: draft.title, desc: draft.desc, subs: draft.subs, waits: draft.waits });
  const waits = draft.waits.map(byId).filter(Boolean);
  const unblocks = isNew ? [] : tasks.filter((t) => t.waits.includes(task.id));
  const candidates = tasks.filter((t) => isOpen(t) && t.id !== task?.id && !draft.waits.includes(t.id)).slice(0, 6);
  const whereText = isNew
    ? place.place === "you" ? "You" : place.place === "backlog" ? `Backlog · ${place.id}` : SESSION_NAMES[place.id]
    : placeLabel(task);
  const pick = (d) => {
    if (isNew) setPlace(d); else moveTask(task.id, d);
    setMenu(false);
  };
  const create = () => {
    if (!draft.title.trim()) return;
    const id = `n${Date.now()}`;
    setTasks((ts) => [{
      ...T(id, draft.title.trim(), place.place), desc: draft.desc, subs: draft.subs, waits: draft.waits, age: "now",
      project: place.place === "backlog" ? place.id : place.place === "session" ? projectName(SESSION_CWD[place.id]) : null,
      session: place.place === "session" ? place.id : undefined,
    }, ...ts]);
    onCreated?.(id);
  };

  return (
    <div class={`tl-detail${phone ? " is-phone" : ""}`}>
      <div class="tl-detail-body">
        <textarea
          class={`tl-field tl-title${done ? " is-done" : ""}`}
          rows={1}
          placeholder="Task title"
          aria-label="Title"
          value={draft.title}
          onInput={(e) => { setDraft({ ...draft, title: e.currentTarget.value }); e.currentTarget.style.height = "auto"; e.currentTarget.style.height = `${e.currentTarget.scrollHeight}px`; }}
          ref={(el) => { if (el) { el.style.height = "auto"; el.style.height = `${el.scrollHeight}px`; if (isNew && !el.dataset.f) { el.dataset.f = "1"; el.focus({ preventScroll: true }); } } }}
        />
        <textarea
          class="tl-field tl-desc"
          rows={draft.desc ? 4 : 1}
          placeholder="Add details"
          aria-label="Details"
          value={draft.desc}
          onInput={(e) => setDraft({ ...draft, desc: e.currentTarget.value })}
        />

        <dl class="tl-props">
          <div class="tl-prop">
            <dt>Where</dt>
            <dd class="tl-anchor">
              <button type="button" class="tl-prop-btn" aria-expanded={menu} onClick={() => (phone && !isNew ? onPushMove?.() : setMenu(!menu))}>
                <span class="tl-prop-v">{whereText}</span>
                {!phone && !isNew && <Kbd>M</Kbd>}
                <ChevronDown size={13} aria-hidden="true" />
              </button>
              {menu && (!phone || isNew) && (
                <div class="tl-pop is-move" role="menu">
                  <MoveMenu task={isNew ? { place: place.place, project: place.id, session: place.id } : task} onPick={pick} pending={init.pending} />
                </div>
              )}
            </dd>
          </div>
          {!isNew && isRequest(task) && (
            <div class="tl-prop">
              <dt>Asked by</dt>
              <dd><a href="#" class="tl-prop-link" onClick={(e) => e.preventDefault()}><span class={`tl-sdot${WORKING.has(task.from) ? " is-working" : ""}`} />{SESSION_NAMES[task.from]}</a></dd>
            </div>
          )}
          {!isNew && task.project && task.place !== "backlog" && (
            <div class="tl-prop"><dt>Project</dt><dd class="tl-prop-v">{task.project}</dd></div>
          )}
          {!isNew && (
            <div class="tl-prop"><dt>Created</dt><dd class="tl-prop-v tl-data">{task.age === "now" ? "now" : `${task.age} ago`}</dd></div>
          )}
        </dl>

        {done && task.note && (
          <div class="tl-sent"><span class="tl-sent-k">Your note</span><p>{task.note}</p></div>
        )}

        <section class="tl-block">
          <div class="tl-block-h">Subtasks{draft.subs.length > 0 && <span class="tl-data">{draft.subs.filter((s) => s.done).length}/{draft.subs.length}</span>}</div>
          {draft.subs.map((s, i) => (
            <div class={`tl-sub${s.done ? " is-done" : ""}`}>
              <Check16 task={{ title: s.t, status: s.done ? "done" : "open" }} onToggle={() => setSubAt(i, { done: !s.done })} />
              <span class="tl-sub-t">{s.t}</span>
              <button type="button" class="tl-icon is-quiet" aria-label="Remove subtask" onClick={() => setDraft({ ...draft, subs: draft.subs.filter((_, j) => j !== i) })}><CloseIcon /></button>
            </div>
          ))}
          <label class="tl-sub is-add">
            <Plus size={14} aria-hidden="true" />
            <input
              class="tl-field"
              placeholder="Add subtask"
              value={sub}
              onInput={(e) => setSub(e.currentTarget.value)}
              onKeyDown={(e) => { if (e.key === "Enter" && sub.trim()) { setDraft({ ...draft, subs: [...draft.subs, { t: sub.trim(), done: false }] }); setSub(""); } }}
            />
          </label>
        </section>

        <section class="tl-block">
          <div class="tl-block-h">Waits for</div>
          {waits.map((b) => (
            <div class="tl-dep">
              <span class={`tl-dep-s${isOpen(b) ? "" : " is-done"}`}>{isOpen(b) ? "Open" : "Done"}</span>
              <button type="button" class="tl-dep-t" onClick={() => onOpen?.(b.id)}>{b.title}</button>
              <button type="button" class="tl-icon is-quiet" aria-label="Remove" onClick={() => setDraft({ ...draft, waits: draft.waits.filter((x) => x !== b.id) })}><CloseIcon /></button>
            </div>
          ))}
          <div class="tl-anchor">
            <button type="button" class="tl-sub is-add is-btn" onClick={() => setDep(!dep)}><Plus size={14} aria-hidden="true" /><span>Add a task it waits for</span></button>
            {dep && (
              <div class="tl-pop is-dep" role="menu">
                {candidates.map((c) => (
                  <button type="button" class="tl-pop-item is-two" onClick={() => { setDraft({ ...draft, waits: [...draft.waits, c.id] }); setDep(false); }}>
                    <span class="tl-pop-t">{c.title}</span><span class="tl-pop-sub">{placeLabel(c)}</span>
                  </button>
                ))}
              </div>
            )}
          </div>
          {unblocks.length > 0 && (
            <>
              <div class="tl-block-h is-sub">Unblocks</div>
              {unblocks.map((b) => (
                <div class="tl-dep">
                  <span class={`tl-dep-s${isOpen(b) ? "" : " is-done"}`}>{isOpen(b) ? "Open" : "Done"}</span>
                  <button type="button" class="tl-dep-t" onClick={() => onOpen?.(b.id)}>{b.title}</button>
                  <span class="tl-dep-w">{placeLabel(b)}</span>
                </div>
              ))}
            </>
          )}
        </section>
      </div>

      <div class={`tl-foot${completing ? " is-note" : ""}`}>
        {isNew ? (
          <>
            <span class="tl-grow" />
            <button type="button" class="zl-ask-btn is-primary" disabled={!draft.title.trim()} onClick={create}>
              {place.place === "session" ? "Assign and notify" : "Add task"}{!phone && <Kbd>{MOD_ENTER}</Kbd>}
            </button>
          </>
        ) : dirty ? (
          <>
            <button type="button" class="zl-ask-btn is-quiet" onClick={() => setDraft({ ...task })}>Discard</button>
            <span class="tl-grow" />
            <button type="button" class={`zl-ask-btn${who ? "" : " is-primary"}`} onClick={save}>Save</button>
            {who && <button type="button" class="zl-ask-btn is-primary" onClick={save} title={`Tells ${SESSION_NAMES[who]}`}>Save and notify</button>}
          </>
        ) : completing ? (
          <CompleteNote task={task} phone={phone} preset={init.note} onCancel={() => setCompleting(false)} onDone={(note) => { completeTask(task.id, note); setCompleting(false); }} />
        ) : done ? (
          <>
            <span class="tl-foot-fact">Done{task.age === "now" ? " just now" : ""}</span>
            <span class="tl-grow" />
            <button type="button" class="zl-ask-btn" onClick={() => reopenTask(task.id)}>Reopen</button>
          </>
        ) : (
          <>
            <button type="button" class="tl-icon" aria-label="Delete task" onClick={() => { removeTask(task.id); onClose?.(); }}><Trash2 size={15} aria-hidden="true" /></button>
            <span class="tl-grow" />
            <button type="button" class="zl-ask-btn" onClick={() => (phone ? onPushMove?.() : setMenu(true))}>Move{!phone && <Kbd>M</Kbd>}</button>
            <button type="button" class="zl-ask-btn is-primary" onClick={() => keys.current.done()}>
              <Check size={15} strokeWidth={2.4} aria-hidden="true" />Done{!phone && <Kbd>{MOD_ENTER}</Kbd>}
            </button>
          </>
        )}
      </div>
    </div>
  );
}

// The task's dossier on the desktop: the same third zone, the same drawer,
// the same head as the session's. Rendered into the shell so it docks.
function TaskDossier({ task, title, onClose, onBack, children }) {
  const host = usePortalHost(".tl-stage .desktop-shell");
  const [entered, setEntered] = useState(false);
  useEffect(() => { const r = requestAnimationFrame(() => setEntered(true)); return () => cancelAnimationFrame(r); }, []);
  if (!host) return null;
  return createPortal(
    <div class="desktop-dossier is-open tl-dossier">
      <aside class={`zl-side zl-side-right${entered ? " is-open" : ""}`} role="dialog" aria-label={title}>
        <div class={`zl-side-head${onBack ? " is-sub" : ""}`}>
          {onBack && <button type="button" class="zl-back" onClick={onBack} aria-label="Back to this session"><BackIcon /></button>}
          <span class={`zl-side-title ${onBack ? "is-page" : "is-eyebrow"}`}>{title}</span>
          <button type="button" class="zl-x" onClick={onClose} aria-label="Close"><CloseIcon /></button>
        </div>
        {children}
      </aside>
    </div>,
    host,
  );
}

function TasksMain({ init = {} }) {
  const ts = useTasks();
  const [sel, setSel] = useState(init.selected || null);
  const [creating, setCreating] = useState(!!init.creating);
  const [agents, setAgents] = useState(!!init.agents);
  const [project, setProject] = useState(null);
  const [completing, setCompleting] = useState(init.completing || null);
  const groups = useMemo(() => groupsFor(ts, { agents, project }), [ts, agents, project]);
  const flat = groups.filter((g) => !g.collapsed).flatMap((g) => g.rows);
  const task = sel ? ts.find((t) => t.id === sel) : null;
  const listRef = useRef(null);
  const nav = useRef({});
  nav.current = { flat, sel };
  useEffect(() => {
    const onKey = (e) => {
      if (/INPUT|TEXTAREA/.test(e.target.tagName) || e.metaKey || e.altKey || e.ctrlKey) return;
      const { flat: f, sel: s } = nav.current;
      const i = f.findIndex((t) => t.id === s);
      if (e.key === "ArrowDown" || e.key === "j") { e.preventDefault(); setSel(f[Math.min(f.length - 1, i + 1)]?.id); setCreating(false); }
      if (e.key === "ArrowUp" || e.key === "k") { e.preventDefault(); setSel(f[Math.max(0, i - 1)]?.id); setCreating(false); }
      if (e.key === "c") { e.preventDefault(); setCreating(true); setSel(null); }
      if (e.key === "Escape") { setSel(null); setCreating(false); }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);
  useEffect(() => {
    const row = listRef.current?.querySelector(`[data-task="${sel}"]`);
    row?.scrollIntoView({ block: "nearest" });
  }, [sel]);
  const openCount = ts.filter((t) => isOpen(t) && t.place !== "session").length;
  return (
    <main class="conversation-main tl-main" ref={listRef}>
      <header class="zl-desk-head tl-head">
        <div class="tl-crumb">
          <span class="zl-crumb-title">Tasks</span>
          {openCount > 0 && <span class="tl-crumb-n zl-data">{openCount} open</span>}
        </div>
        <span class="tl-grow" />
        <button type="button" class="zl-ask-btn tl-newbtn" onClick={() => { setCreating(true); setSel(null); }}>
          <Plus size={15} aria-hidden="true" />New task<Kbd>C</Kbd>
        </button>
      </header>
      <Filters agents={agents} setAgents={setAgents} project={project} setProject={setProject} />
      <div class="tl-scroll">
        {TaskList({ groups, selected: sel, onSelect: (id) => { setSel(id); setCreating(false); }, completing, setCompleting, demoDrag: init.demoDrag })
          || <EmptyState onNew={() => setCreating(true)} />}
      </div>
      {creating && (
        <TaskDossier title="New task" onClose={() => setCreating(false)}>
          <Detail isNew newPlace={init.newPlace} onCreated={(id) => { setCreating(false); setSel(id); }} />
        </TaskDossier>
      )}
      {task && !creating && (
        <TaskDossier title={task.status === "done" ? "Done" : isRequest(task) ? "For you" : task.place === "session" ? "Agent task" : task.place === "backlog" ? "Backlog" : "Your task"} onClose={() => setSel(null)}>
          <Detail key={task.id} task={task} onClose={() => setSel(null)} onOpen={setSel} init={init.detail} />
        </TaskDossier>
      )}
    </main>
  );
}

// ── The pinned line above the composer ───────────────────────────────────

function PinnedLine({ sessionId, phone, onOpenTask, startCompleting, preset }) {
  const ts = useTasks();
  const reqs = openRequests(ts).filter((t) => t.from === sessionId);
  const [completing, setCompleting] = useState(!!startCompleting);
  const host = usePortalHost(phone ? ".tl-stage .mcomposer.zl-dock" : ".tl-stage .conversation-main .zl-dock", { prepend: true, display: "block", cls: "tl-pin-host" });
  if (!host || !reqs.length) return null;
  const t = reqs[0];
  return createPortal(
    <div class={`tl-pin${phone ? " is-phone" : ""}${completing ? " is-open" : ""}`}>
      {completing ? (
        <CompleteNote task={t} phone={phone} preset={preset} onCancel={() => setCompleting(false)} onDone={(note) => { completeTask(t.id, note); setCompleting(false); }} />
      ) : (
        <div class="tl-pin-bar">
          <span class="tl-pin-ico"><TasksGlyph /></span>
          <span class="tl-pin-k">For you</span>
          <button type="button" class="tl-pin-t" onClick={() => onOpenTask(t.id)}>{t.title}</button>
          {reqs.length > 1 && <span class="tl-pin-more tl-data">+{reqs.length - 1}</span>}
          <button type="button" class="tl-pin-done" onClick={() => setCompleting(true)}>
            <Check size={14} strokeWidth={2.4} aria-hidden="true" />Done
          </button>
        </div>
      )}
    </div>,
    host,
  );
}

// ── The dossier row and page ─────────────────────────────────────────────

function PanelTasksRow({ sessionId, onOpen }) {
  const ts = useTasks();
  const host = usePortalHost(".tl-stage .zl-side-right:not(.tl-own) .zl-prows", { prepend: true });
  if (!host) return null;
  const mine = ts.filter((t) => t.place === "session" && t.session === sessionId);
  const reqs = openRequests(ts).filter((t) => t.from === sessionId);
  const verdict = [reqs.length ? `${reqs.length} for you` : "", mine.length ? `${mine.filter((t) => !isOpen(t)).length}/${mine.length}` : ""].filter(Boolean).join(" · ") || "none";
  return createPortal(
    <button type="button" class="zl-prow" onClick={onOpen} aria-label={`Tasks: ${verdict}`}>
      <svg class="zl-prow-ico" viewBox="0 0 16 16" aria-hidden="true"><path d="M2.5 4.2l1.3 1.3 2.2-2.4M2.5 10.2l1.3 1.3 2.2-2.4M8.5 4.5H14M8.5 10.5H14" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" /></svg>
      <span class="zl-prow-t">Tasks</span>
      <span class="zl-prow-v zl-data">{verdict}</span>
      <GoIcon />
    </button>,
    host,
  );
}

// The session's Tasks page, then one task, then (phone) Move: every level is a
// page of the same panel.
function SessionTasksPages({ sessionId, stack, setStack, phone }) {
  const ts = useTasks();
  const [completing, setCompleting] = useState(null);
  const top = stack[stack.length - 1];
  const push = (p) => setStack([...stack, p]);
  if (top === "tasks") {
    const groups = groupsFor(ts, { session: sessionId });
    return (
      <div class="zl-panel-body is-sub tl-panel-body">
        <TaskList groups={groups} context="session" phone onSelect={(id) => push(`task:${id}`)} completing={completing} setCompleting={setCompleting} onAdd={() => push("new")} />
      </div>
    );
  }
  if (top === "new") return <Detail isNew phone={phone} newPlace={{ place: "session", id: sessionId }} onCreated={() => setStack(stack.slice(0, -1))} />;
  if (top.startsWith("move:")) {
    const t = byId(top.slice(5));
    return <div class="tl-panel-body tl-move-page"><MoveMenu task={t} phone onPick={(d) => { moveTask(t.id, d); setStack(stack.slice(0, -1)); }} /></div>;
  }
  const t = byId(top.slice(5));
  return t ? <Detail key={t.id} task={t} phone={phone} onOpen={(id) => push(`task:${id}`)} onClose={() => setStack(stack.slice(0, -1))} onPushMove={() => push(`move:${t.id}`)} /> : null;
}

function pageTitle(top) {
  if (top === "tasks") return "Tasks";
  if (top === "new") return "New task";
  if (top.startsWith("move:")) return "Move to";
  const t = byId(top.slice(5));
  return t ? (isRequest(t) ? "For you" : "Task") : "Task";
}

function DesktopSession({ init = {} }) {
  const [stack, setStack] = useState(init.stack || null);
  useEffect(() => { if (!stack) openSessionPanel("pulse", "root"); else closeSessionPanel(); }, [!!stack]);
  const top = stack?.[stack.length - 1];
  return (
    <>
      <ConversationScreen />
      <PinnedLine sessionId="pulse" onOpenTask={(id) => setStack(["tasks", `task:${id}`])} startCompleting={init.pinCompleting} preset={init.note} />
      <PanelTasksRow sessionId="pulse" onOpen={() => setStack(["tasks"])} />
      {stack && (
        <TaskDossier
          title={pageTitle(top)}
          onClose={() => setStack(null)}
          onBack={() => (stack.length > 1 ? setStack(stack.slice(0, -1)) : setStack(null))}
        >
          <SessionTasksPages sessionId="pulse" stack={stack} setStack={setStack} />
        </TaskDossier>
      )}
    </>
  );
}

// ── Phone ────────────────────────────────────────────────────────────────

function PhonePush({ title, onBack, right, children, depth = 0 }) {
  return (
    <div class="minbox tl-push" style={{ zIndex: 40 + depth }}>
      <div class="zi-inbox is-phone tl-push-in">
        <div class="zi-head is-sheet">
          <button type="button" class="zi-back" onClick={onBack} aria-label="Back"><BackIcon /></button>
          <span class="zi-title">{title}</span>
          {right}
        </div>
        {children}
      </div>
    </div>
  );
}

function PhoneTasks({ init = {}, onBack }) {
  const ts = useTasks();
  const [stack, setStack] = useState(init.stack || []);
  const [agents, setAgents] = useState(!!init.agents);
  const [project, setProject] = useState(null);
  const [completing, setCompleting] = useState(init.completing || null);
  const groups = groupsFor(ts, { agents, project });
  const push = (p) => setStack([...stack, p]);
  const pop = () => setStack(stack.slice(0, -1));
  const list = TaskList({ groups, phone: true, onSelect: (id) => push(`task:${id}`), completing, setCompleting });
  return (
    <>
      <PhonePush
        title="Tasks"
        onBack={onBack}
        right={<button type="button" class="zi-x tl-plus" aria-label="New task" onClick={() => push("new")}><Plus size={18} aria-hidden="true" /></button>}
      >
        <div class="tl-phone-body">
          <Filters phone agents={agents} setAgents={setAgents} project={project} setProject={setProject} />
          {list || <EmptyState phone onNew={() => push("new")} />}
        </div>
      </PhonePush>
      {stack.map((p, i) => {
        const t = p.startsWith("task:") || p.startsWith("move:") ? byId(p.slice(5)) : null;
        return (
          <PhonePush key={p} depth={i + 1} title={p === "new" ? "New task" : p.startsWith("move:") ? "Move to" : ""} onBack={() => setStack(stack.slice(0, i))}>
            {p === "new" && <Detail isNew phone newPlace={init.newPlace} onCreated={(id) => setStack([`task:${id}`])} />}
            {p.startsWith("task:") && t && <Detail key={t.id} task={t} phone onOpen={(id) => push(`task:${id}`)} onClose={pop} onPushMove={() => push(`move:${t.id}`)} init={i === stack.length - 1 ? init.detail : undefined} />}
            {p.startsWith("move:") && t && <div class="tl-phone-body"><MoveMenu task={t} phone pending={init.pending} onPick={(d) => { moveTask(t.id, d); pop(); }} /></div>}
          </PhonePush>
        );
      })}
    </>
  );
}

function PhoneSession({ init = {} }) {
  const [stack, setStack] = useState(init.stack || null);
  const [tasksOpen, setTasksOpen] = useState(!!init.tasksOpen);
  useEffect(() => { if (init.panelRoot) openSessionPanel("pulse", "root"); }, []);
  const host = usePortalHost(".tl-stage .mconv", { display: "contents" });
  const top = stack?.[stack.length - 1];
  return (
    <>
      <MobileConversationScreen forceMobile version={PROD_VERSION} />
      <PinnedLine phone sessionId="pulse" onOpenTask={(id) => { closeSessionPanel(); setStack(["tasks", `task:${id}`]); }} startCompleting={init.pinCompleting} preset={init.note} />
      <PanelTasksRow sessionId="pulse" onOpen={() => { closeSessionPanel(); setTimeout(() => setStack(["tasks"]), 60); }} />
      <TasksDoor onOpen={() => { setState({ drawerOpen: false }); setTasksOpen(true); }} />
      {host && createPortal(
        <MobileSheet
          open={!!stack}
          onClose={() => setStack(null)}
          onBack={stack && stack.length > 0 ? () => (stack.length > 1 ? setStack(stack.slice(0, -1)) : (setStack(null), openSessionPanel("pulse", "root"))) : undefined}
          title={top ? pageTitle(top) : "Tasks"}
          bare
        >
          {stack && (
            <aside class="zl-side zl-side-right is-open is-sheet tl-own">
              <div class="zl-side-head is-sub">
                <button type="button" class="zl-back" aria-label="Back" onClick={() => (stack.length > 1 ? setStack(stack.slice(0, -1)) : (setStack(null), openSessionPanel("pulse", "root")))}><BackIcon /></button>
                <h2 class="zl-side-title is-page">{pageTitle(top)}</h2>
                <button type="button" class="zl-x" aria-label="Close" onClick={() => setStack(null)}><CloseIcon /></button>
              </div>
              <SessionTasksPages phone sessionId="pulse" stack={stack} setStack={setStack} />
            </aside>
          )}
        </MobileSheet>,
        host,
      )}
      {tasksOpen && host && createPortal(<PhoneTasks onBack={() => setTasksOpen(false)} init={init.tasks} />, host)}
    </>
  );
}

// ── Scenes ───────────────────────────────────────────────────────────────

const SCENES = [
  ["global", "Tasks"],
  ["agents", "Agents' tasks"],
  ["session", "In a session"],
  ["panel", "Session › Tasks"],
  ["new", "New task"],
  ["edit", "Edit"],
  ["move", "Move"],
  ["done", "Done with a note"],
  ["sent", "The session hears it"],
  ["foot", "Sidebar foot"],
  ["empty", "Empty"],
];
const FOOTS = [
  ["today", "Foot: as today + Tasks"],
  ["rows", "Foot: two rows"],
  ["settings", "Foot: version in Settings"],
];

function seed(scene) {
  let ts = SEED.map((t) => ({ ...t, subs: t.subs.map((s) => ({ ...s })), waits: [...t.waits] }));
  if (scene === "empty") ts = [];
  if (scene === "sent") ts = ts.map((t) => (t.id === "t1" ? { ...t, status: "done", isNew: false, age: "1m", note: "Se llama GH_DEPLOY_KEY_PULSE, no GH_DEPLOY_KEY." } : t));
  tasks = ts;
  const ss = sessions(scene);
  const focus = ["global", "agents", "new", "edit", "move", "empty"].includes(scene) && lab.device === "desktop" ? null : "pulse";
  setState((s) => ({
    sessions: ss,
    sessionsLoaded: true,
    activeSession: "pulse",
    sessionPanel: { open: false, sessionId: null, page: "root" },
    drawerOpen: false,
    inboxOpen: false,
    sidebarMode: "recent",
    owners: { ...s.owners, list: OWNERS, loaded: true },
    tileTree: setTileSession(s.tileTree, s.focusedTile, focus),
  }));
}

function useNoToasts() {
  useEffect(() => {
    const clear = () => getToasts().forEach((t) => removeToast(t.id));
    clear();
    return subscribeToasts(() => setTimeout(clear, 0));
  }, []);
}

function Screen({ scene, device }) {
  const phone = device === "phone";
  if (phone) {
    const m = {
      global: { tasksOpen: true },
      agents: { tasksOpen: true, tasks: { agents: true } },
      session: {},
      panel: { stack: ["tasks"] },
      new: { tasksOpen: true, tasks: { stack: ["new"] } },
      edit: { tasksOpen: true, tasks: { stack: ["task:t7"], detail: { dirty: { subs: [{ t: "Script que mida el uso de /", done: true }, { t: "Timer de systemd cada 15 min", done: true }, { t: "Aviso por Pulse", done: false }] } } } },
      move: { tasksOpen: true, tasks: { stack: ["task:t7", "move:t7"], pending: "race" } },
      done: { pinCompleting: true, note: "Se llama GH_DEPLOY_KEY_PULSE, no GH_DEPLOY_KEY." },
      sent: {},
      foot: { drawer: true },
      empty: { tasksOpen: true },
    }[scene] || {};
    return <PhoneStage init={m} />;
  }
  const d = {
    global: { main: { selected: "t1" } },
    agents: { main: { selected: "t10", agents: true } },
    session: { session: {} },
    panel: { session: { stack: ["tasks"] } },
    new: { main: { creating: true } },
    edit: { main: { selected: "t12", agents: true, detail: { dirty: { subs: [{ t: "Job con environment staging", done: true }, { t: "Caché de módulos Go", done: false }, { t: "Rollback si falla el healthcheck", done: false }] } } } },
    move: { main: { selected: "t7", detail: { menu: true, pending: "race" }, demoDrag: null } },
    done: { main: { selected: "t1", completing: "t1" } },
    sent: { session: {} },
    foot: { session: {} },
    empty: { main: {} },
  }[scene] || {};
  return <DesktopStage init={d} />;
}

function DesktopStage({ init }) {
  const [tasksView, setTasksView] = useState(!!init.main);
  return (
    <DesktopShell version={PROD_VERSION}>
      {tasksView ? <TasksMain init={init.main} /> : <DesktopSession init={init.session} />}
      <TasksDoor on={tasksView} onOpen={() => setTasksView(!tasksView)} />
      <FootVersionProposal />
    </DesktopShell>
  );
}

function PhoneStage({ init }) {
  useEffect(() => { if (init.drawer) openDrawer("list"); }, []);
  return (
    <>
      <PhoneSession init={init} />
      <FootVersionProposal />
    </>
  );
}

function LabBar({ scene, device, foot }) {
  const href = (p) => {
    const q = new URLSearchParams(location.search);
    Object.entries(p).forEach(([k, v]) => q.set(k, v));
    return `?${q.toString()}`;
  };
  const Seg = ({ items, cur, k }) => (
    <div class="tl-lab-seg">
      {items.map(([id, label]) => <a href={href({ [k]: id })} class={`tl-lab-opt${cur === id ? " is-on" : ""}`}>{label}</a>)}
    </div>
  );
  return (
    <div class="tl-lab-bar">
      <Seg k="v" cur="b" items={[["a", "A · four places"], ["b", "B · three places"]]} />
      <Seg k="device" cur={device} items={[["desktop", "Desktop"], ["phone", "Phone"]]} />
      <Seg k="scene" cur={scene} items={SCENES} />
      <Seg k="foot" cur={foot} items={FOOTS} />
    </div>
  );
}

export function TasksLab() {
  const scene = lab.scene;
  const device = lab.device;
  const foot = lab.foot;
  const [ready, setReady] = useState(false);
  useNoToasts();
  useLayoutEffect(() => { seed(scene); setReady(true); }, []);
  if (lab.params.get("v") === "a") {
    return (
      <>
        <div class="tl-lab-top"><LabBar scene={scene} device={device} foot={foot} /></div>
        <TasksLabA />
      </>
    );
  }
  const phone = device === "phone";
  return (
    <div class={`tl-lab foot-${foot}`}>
      <LabBar scene={scene} device={device} foot={foot} />
      <div class="tl-stage">
        {ready && (
          <ScreenLab width={phone ? PHONE_LAB_WIDTH : DESKTOP_LAB_WIDTH} height={phone ? PHONE_LAB_HEIGHT : DESKTOP_LAB_HEIGHT} note={null}>
            <Screen scene={scene} device={device} />
          </ScreenLab>
        )}
      </div>
    </div>
  );
}
