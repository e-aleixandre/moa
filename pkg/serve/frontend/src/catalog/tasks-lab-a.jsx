import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import {
  Check,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  Inbox,
  Link2,
  ListTodo,
  Menu,
  MoveRight,
  Plus,
  Search,
  Settings,
  Trash2,
  X,
} from "lucide-preact";
import "./tasks-lab-a.css";

/* Global tasks — design lab only (?view=tasks).

   Nothing here is wired to the product: the sidebar, the conversation and the
   session panel are drawn by the lab so the tasks surfaces can be judged in
   place without touching a production component. Every frame is live: rows
   open, circles complete, the move menu moves, edits mark the draft dirty.

   Two models are drawn from the same fixtures:
   A · four places — Notes, For you, Backlog, Agents (the owner's proposal).
   B · three places — You, Backlog, Agents. A request from a session is a task
       on You that remembers who asked; "Notes" and "For you" are one list. */

// ── Fixtures ────────────────────────────────────────────────────────────

const PROJECTS = {
  moa: { id: "moa", name: "moa", path: "~/dev/moa" },
  winerim: { id: "winerim", name: "Winerim", path: "~/dev/winerim-backend" },
  ourown: { id: "ourown", name: "OurOwn Studio", path: "~/dev/ourown-studio" },
};

const SESSIONS = {
  pulse: { id: "pulse", name: "Pipeline de deploy de pulse-api", project: "moa", state: "Working" },
  race: { id: "race", name: "Carrera en el borrado de adjuntos", project: "moa", state: "Working" },
  pw: { id: "pw", name: "Playwright por sesión", project: "moa", state: "" },
  torres: { id: "torres", name: "Importar albaranes de Bodegas Torres", project: "winerim", state: "Working" },
  tarifas: { id: "tarifas", name: "Migrar las tarifas de distribuidor", project: "winerim", state: "" },
  checkout: { id: "checkout", name: "Checkout con Stripe", project: "ourown", state: "Working" },
};

const SEED = [
  {
    id: "t1", title: "Añadir el secret GH_DEPLOY_KEY en GitHub", place: "you", from: "pulse", project: "moa",
    desc: "Repo ealeixandre/pulse-api → Settings → Secrets and variables → Actions. La clave privada está en 1Password, «pulse-api deploy».",
    status: "open", age: "12m", isNew: true, subs: [], blockedBy: [],
  },
  {
    id: "t2", title: "Confirmar con Marta el conteo de cajas de regalo", place: "you", from: "torres", project: "winerim",
    desc: "Albarán 2231: tres líneas con importe 0. ¿Cuentan para el stock y no para la factura?",
    status: "open", age: "1h", isNew: true, subs: [], blockedBy: [],
  },
  {
    id: "t3", title: "Aprobar el texto legal del checkout", place: "you", from: "checkout", project: "ourown",
    desc: "Borrador en docs/legal/checkout.md. Falta el párrafo de devoluciones.",
    status: "open", age: "3h", subs: [], blockedBy: [],
  },
  {
    id: "t4", title: "Fijar la versión de @playwright/mcp", place: "you", project: "moa",
    desc: "", status: "open", age: "2d", subs: [], blockedBy: [],
  },
  {
    id: "t5", title: "docker builder prune (~25 GB)", place: "you", project: "moa",
    desc: "", status: "open", age: "1d", subs: [], blockedBy: [],
  },
  {
    id: "t6", title: "Volver a emparejar el iPhone", place: "you", project: null,
    desc: "", status: "open", age: "1d", subs: [], blockedBy: [],
  },
  {
    id: "t7", title: "Alarma de disco al 85 %", place: "backlog", project: "moa",
    desc: "El disco se llenó el 28/09 y falló el emparejamiento del iPhone.",
    status: "open", age: "1d",
    subs: [
      { t: "Script que mida el uso de /", done: false },
      { t: "Timer de systemd cada 15 min", done: false },
      { t: "Aviso por Pulse", done: false },
    ],
    blockedBy: [],
  },
  {
    id: "t8", title: "El orden de «más reciente» es falso tras reiniciar", place: "backlog", project: "moa",
    desc: "", status: "open", age: "4d", subs: [], blockedBy: [],
  },
  {
    id: "t9", title: "Exportar la trazabilidad a PDF por lote", place: "backlog", project: "winerim",
    desc: "", status: "open", age: "2d", subs: [], blockedBy: ["t2"],
  },
  {
    id: "t9b", title: "Optimizar las imágenes de la galería", place: "backlog", project: "ourown",
    desc: "", status: "open", age: "5d", subs: [], blockedBy: [],
  },
  {
    id: "t11", title: "Escribir el workflow de deploy", place: "session", session: "pulse", project: "moa",
    desc: "", status: "done", age: "40m", subs: [], blockedBy: [],
  },
  {
    id: "t12", title: "Configurar el job de deploy en GitHub Actions", place: "session", session: "pulse", project: "moa",
    desc: "", status: "working", age: "20m",
    subs: [{ t: "Job con environment staging", done: true }, { t: "Cache de módulos Go", done: false }],
    blockedBy: [],
  },
  {
    id: "t10", title: "Probar el despliegue en staging", place: "session", session: "pulse", project: "moa",
    desc: "", status: "open", age: "20m", subs: [], blockedBy: ["t1"],
  },
  {
    id: "t13", title: "Reproducir con 100 borrados en paralelo", place: "session", session: "race", project: "moa",
    desc: "", status: "done", age: "1h", subs: [], blockedBy: [],
  },
  {
    id: "t14", title: "Mover la comprobación dentro del lock", place: "session", session: "race", project: "moa",
    desc: "", status: "working", age: "30m", subs: [], blockedBy: [],
  },
  {
    id: "t15", title: "Pasar go vet ./...", place: "session", session: "race", project: "moa",
    desc: "", status: "open", age: "30m", subs: [], blockedBy: ["t14"],
  },
  {
    id: "t16", title: "Normalizar las líneas del CSV de Torres", place: "session", session: "torres", project: "winerim",
    desc: "", status: "done", age: "2h", subs: [], blockedBy: [],
  },
  {
    id: "t17", title: "Conciliar contra el conteo de Marta", place: "session", session: "torres", project: "winerim",
    desc: "", status: "open", age: "2h", subs: [], blockedBy: ["t2"],
  },
  {
    id: "t18", title: "Renovar el token de Sentry", place: "you", from: "checkout", project: "ourown",
    desc: "", status: "done", age: "5h", doneNote: "Nuevo token en 1Password, «sentry-ourown».", subs: [], blockedBy: [],
  },
];

const cloneSeed = () => SEED.map((t) => ({ ...t, subs: t.subs.map((s) => ({ ...s })), blockedBy: [...t.blockedBy] }));

// ── Model ───────────────────────────────────────────────────────────────

const isRequest = (t) => t.place === "you" && !!t.from;
const isOpen = (t) => t.status !== "done";

function placeLabel(t, variant = "b") {
  if (t.place === "you") return variant === "a" ? (t.from ? "For you" : "Notes") : "You";
  if (t.place === "backlog") return `Backlog · ${PROJECTS[t.project]?.name}`;
  return SESSIONS[t.session]?.name || "A session";
}

// The session that hears about a change to this task: the one it is assigned
// to, or the one that asked for it. Notes and backlog tell nobody.
function listener(t) {
  if (t.place === "session") return SESSIONS[t.session];
  if (isRequest(t)) return SESSIONS[t.from];
  return null;
}

function useTaskStore(mutate) {
  const [tasks, setTasks] = useState(() => {
    const seed = cloneSeed();
    return mutate ? mutate(seed) : seed;
  });
  const patch = (id, p) => setTasks((ts) => ts.map((t) => (t.id === id ? { ...t, ...p } : t)));
  return {
    tasks,
    byId: (id) => tasks.find((t) => t.id === id),
    patch,
    complete: (id, note) => patch(id, { status: "done", doneNote: note || "", age: "now", isNew: false }),
    reopen: (id) => patch(id, { status: "open", doneNote: "" }),
    move: (id, place, target) =>
      patch(id, place === "session"
        ? { place, session: target, project: SESSIONS[target].project, from: undefined }
        : place === "backlog"
          ? { place, project: target, session: undefined, from: undefined }
          : { place: "you", session: undefined }),
    add: (t) => setTasks((ts) => [{ ...t }, ...ts]),
    remove: (id) => setTasks((ts) => ts.filter((t) => t.id !== id)),
  };
}

// Sections of the global list. Same order in both variants; only whether the
// requests and the notes are one list or two changes.
function sections(tasks, { variant, agents, project }) {
  const scoped = tasks.filter((t) => !project || t.project === project);
  const open = scoped.filter(isOpen);
  const out = [];
  const reqFirst = (a, b) => (isRequest(b) ? 1 : 0) - (isRequest(a) ? 1 : 0);
  if (variant === "a") {
    out.push({ id: "foryou", title: "For you", drop: null, rows: open.filter(isRequest) });
    out.push({ id: "notes", title: "Notes", drop: { place: "you" }, rows: open.filter((t) => t.place === "you" && !t.from) });
  } else {
    out.push({ id: "you", title: "You", drop: { place: "you" }, rows: open.filter((t) => t.place === "you").sort(reqFirst) });
  }
  const projects = project ? [project] : Object.keys(PROJECTS);
  out.push({
    id: "backlog",
    title: "Backlog",
    groups: projects.map((p) => ({
      id: `bl-${p}`,
      title: PROJECTS[p].name,
      drop: { place: "backlog", target: p },
      rows: open.filter((t) => t.place === "backlog" && t.project === p),
    })),
  });
  if (agents) {
    const sess = Object.values(SESSIONS).filter((s) => !project || s.project === project);
    out.push({
      id: "agents",
      title: "Agents",
      groups: sess
        .map((s) => ({
          id: `ag-${s.id}`,
          title: s.name,
          sub: PROJECTS[s.project].name,
          state: s.state,
          drop: { place: "session", target: s.id },
          rows: scoped.filter((t) => t.place === "session" && t.session === s.id),
        }))
        .filter((g) => g.rows.length > 0),
    });
  }
  const done = scoped.filter((t) => !isOpen(t) && t.place !== "session");
  out.push({ id: "done", title: "Done", collapsed: true, rows: done });
  return out;
}

function countForYou(tasks) {
  return tasks.filter((t) => isRequest(t) && isOpen(t)).length;
}

// ── Atoms ───────────────────────────────────────────────────────────────

function Circle({ task, onClick, size = 20 }) {
  const done = task.status === "done";
  return (
    <button
      type="button"
      class={`tk-circle${done ? " is-done" : ""}${task.status === "working" ? " is-working" : ""}`}
      style={{ width: `${size}px`, height: `${size}px` }}
      aria-label={done ? `Reopen ${task.title}` : `Mark ${task.title} done`}
      onClick={(e) => { e.stopPropagation(); onClick?.(); }}
    >
      <Check size={12} strokeWidth={3} aria-hidden="true" />
    </button>
  );
}

function Meta({ task, variant, context }) {
  const bits = [];
  const blocker = task.blockedBy?.length ? task._store?.byId(task.blockedBy[0]) : null;
  if (task.status === "done" && task.doneNote) bits.push(<span class="tk-meta-note">“{task.doneNote}”</span>);
  if (task.status === "working") bits.push(<span class="tk-word-working">Working</span>);
  if (blocker && isOpen(blocker) && isOpen(task)) {
    bits.push(<span class="tk-meta-blocked"><Link2 size={11} aria-hidden="true" /><span class="tk-ellip">Waits for {blocker.title}</span></span>);
  }
  if (isRequest(task) && context !== "session") {
    bits.push(<span class="tk-meta-from">{SESSIONS[task.from].name}</span>);
  }
  if (task.project && context !== "session" && context !== "project" && !(task.place === "session")) {
    bits.push(<span>{PROJECTS[task.project].name}</span>);
  }
  if (task.subs?.length) {
    const d = task.subs.filter((s) => s.done).length;
    bits.push(<span class="tk-mono">{d}/{task.subs.length}</span>);
  }
  if (!bits.length) return null;
  return (
    <span class="tk-meta">
      {bits.map((b, i) => <>{i > 0 && <span class="tk-dot" aria-hidden="true">·</span>}{b}</>)}
    </span>
  );
}

// ── Completing a request: the row opens in place ─────────────────────────

function CompleteForm({ task, onDone, onCancel, autoFocus = true, value: initial = "" }) {
  const [note, setNote] = useState(initial);
  const ref = useRef(null);
  useEffect(() => { if (autoFocus) ref.current?.focus(); }, []);
  const who = SESSIONS[task.from];
  return (
    <div class="tk-complete" onClick={(e) => e.stopPropagation()}>
      <label class="tk-complete-label" for={`note-${task.id}`}>Note to {who.name}</label>
      <textarea
        id={`note-${task.id}`}
        ref={ref}
        class="tk-field tk-complete-note"
        rows={2}
        placeholder="Optional"
        value={note}
        onInput={(e) => setNote(e.currentTarget.value)}
      />
      <div class="tk-complete-actions">
        <button type="button" class="tk-btn is-ghost" onClick={onCancel}>Cancel</button>
        <button type="button" class="tk-btn is-primary" onClick={() => onDone(note)}>
          <Check size={14} strokeWidth={2.5} aria-hidden="true" /> Done
        </button>
      </div>
    </div>
  );
}

// ── A row ────────────────────────────────────────────────────────────────

function TaskRow({ task, store, variant, context, selected, onOpen, completing, setCompleting, dragging, onDragStart, onDragEnd, touch }) {
  const t = { ...task, _store: store };
  const open = completing === task.id;
  const toggle = () => {
    if (task.status === "done") return store.reopen(task.id);
    if (isRequest(task)) return setCompleting(open ? null : task.id);
    store.complete(task.id);
  };
  return (
    <div
      class={`tk-row${selected === task.id ? " is-selected" : ""}${task.status === "done" ? " is-done" : ""}${open ? " is-completing" : ""}${dragging === task.id ? " is-dragging" : ""}${touch ? " is-touch" : ""}`}
      draggable={!touch && !open}
      onDragStart={(e) => { e.dataTransfer.setData("text/plain", task.id); e.dataTransfer.effectAllowed = "move"; onDragStart?.(task.id); }}
      onDragEnd={() => onDragEnd?.()}
      onClick={() => !open && onOpen?.(task.id)}
      role="button"
      tabIndex={0}
    >
      <div class="tk-row-line">
        <Circle task={task} onClick={toggle} />
        <div class="tk-row-main">
          <span class="tk-row-title">
            {task.isNew && isOpen(task) && <span class="tk-new" aria-label="New" />}
            {task.title}
          </span>
          <Meta task={t} variant={variant} context={context} />
        </div>
        <span class="tk-row-age tk-mono">{task.age}</span>
        {touch && !open && <ChevronRight class="tk-row-chev" size={16} aria-hidden="true" />}
      </div>
      {open && (
        <CompleteForm
          task={task}
          onCancel={() => setCompleting(null)}
          onDone={(note) => { store.complete(task.id, note); setCompleting(null); }}
        />
      )}
    </div>
  );
}

// ── The list ─────────────────────────────────────────────────────────────

function DropHead({ drop, children, onDrop, dragging, forceOver, class: cls = "" }) {
  const [over, setOver] = useState(false);
  const live = !!drop && (!!dragging || forceOver);
  const hot = forceOver || over;
  return (
    <div
      class={`${cls}${live ? " is-droppable" : ""}${live && hot ? " is-over" : ""}`}
      onDragOver={(e) => { if (drop && dragging) { e.preventDefault(); setOver(true); } }}
      onDragLeave={() => setOver(false)}
      onDrop={(e) => { e.preventDefault(); setOver(false); onDrop?.(drop); }}
    >
      {children}
      {live && hot && <span class="tk-drop-hint">{dropHint(drop)}</span>}
    </div>
  );
}

function dropHint(drop) {
  if (drop.place === "you") return "Take it back";
  if (drop.place === "backlog") return `Send to backlog`;
  return `Assign · tells it now`;
}

function TaskList({ store, variant, agents, project, selected, onOpen, completing, setCompleting, touch, demoDrag, showDone, onNew }) {
  const [dragging, setDragging] = useState(demoDrag?.task || null);
  const [doneOpen, setDoneOpen] = useState(!!showDone);
  const secs = sections(store.tasks, { variant, agents, project });
  const onDrop = (drop) => {
    if (!dragging) return;
    store.move(dragging, drop.place, drop.target);
    setDragging(null);
  };
  const rowProps = { store, variant, selected, onOpen, completing, setCompleting, dragging, touch, onDragStart: setDragging, onDragEnd: () => setDragging(null) };
  const nothing = secs.every((s) => s.id === "done" || (s.rows ? s.rows.length === 0 : s.groups.every((g) => g.rows.length === 0)));
  if (nothing) {
    return (
      <div class="tk-empty">
        <p class="tk-empty-title">Nothing pending.</p>
        {onNew && <button type="button" class="tk-btn" onClick={onNew}><Plus size={14} aria-hidden="true" /> New task</button>}
      </div>
    );
  }
  return (
    <div class="tk-list">
      {secs.map((s) => {
        if (s.id === "done") {
          if (!s.rows.length) return null;
          return (
            <section class="tk-sec is-done" key={s.id}>
              <button type="button" class="tk-sec-head is-toggle" onClick={() => setDoneOpen(!doneOpen)} aria-expanded={doneOpen}>
                <ChevronDown size={14} class={`tk-chev${doneOpen ? "" : " is-closed"}`} aria-hidden="true" />
                <span>Done</span><span class="tk-sec-n">{s.rows.length}</span>
              </button>
              {doneOpen && s.rows.map((t) => <TaskRow key={t.id} task={t} {...rowProps} />)}
            </section>
          );
        }
        if (s.rows) {
          if (!s.rows.length && (s.id === "foryou" || !dragging)) return null;
          return (
            <section class="tk-sec" key={s.id}>
              <DropHead class="tk-sec-head" drop={s.drop} onDrop={onDrop} dragging={dragging} forceOver={demoDrag?.over === s.id}>
                <span>{s.title}</span><span class="tk-sec-n">{s.rows.length || ""}</span>
              </DropHead>
              {s.rows.length === 0 && <p class="tk-quiet">Drop here</p>}
              {s.rows.map((t, i) => (
                <>
                  {s.id === "you" && i > 0 && isRequest(s.rows[i - 1]) && !isRequest(t) && <div class="tk-split" aria-hidden="true" />}
                  <TaskRow key={t.id} task={t} {...rowProps} />
                </>
              ))}
            </section>
          );
        }
        const groups = s.groups.filter((g) => g.rows.length || dragging);
        if (!groups.length) return null;
        return (
          <section class="tk-sec" key={s.id}>
            <div class="tk-sec-head"><span>{s.title}</span></div>
            {groups.map((g) => (
              <div class="tk-group" key={g.id}>
                <DropHead class="tk-group-head" drop={g.drop} onDrop={onDrop} dragging={dragging} forceOver={demoDrag?.over === g.id}>
                  <span class="tk-group-name">{g.title}</span>
                  {g.state && <span class="tk-word-working">{g.state}</span>}
                  {g.sub && !project && <span class="tk-group-sub">{g.sub}</span>}
                </DropHead>
                {g.rows.map((t) => <TaskRow key={t.id} task={t} {...rowProps} context={s.id === "backlog" ? "project" : "session"} />)}
              </div>
            ))}
          </section>
        );
      })}
    </div>
  );
}

function ListHead({ agents, setAgents, project, setProject, onNew, touch }) {
  const [menu, setMenu] = useState(false);
  return (
    <div class="tk-filters">
      <div class="tk-select-wrap">
        <button type="button" class="tk-chip" onClick={() => setMenu(!menu)} aria-expanded={menu}>
          {project ? PROJECTS[project].name : "All projects"} <ChevronDown size={13} aria-hidden="true" />
        </button>
        {menu && (
          <div class="tk-pop is-small" role="menu">
            {[null, ...Object.keys(PROJECTS)].map((p) => (
              <button type="button" role="menuitemradio" aria-checked={project === p} class={`tk-pop-item${project === p ? " is-on" : ""}`} onClick={() => { setProject(p); setMenu(false); }}>
                {p ? PROJECTS[p].name : "All projects"}
                {project === p && <Check size={14} aria-hidden="true" />}
              </button>
            ))}
          </div>
        )}
      </div>
      <button type="button" class={`tk-chip${agents ? " is-on" : ""}`} aria-pressed={agents} onClick={() => setAgents(!agents)}>
        Agents' tasks
      </button>
      {!touch && onNew && (
        <button type="button" class="tk-btn is-primary tk-new-btn" onClick={onNew}>
          <Plus size={14} strokeWidth={2.5} aria-hidden="true" /> New task
        </button>
      )}
    </div>
  );
}

// ── Move ─────────────────────────────────────────────────────────────────

function MoveTargets({ task, onPick, pending, setPending, compact, variant }) {
  const [q, setQ] = useState("");
  const sess = Object.values(SESSIONS).filter((s) => !q || s.name.toLowerCase().includes(q.toLowerCase()));
  const here = (place, target) =>
    task.place === place && (place === "you" || (place === "backlog" ? task.project === target : task.session === target));
  return (
    <div class={`tk-move${compact ? " is-compact" : ""}`}>
      <button type="button" class={`tk-pop-item${here("you") ? " is-on" : ""}`} onClick={() => onPick("you")}>
        <span class="tk-move-name">{variant === "a" ? "Notes" : "You"}</span>
        {here("you") && <Check size={14} aria-hidden="true" />}
      </button>
      <div class="tk-pop-label">Backlog</div>
      {Object.values(PROJECTS).map((p) => (
        <button type="button" class={`tk-pop-item${here("backlog", p.id) ? " is-on" : ""}`} onClick={() => onPick("backlog", p.id)}>
          <span class="tk-move-name">{p.name}</span>
          {here("backlog", p.id) && <Check size={14} aria-hidden="true" />}
        </button>
      ))}
      <div class="tk-pop-label">A session</div>
      <label class="tk-search">
        <Search size={14} aria-hidden="true" />
        <input class="tk-field" type="text" placeholder="Find a session" value={q} onInput={(e) => setQ(e.currentTarget.value)} />
      </label>
      {sess.map((s) => (
        <button type="button" class={`tk-pop-item is-two${pending === s.id ? " is-pending" : ""}${here("session", s.id) ? " is-on" : ""}`} onClick={() => setPending(s.id)}>
          <span class="tk-move-name">{s.name}</span>
          <span class="tk-move-sub">{PROJECTS[s.project].name}{s.state && <> · <span class="tk-word-working">{s.state}</span></>}</span>
        </button>
      ))}
      {pending && (
        <div class="tk-move-confirm">
          <button type="button" class="tk-btn is-primary is-wide" onClick={() => onPick("session", pending)}>
            Assign to {SESSIONS[pending].name}
          </button>
        </div>
      )}
    </div>
  );
}

// ── Detail / editor ──────────────────────────────────────────────────────

function Detail({ task, store, variant, onClose, onOpen, touch, startMenu, startCompleting, startDirty, isNew, onCreated, newPlace, onSubpage }) {
  const blank = { title: "", desc: "", subs: [], blockedBy: [] };
  const base = isNew ? blank : task;
  const [draft, setDraft] = useState(() => startDirty ? { ...base, ...startDirty } : { ...base, subs: base.subs.map((s) => ({ ...s })) });
  const [menu, setMenu] = useState(!!startMenu);
  const [pending, setPending] = useState(startMenu?.pending || null);
  const [completing, setCompleting] = useState(!!startCompleting);
  const [addingSub, setAddingSub] = useState("");
  const [depMenu, setDepMenu] = useState(false);
  const [place, setPlace] = useState(newPlace || { place: "you" });
  const [placeMenu, setPlaceMenu] = useState(false);

  const shownId = useRef(task?.id);
  useEffect(() => {
    if (isNew || shownId.current === task?.id) return;
    shownId.current = task?.id;
    setDraft({ ...task, subs: task.subs.map((s) => ({ ...s })) });
    setMenu(false); setCompleting(false); setPending(null);
  }, [task?.id]);

  useEffect(() => { onSubpage?.(!!(touch && menu && !isNew)); }, [menu]);
  if (!isNew && !task) return null;
  const dirty = !isNew && (draft.title !== task.title || draft.desc !== task.desc || JSON.stringify(draft.subs) !== JSON.stringify(task.subs) || JSON.stringify(draft.blockedBy) !== JSON.stringify(task.blockedBy));
  const who = isNew ? (place.place === "session" ? SESSIONS[place.target] : null) : listener(task);
  const blockers = (draft.blockedBy || []).map(store.byId).filter(Boolean);
  const blocks = isNew ? [] : store.tasks.filter((t) => t.blockedBy?.includes(task.id));
  const save = () => store.patch(task.id, { title: draft.title, desc: draft.desc, subs: draft.subs, blockedBy: draft.blockedBy });
  const discard = () => setDraft({ ...task, subs: task.subs.map((s) => ({ ...s })) });
  const pick = (p, target) => { store.move(task.id, p, target); setMenu(false); setPending(null); };
  const done = !isNew && task.status === "done";
  const setSub = (i, p) => setDraft({ ...draft, subs: draft.subs.map((s, j) => (j === i ? { ...s, ...p } : s)) });
  const candidates = store.tasks.filter((t) => isOpen(t) && t.id !== task?.id && !(draft.blockedBy || []).includes(t.id)).slice(0, 6);

  const create = () => {
    if (!draft.title.trim()) return;
    const id = `n${Date.now()}`;
    const t = {
      id, title: draft.title, desc: draft.desc, subs: draft.subs, blockedBy: draft.blockedBy, status: "open", age: "now",
      place: place.place,
      project: place.place === "backlog" ? place.target : place.place === "session" ? SESSIONS[place.target].project : null,
      session: place.place === "session" ? place.target : undefined,
    };
    store.add(t);
    onCreated?.(id);
  };

  if (touch && menu && !isNew) {
    return (
      <div class="tk-detail is-touch is-move">
        <PhoneHead title="Move to" onBack={() => { setMenu(false); setPending(null); }} backLabel="Task" />
        <div class="tk-detail-scroll">
          <MoveTargets variant={variant} task={task} pending={pending} setPending={setPending} onPick={pick} compact />
        </div>
      </div>
    );
  }

  const placeText = isNew
    ? place.place === "you" ? (variant === "a" ? "Notes" : "You") : place.place === "backlog" ? `Backlog · ${PROJECTS[place.target].name}` : SESSIONS[place.target].name
    : placeLabel(task, variant);

  return (
    <div class={`tk-detail${touch ? " is-touch" : ""}`}>
      <div class="tk-detail-scroll">
        <div class="tk-detail-place">
          <div class="tk-select-wrap">
            <button
              type="button"
              class="tk-chip is-place"
              aria-expanded={isNew ? placeMenu : menu}
              onClick={() => (isNew ? setPlaceMenu(!placeMenu) : setMenu(!menu))}
            >
              <MoveRight size={13} aria-hidden="true" /> {placeText} <ChevronDown size={13} aria-hidden="true" />
            </button>
            {!touch && (isNew ? placeMenu : menu) && (
              <div class="tk-pop" role="menu">
                <MoveTargets
                  variant={variant}
                  task={isNew ? { place: place.place, project: place.target, session: place.target } : task}
                  pending={pending}
                  setPending={isNew ? (s) => { setPlace({ place: "session", target: s }); setPlaceMenu(false); } : setPending}
                  onPick={isNew ? (p, target) => { setPlace({ place: p, target }); setPlaceMenu(false); } : pick}
                />
              </div>
            )}
          </div>
          {!isNew && isRequest(task) && (
            <span class="tk-detail-from">
              asked by <a href="#" onClick={(e) => e.preventDefault()}>{SESSIONS[task.from].name}</a> · {task.age}
            </span>
          )}
          {!isNew && !touch && onClose && (
            <button type="button" class="tk-icon-btn" aria-label="Close" onClick={onClose}><X size={16} aria-hidden="true" /></button>
          )}
        </div>


        <textarea
          class={`tk-field tk-detail-title${done ? " is-done" : ""}`}
          rows={1}
          value={draft.title}
          placeholder="What needs doing?"
          aria-label="Title"
          onInput={(e) => { setDraft({ ...draft, title: e.currentTarget.value }); e.currentTarget.style.height = "auto"; e.currentTarget.style.height = `${e.currentTarget.scrollHeight}px`; }}
          ref={(el) => { if (el) { el.style.height = "auto"; el.style.height = `${el.scrollHeight}px`; if (isNew && !el.dataset.f) { el.dataset.f = "1"; el.focus(); } } }}
        />
        <textarea
          class="tk-field tk-detail-desc"
          rows={draft.desc ? 3 : 1}
          value={draft.desc}
          placeholder="Add details"
          aria-label="Details"
          onInput={(e) => setDraft({ ...draft, desc: e.currentTarget.value })}
        />

        {done && task.doneNote && (
          <div class="tk-done-note">
            <span class="tk-block-label">Your note</span>
            <p>{task.doneNote}</p>
          </div>
        )}

        <div class="tk-block">
          <div class="tk-block-label">
            Subtasks {draft.subs.length > 0 && <span class="tk-mono">{draft.subs.filter((s) => s.done).length}/{draft.subs.length}</span>}
          </div>
          {draft.subs.map((s, i) => (
            <div class={`tk-sub${s.done ? " is-done" : ""}`}>
              <button type="button" class={`tk-circle is-small${s.done ? " is-done" : ""}`} aria-label={s.done ? "Reopen subtask" : "Complete subtask"} onClick={() => setSub(i, { done: !s.done })}>
                <Check size={10} strokeWidth={3} aria-hidden="true" />
              </button>
              <input class="tk-field tk-sub-text" value={s.t} onInput={(e) => setSub(i, { t: e.currentTarget.value })} aria-label="Subtask" />
              <button type="button" class="tk-icon-btn is-quiet" aria-label="Remove subtask" onClick={() => setDraft({ ...draft, subs: draft.subs.filter((_, j) => j !== i) })}>
                <X size={14} aria-hidden="true" />
              </button>
            </div>
          ))}
          <div class="tk-sub is-add">
            <Plus size={14} class="tk-sub-plus" aria-hidden="true" />
            <input
              class="tk-field tk-sub-text"
              placeholder="Add a subtask"
              value={addingSub}
              onInput={(e) => setAddingSub(e.currentTarget.value)}
              onKeyDown={(e) => { if (e.key === "Enter" && addingSub.trim()) { setDraft({ ...draft, subs: [...draft.subs, { t: addingSub.trim(), done: false }] }); setAddingSub(""); } }}
            />
          </div>
        </div>

        <div class="tk-block">
          <div class="tk-block-label">Waits for</div>
          {blockers.map((b) => (
            <div class="tk-dep">
              <span class={`tk-dep-state${isOpen(b) ? "" : " is-done"}`}>{isOpen(b) ? "Open" : "Done"}</span>
              <button type="button" class="tk-dep-title" onClick={() => onOpen?.(b.id)}>{b.title}</button>
              <span class="tk-dep-where">{placeLabel(b)}</span>
              <button type="button" class="tk-icon-btn is-quiet" aria-label="Remove" onClick={() => setDraft({ ...draft, blockedBy: draft.blockedBy.filter((x) => x !== b.id) })}>
                <X size={14} aria-hidden="true" />
              </button>
            </div>
          ))}
          <div class="tk-select-wrap">
            <button type="button" class="tk-add-link" onClick={() => setDepMenu(!depMenu)}>
              <Plus size={14} aria-hidden="true" /> Add a task it waits for
            </button>
            {depMenu && (
              <div class="tk-pop is-dep" role="menu">
                <label class="tk-search"><Search size={14} aria-hidden="true" /><input class="tk-field" placeholder="Find a task" /></label>
                {candidates.map((c) => (
                  <button type="button" class="tk-pop-item is-two" onClick={() => { setDraft({ ...draft, blockedBy: [...(draft.blockedBy || []), c.id] }); setDepMenu(false); }}>
                    <span class="tk-move-name">{c.title}</span>
                    <span class="tk-move-sub">{placeLabel(c)}</span>
                  </button>
                ))}
              </div>
            )}
          </div>
          {blocks.length > 0 && (
            <>
              <div class="tk-block-label is-sub">Unblocks</div>
              {blocks.map((b) => (
                <div class="tk-dep">
                  <span class={`tk-dep-state${isOpen(b) ? "" : " is-done"}`}>{isOpen(b) ? "Open" : "Done"}</span>
                  <button type="button" class="tk-dep-title" onClick={() => onOpen?.(b.id)}>{b.title}</button>
                  <span class="tk-dep-where">{placeLabel(b)}</span>
                </div>
              ))}
            </>
          )}
        </div>
      </div>

      <div class="tk-detail-foot">
        {isNew ? (
          <>
            <span class="tk-foot-hint" />
            <button type="button" class="tk-btn is-primary" disabled={!draft.title.trim()} onClick={create}>
              {place.place === "session" ? `Assign to ${SESSIONS[place.target].name}` : place.place === "backlog" ? "Add to backlog" : "Add"}
            </button>
          </>
        ) : dirty ? (
          <>
            <button type="button" class="tk-btn is-ghost" onClick={discard}>Discard</button>
            <span class="tk-foot-grow" />
            <button type="button" class={`tk-btn${who ? "" : " is-primary"}`} onClick={save}>Save</button>
            {who && (
              <button type="button" class="tk-btn is-primary" onClick={save} title={`Tells ${who.name}`}>Save and notify</button>
            )}
          </>
        ) : completing ? (
          <div class="tk-foot-complete">
            <CompleteForm task={task} autoFocus onCancel={() => setCompleting(false)} onDone={(note) => { store.complete(task.id, note); setCompleting(false); }} />
          </div>
        ) : done ? (
          <>
            <span class="tk-foot-hint">Done {task.age === "now" ? "just now" : `${task.age} ago`}</span>
            <span class="tk-foot-grow" />
            <button type="button" class="tk-btn" onClick={() => store.reopen(task.id)}>Reopen</button>
          </>
        ) : (
          <>
            <button type="button" class="tk-icon-btn is-quiet" aria-label="Delete task" onClick={() => { store.remove(task.id); onClose?.(); }}><Trash2 size={15} aria-hidden="true" /></button>
            <span class="tk-foot-grow" />
            {touch && <button type="button" class="tk-btn" onClick={() => setMenu(!menu)}>Move</button>}
            <button type="button" class="tk-btn is-primary" onClick={() => (isRequest(task) ? setCompleting(true) : store.complete(task.id))}>
              <Check size={14} strokeWidth={2.5} aria-hidden="true" /> Mark done
            </button>
          </>
        )}
      </div>
    </div>
  );
}

// ── Global view ──────────────────────────────────────────────────────────

function GlobalDesktop({ store, variant, init = {} }) {
  const [sel, setSel] = useState(init.selected || null);
  const [creating, setCreating] = useState(!!init.creating);
  const [agents, setAgents] = useState(!!init.agents);
  const [project, setProject] = useState(init.project || null);
  const [completing, setCompleting] = useState(init.completing || null);
  const task = sel ? store.byId(sel) : null;
  const listRef = useRef(null);
  useEffect(() => {
    const list = listRef.current?.querySelector(".tk-list");
    const row = list?.querySelector(".tk-row.is-selected");
    if (list && row && row.offsetTop > list.clientHeight - 80) list.scrollTop = row.offsetTop - 140;
  }, []);
  return (
    <div class="tk-global">
      <div class="tk-global-list" ref={listRef}>
        <div class="tk-page-head">
          <h2 class="tk-page-title">Tasks</h2>
        </div>
        <ListHead agents={agents} setAgents={setAgents} project={project} setProject={setProject} onNew={() => { setCreating(true); setSel(null); }} />
        <TaskList
          store={store} variant={variant} agents={agents} project={project}
          selected={sel} onOpen={(id) => { setSel(id); setCreating(false); }}
          completing={completing} setCompleting={setCompleting}
          demoDrag={init.demoDrag} showDone={init.showDone}
        />
      </div>
      <div class="tk-global-detail">
        {creating ? (
          <Detail isNew store={store} variant={variant} newPlace={init.newPlace} onCreated={(id) => { setCreating(false); setSel(id); }} />
        ) : task ? (
          <Detail task={task} store={store} variant={variant} onClose={() => setSel(null)} onOpen={setSel}
            startMenu={init.menu} startCompleting={init.detailCompleting} startDirty={init.dirty} />
        ) : (
          <div class="tk-detail-empty">
            {store.tasks.some(isOpen) && <><ListTodo size={22} aria-hidden="true" /><p>Pick a task, or drag it to another list.</p></>}
          </div>
        )}
      </div>
    </div>
  );
}

// ── Desktop chrome (drawn by the lab) ────────────────────────────────────

function SideRow({ title, sub, age, state, active }) {
  return (
    <div class={`tk-side-row${active ? " is-active" : ""}`}>
      <div class="tk-side-row-l1"><span class="tk-side-row-title">{title}</span><span class="tk-mono tk-side-age">{age}</span></div>
      <div class={`tk-side-row-sub${state ? ` is-${state}` : ""}`}>{sub}</div>
    </div>
  );
}

function Sidebar({ tasksActive, count, inbox = 2, activeSession }) {
  return (
    <aside class="tk-side">
      <div class="tk-side-top">
        <span class="tk-brand">moa</span>
        <span class="tk-side-icon"><Search size={15} aria-hidden="true" /></span>
        <span class="tk-side-icon"><Plus size={16} aria-hidden="true" /></span>
      </div>
      <div class="tk-side-scroll">
        <div class="tk-side-sec">Owners <span class="tk-mono">3</span></div>
        <div class="tk-owner"><span class="tk-owner-av" style={{ background: "#f5a97f" }}>w</span><div><div class="tk-side-row-title">Winerim</div><div class="tk-side-row-sub"><span class="is-working">1 working</span></div></div></div>
        <div class="tk-owner"><span class="tk-owner-av" style={{ background: "#b4befe" }}>m</span><div><div class="tk-side-row-title">moa</div><div class="tk-side-row-sub"><span class="is-working">2 working</span></div></div></div>
        <div class="tk-owner"><span class="tk-owner-av" style={{ background: "#94e2d5" }}>o</span><div><div class="tk-side-row-title">OurOwn Studio</div><div class="tk-side-row-sub"><span class="is-working">1 working</span></div></div></div>
        <div class="tk-side-sec">Active <span class="tk-mono">6</span></div>
        <SideRow title={SESSIONS.pulse.name} sub="Working" state="working" age="now" active={activeSession === "pulse"} />
        <SideRow title={SESSIONS.race.name} sub="Working · 3m" state="working" age="3m" />
        <SideRow title={SESSIONS.torres.name} sub="Working" state="working" age="1h" />
        <SideRow title={SESSIONS.checkout.name} sub="Working" state="working" age="2h" />
        <SideRow title={SESSIONS.tarifas.name} sub="~/dev/winerim-backend" age="5h" />
        <SideRow title={SESSIONS.pw.name} sub="~/dev/moa/main" age="1d" />
      </div>
      <SideFoot tasksActive={tasksActive} count={count} inbox={inbox} />
    </aside>
  );
}

function SideFoot({ tasksActive, count, inbox }) {
  return (
    <div class="tk-side-foot">
      <span class="tk-foot-entry"><Inbox size={15} aria-hidden="true" /> Inbox {inbox > 0 && <span class="tk-pill-attn">{inbox}</span>}</span>
      <span class={`tk-foot-entry${tasksActive ? " is-active" : ""}`}>
        <ListTodo size={15} aria-hidden="true" /> Tasks {count > 0 && <span class="tk-count">{count}</span>}
      </span>
      <span class="tk-side-icon tk-gear"><Settings size={15} aria-hidden="true" /></span>
    </div>
  );
}

function DesktopFrame({ children, label, sidebar }) {
  return (
    <figure class="tk-frame-wrap">
      <figcaption class="tk-caption">{label}</figcaption>
      <div class="tk-frame is-desktop">
        {sidebar}
        <main class="tk-main">{children}</main>
      </div>
    </figure>
  );
}

function PhoneFrame({ children, label }) {
  return (
    <figure class="tk-frame-wrap">
      <figcaption class="tk-caption">{label}</figcaption>
      <div class="tk-frame is-phone">{children}</div>
    </figure>
  );
}

// ── Phone pages ──────────────────────────────────────────────────────────

function PhoneHead({ title, onBack, backLabel, right }) {
  return (
    <div class="tk-ph-head">
      {onBack ? (
        <button type="button" class="tk-ph-back" onClick={onBack} aria-label={`Back to ${backLabel || "list"}`}>
          <ChevronLeft size={20} aria-hidden="true" />{backLabel && <span>{backLabel}</span>}
        </button>
      ) : <span class="tk-ph-back is-ghost" />}
      <span class="tk-ph-title">{title}</span>
      <span class="tk-ph-right">{right}</span>
    </div>
  );
}

function GlobalPhone({ store, variant, init = {} }) {
  const [page, setPage] = useState(init.page || "list");
  const [sel, setSel] = useState(init.selected || null);
  const [agents, setAgents] = useState(!!init.agents);
  const [project, setProject] = useState(null);
  const [completing, setCompleting] = useState(init.completing || null);
  const [sub, setSub] = useState(false);
  const task = sel ? store.byId(sel) : null;

  if (page === "new") {
    return (
      <div class="tk-ph">
        <PhoneHead title="New task" onBack={() => setPage("list")} backLabel="Tasks" />
        <Detail isNew touch store={store} variant={variant} newPlace={init.newPlace} onCreated={(id) => { setSel(id); setPage("detail"); }} />
      </div>
    );
  }
  if (page === "detail" && task) {
    return (
      <div class="tk-ph">
        {!sub && <PhoneHead title="" onBack={() => setPage("list")} backLabel="Tasks" />}
        <Detail task={task} touch store={store} variant={variant} onOpen={setSel} onClose={() => setPage("list")} onSubpage={setSub}
          startMenu={init.menu} startCompleting={init.detailCompleting} startDirty={init.dirty} />
      </div>
    );
  }
  return (
    <div class="tk-ph">
      <PhoneHead
        title="Tasks"
        onBack={() => {}}
        right={<button type="button" class="tk-ph-plus" aria-label="New task" onClick={() => setPage("new")}><Plus size={20} aria-hidden="true" /></button>}
      />
      <div class="tk-ph-body">
        <ListHead touch agents={agents} setAgents={setAgents} project={project} setProject={setProject} />
        <TaskList
          touch store={store} variant={variant} agents={agents} project={project}
          onOpen={(id) => { setSel(id); setPage("detail"); }} onNew={() => setPage("new")}
          completing={completing} setCompleting={setCompleting}
        />
      </div>
    </div>
  );
}

// ── A session: conversation, the pinned request, the panel ──────────────

function AskedCard({ task, onOpen }) {
  const done = task.status === "done";
  return (
    <button type="button" class="tk-asked" onClick={onOpen}>
      <ListTodo size={15} aria-hidden="true" />
      <span class="tk-asked-verb">Asked you</span>
      <span class="tk-asked-title">{task.title}</span>
      <span class={`tk-asked-state${done ? " is-done" : ""}`}>{done ? "Done" : "Open"}</span>
      <ChevronRight size={14} aria-hidden="true" />
    </button>
  );
}

function Conversation({ store, session, onOpenTask, phone, onPanel }) {
  const req = store.tasks.filter((t) => isRequest(t) && t.from === session && isOpen(t));
  const asked = store.byId("t1");
  const done = asked && asked.status === "done";
  const [completing, setCompleting] = useState(false);
  return (
    <div class={`tk-conv${phone ? " is-phone" : ""}`}>
      <div class="tk-conv-head">
        {phone && <span class="tk-ph-menu"><Menu size={18} aria-hidden="true" /></span>}
        <button type="button" class="tk-conv-title" onClick={onPanel}>{SESSIONS[session].name}</button>
        {!phone && <span class="tk-mono tk-conv-path">~/dev/moa/pulse-api</span>}
      </div>
      <div class="tk-conv-body">
        <div class="tk-msg-user">Monta el pipeline de deploy de pulse-api a staging con GitHub Actions.</div>
        <p class="tk-msg">El workflow está escrito y el job de deploy configurado. Para desplegar necesito la clave de deploy en los secrets del repo; mientras tanto sigo con la caché de módulos.</p>
        {asked && <AskedCard task={asked} onOpen={() => onOpenTask?.("t1")} />}
        <div class="tk-tool"><span class="tk-mono">edit</span> <span class="tk-mono tk-tool-path">.github/workflows/deploy.yml</span><Check size={13} class="tk-tool-ok" aria-hidden="true" /></div>
        {done && (
          <div class="tk-event">
            <span class="tk-event-k">You marked it done</span>
            {asked.doneNote && <span class="tk-event-note">“{asked.doneNote}”</span>}
          </div>
        )}
        {done && <p class="tk-msg">Perfecto, lanzo el deploy a staging.</p>}
      </div>
      <div class="tk-conv-foot">
        {req.length > 0 && (
          <div class={`tk-pin${completing ? " is-open" : ""}`}>
            {!completing ? (
              <div class="tk-pin-line">
                <span class="tk-pin-k">For you</span>
                <button type="button" class="tk-pin-title" onClick={() => onOpenTask?.(req[0].id)}>{req[0].title}</button>
                {req.length > 1 && <span class="tk-pin-more">+{req.length - 1}</span>}
                <button type="button" class="tk-btn is-small" onClick={() => setCompleting(true)}>Done</button>
              </div>
            ) : (
              <CompleteForm task={req[0]} onCancel={() => setCompleting(false)} onDone={(note) => { store.complete(req[0].id, note); setCompleting(false); }} />
            )}
          </div>
        )}
        <div class="tk-composer">
          <span class="tk-composer-ph">Message moa</span>
          <span class="tk-composer-send">↑</span>
        </div>
      </div>
    </div>
  );
}

function SessionTasks({ store, session, onOpen, touch, completing, setCompleting, onAdd }) {
  const mine = store.tasks.filter((t) => t.place === "session" && t.session === session);
  const req = store.tasks.filter((t) => isRequest(t) && t.from === session);
  const openReq = req.filter(isOpen);
  const rowProps = { store, variant: "b", context: "session", onOpen, completing, setCompleting, touch };
  if (!mine.length && !req.length) {
    return (
      <div class="tk-empty is-panel">
        <p class="tk-empty-title">No tasks yet.</p>
        <button type="button" class="tk-btn" onClick={onAdd}><Plus size={14} aria-hidden="true" /> Add a task</button>
      </div>
    );
  }
  return (
    <div class="tk-list is-panel">
      {req.length > 0 && (
        <section class="tk-sec">
          <div class="tk-sec-head"><span>For you</span><span class="tk-sec-n">{openReq.length || ""}</span></div>
          {req.map((t) => <TaskRow key={t.id} task={t} {...rowProps} />)}
        </section>
      )}
      <section class="tk-sec">
        <div class="tk-sec-head">
          <span>This session</span>
          <span class="tk-sec-n tk-mono">{mine.filter((t) => !isOpen(t)).length}/{mine.length}</span>
        </div>
        {mine.map((t) => <TaskRow key={t.id} task={t} {...rowProps} />)}
        <button type="button" class="tk-add-link is-row" onClick={onAdd}><Plus size={14} aria-hidden="true" /> Add a task</button>
      </section>
    </div>
  );
}

function PanelHead({ title, onBack }) {
  return (
    <div class="tk-panel-head">
      {onBack && <button type="button" class="tk-icon-btn" onClick={onBack} aria-label="Back"><ChevronLeft size={16} aria-hidden="true" /></button>}
      <span class="tk-panel-title">{title}</span>
    </div>
  );
}

function PanelRoot({ store, session, onPage }) {
  const mine = store.tasks.filter((t) => t.place === "session" && t.session === session);
  const req = store.tasks.filter((t) => isRequest(t) && t.from === session && isOpen(t));
  const verdict = [req.length ? `${req.length} for you` : null, mine.length ? `${mine.filter((t) => !isOpen(t)).length}/${mine.length}` : null].filter(Boolean).join(" · ");
  return (
    <div class="tk-panel-root">
      <div class="tk-panel-facts">
        <span>Tokens</span><span class="tk-mono">↑48k ↓6.1k</span>
        <span>Spend</span><span class="tk-mono">$2.10</span>
        <span>Turns</span><span class="tk-mono">9</span>
      </div>
      <button type="button" class="tk-panel-row" onClick={() => onPage("tasks")}>
        <ListTodo size={16} aria-hidden="true" /><span class="tk-panel-row-name">Tasks</span>
        <span class="tk-panel-row-v">{verdict || "none"}</span><ChevronRight size={14} aria-hidden="true" />
      </button>
      <div class="tk-panel-row is-static"><span class="tk-panel-row-name">Usage</span><span class="tk-panel-row-v">Anthropic · 5h 41%</span><ChevronRight size={14} aria-hidden="true" /></div>
      <div class="tk-panel-row is-static"><span class="tk-panel-row-name">MCP</span><span class="tk-panel-row-v">2 servers</span><ChevronRight size={14} aria-hidden="true" /></div>
      <div class="tk-panel-row is-static"><span class="tk-panel-row-name">Artifacts</span><span class="tk-panel-row-v">3 files</span><ChevronRight size={14} aria-hidden="true" /></div>
    </div>
  );
}

// The panel owns one stack: root → Tasks → a task → (Move). Every level is a
// page of the same panel, never a second sheet.
function SessionPanelBody({ store, session, init = {}, touch }) {
  const [stack, setStack] = useState(init.stack || ["root"]);
  const [completing, setCompleting] = useState(init.completing || null);
  const [sub, setSub] = useState(false);
  const top = stack[stack.length - 1];
  const push = (p) => setStack([...stack, p]);
  const back = () => setStack(stack.slice(0, -1));
  if (top === "root") {
    return <><PanelHead title="This session" /><PanelRoot store={store} session={session} onPage={push} /></>;
  }
  if (top === "tasks") {
    return (
      <>
        <PanelHead title="Tasks" onBack={back} />
        <div class="tk-panel-scroll">
          <SessionTasks store={store} session={session} touch={touch} onOpen={(id) => push(`task:${id}`)} completing={completing} setCompleting={setCompleting} onAdd={() => push("new")} />
        </div>
      </>
    );
  }
  if (top === "new") {
    return (
      <>
        <PanelHead title="New task" onBack={back} />
        <Detail isNew touch={touch} store={store} newPlace={{ place: "session", target: session }} onCreated={() => back()} />
      </>
    );
  }
  const id = top.slice(5);
  const task = store.byId(id);
  return (
    <>
      {!sub && <PanelHead title="" onBack={back} />}
      {task && <Detail task={task} touch store={store} onOpen={(x) => push(`task:${x}`)} onClose={back} onSubpage={setSub} startCompleting={init.detailCompleting} startDirty={init.dirty} />}
    </>
  );
}

function SessionDesktop({ store, init = {} }) {
  const [open, setOpen] = useState(init.panel !== false);
  const panelKey = useRef(0);
  const [stack, setStack] = useState(init.stack || ["root", "tasks"]);
  return (
    <div class="tk-session">
      <Conversation store={store} session="pulse" onPanel={() => setOpen(!open)} onOpenTask={(id) => { setOpen(true); setStack(["root", "tasks", `task:${id}`]); panelKey.current++; }} />
      {open && (
        <aside class="tk-panel">
          <SessionPanelBody key={`${panelKey.current}`} store={store} session="pulse" init={{ ...init, stack }} touch />
        </aside>
      )}
    </div>
  );
}

function SessionPhone({ store, init = {} }) {
  const [sheet, setSheet] = useState(!!init.sheet);
  const [stack, setStack] = useState(init.stack || ["root", "tasks"]);
  const k = useRef(0);
  return (
    <div class="tk-ph is-session">
      <Conversation phone store={store} session="pulse" onPanel={() => { setStack(["root"]); k.current++; setSheet(true); }} onOpenTask={(id) => { setStack(["root", "tasks", `task:${id}`]); k.current++; setSheet(true); }} />
      {sheet && (
        <>
          <div class="tk-veil" onClick={() => setSheet(false)} />
          <div class="tk-sheet">
            <div class="tk-grab" aria-hidden="true" />
            <SessionPanelBody key={k.current} store={store} session="pulse" init={{ ...init, stack }} touch />
          </div>
        </>
      )}
    </div>
  );
}

// ── Scenes ───────────────────────────────────────────────────────────────

const SCENES = [
  {
    id: "global",
    label: "1 · Global view",
    note: "Todas las tareas en una página, al lado del sidebar. Por defecto: lo tuyo y el backlog; lo de los agentes, tras «Agents' tasks». Las peticiones de un agente van arriba con el nombre de la sesión que la pidió. El punto lila = nueva desde tu última visita.",
  },
  {
    id: "agents",
    label: "1b · With agents' tasks",
    note: "El mismo listado con el filtro «Agents' tasks»: la checklist de cada sesión debajo de su nombre, con «Working» en palabras. «Waits for» enlaza con la tarea de la que depende, aunque sea tuya.",
  },
  {
    id: "session",
    label: "2 · In a session",
    note: "Dentro de la sesión, la petición se queda anclada sobre el composer mientras esté abierta: no se hunde en el transcript. En el transcript queda una tarjeta «Asked you». El panel de la sesión tiene una fila Tasks que abre su página (en el móvil, página de la misma hoja).",
  },
  {
    id: "edit",
    label: "3 · Create & edit",
    note: "El detalle es el editor: no hay modo edición. Al cambiar algo aparece el pie Guardar / Guardar y avisar (solo si hay una sesión a la que avisar). Subtareas de un nivel; «Waits for» son las dependencias y «Unblocks» su reverso.",
  },
  {
    id: "move",
    label: "4 · Move",
    note: "Escritorio: arrastrar una fila sobre la cabecera de una lista, o el chip de sitio del detalle. Enviar al backlog no avisa. Asignar a una sesión pide un segundo clic («Assign to …») porque avisa en el acto. En el móvil, «Move» abre la lista en la misma página.",
  },
  {
    id: "done",
    label: "5 · Done with a note",
    note: "Completar una petición abre la fila en el sitio con una nota opcional para la sesión que la pidió; «Done» la marca y se lo manda. La sesión lo recibe como evento y sigue sola. Una nota tuya se completa con un toque, sin preguntar.",
  },
  {
    id: "entry",
    label: "6 · Entry & counter",
    note: "La entrada vive en el pie del sidebar (y del drawer en el móvil), al lado de Inbox. El número cuenta peticiones abiertas de agentes; tus notas no cuentan. Sin relleno ámbar ni toast: Inbox es atención, Tasks es pendiente.",
  },
  {
    id: "empty",
    label: "7 · Empty",
    note: "Sin tareas la página dice «Nothing pending.» y deja el botón New task. La sesión sin tareas ofrece «Add a task» (asignarla a esa sesión, que avisa). Sin peticiones abiertas, la entrada del sidebar no muestra número.",
  },
];

const VARIANTS = [
  { id: "a", label: "A · Four places", note: "Notes, For you, Backlog, Agents: el modelo propuesto por el owner." },
  { id: "b", label: "B · Three places", note: "You, Backlog, Agents. Una petición es una tarea tuya que recuerda quién la pidió: «Notes» y «For you» son una sola lista, con las peticiones arriba. Un concepto menos y ningún destino sin sentido (no puedes «mover a For you»)." },
];

function emptyAll(seed) { return []; }
function emptyForYou(seed) { return seed.filter((t) => !(t.place === "you")); }

function Scene({ scene, variant }) {
  // Each frame gets its own store, so playing with one does not change the other.
  const k = `${scene}-${variant}`;
  if (scene === "global" || scene === "agents") {
    const agents = scene === "agents";
    return (
      <div class="tk-frames" key={k}>
        <StoreFrame render={(s) => (
          <DesktopFrame label="Desktop · 1180 × 760" sidebar={<Sidebar tasksActive count={countForYou(s.tasks)} />}>
            <GlobalDesktop store={s} variant={variant} init={{ selected: agents ? "t10" : "t1", agents }} />
          </DesktopFrame>
        )} />
        <StoreFrame render={(s) => (
          <PhoneFrame label="Phone · 390 × 780">
            <GlobalPhone store={s} variant={variant} init={{ agents }} />
          </PhoneFrame>
        )} />
      </div>
    );
  }
  if (scene === "session") {
    return (
      <div class="tk-frames" key={k}>
        <StoreFrame render={(s) => (
          <DesktopFrame label="Desktop · session with its panel on Tasks" sidebar={<Sidebar count={countForYou(s.tasks)} activeSession="pulse" />}>
            <SessionDesktop store={s} />
          </DesktopFrame>
        )} />
        <StoreFrame render={(s) => (
          <PhoneFrame label="Phone · the conversation">
            <SessionPhone store={s} />
          </PhoneFrame>
        )} />
        <StoreFrame render={(s) => (
          <PhoneFrame label="Phone · panel › Tasks (one sheet)">
            <SessionPhone store={s} init={{ sheet: true }} />
          </PhoneFrame>
        )} />
      </div>
    );
  }
  if (scene === "edit") {
    return (
      <div class="tk-frames" key={k}>
        <StoreFrame render={(s) => (
          <DesktopFrame label="Desktop · editing an agent's task" sidebar={<Sidebar tasksActive count={countForYou(s.tasks)} />}>
            <GlobalDesktop store={s} variant={variant} init={{
              selected: "t12", agents: true,
              dirty: { subs: [{ t: "Job con environment staging", done: true }, { t: "Cache de módulos Go", done: false }, { t: "Rollback automático si falla el healthcheck", done: false }] },
            }} />
          </DesktopFrame>
        )} />
        <StoreFrame render={(s) => (
          <DesktopFrame label="Desktop · new task" sidebar={<Sidebar tasksActive count={countForYou(s.tasks)} />}>
            <GlobalDesktop store={s} variant={variant} init={{ creating: true }} />
          </DesktopFrame>
        )} />
        <StoreFrame render={(s) => (
          <PhoneFrame label="Phone · new task">
            <GlobalPhone store={s} variant={variant} init={{ page: "new" }} />
          </PhoneFrame>
        )} />
        <StoreFrame render={(s) => (
          <PhoneFrame label="Phone · a task with subtasks">
            <GlobalPhone store={s} variant={variant} init={{ page: "detail", selected: "t7" }} />
          </PhoneFrame>
        )} />
      </div>
    );
  }
  if (scene === "move") {
    return (
      <div class="tk-frames" key={k}>
        <StoreFrame render={(s) => (
          <DesktopFrame label="Desktop · dragging a note onto Backlog · moa" sidebar={<Sidebar tasksActive count={countForYou(s.tasks)} />}>
            <GlobalDesktop store={s} variant={variant} init={{ selected: "t4", demoDrag: { task: "t4", over: "bl-moa" } }} />
          </DesktopFrame>
        )} />
        <StoreFrame render={(s) => (
          <DesktopFrame label="Desktop · move menu, assigning to a session" sidebar={<Sidebar tasksActive count={countForYou(s.tasks)} />}>
            <GlobalDesktop store={s} variant={variant} init={{ selected: "t7", menu: { pending: "race" } }} />
          </DesktopFrame>
        )} />
        <StoreFrame render={(s) => (
          <PhoneFrame label="Phone · Move">
            <GlobalPhone store={s} variant={variant} init={{ page: "detail", selected: "t7", menu: { pending: "race" } }} />
          </PhoneFrame>
        )} />
      </div>
    );
  }
  if (scene === "done") {
    return (
      <div class="tk-frames" key={k}>
        <StoreFrame render={(s) => (
          <DesktopFrame label="Desktop · completing a request from the list" sidebar={<Sidebar tasksActive count={countForYou(s.tasks)} />}>
            <GlobalDesktop store={s} variant={variant} init={{ selected: "t1", completing: "t1", showDone: true }} />
          </DesktopFrame>
        )} />
        <StoreFrame mutate={(seed) => seed.map((t) => (t.id === "t1" ? { ...t, status: "done", age: "now", isNew: false, doneNote: "Se llama GH_DEPLOY_KEY_PULSE, no GH_DEPLOY_KEY." } : t))} render={(s) => (
          <DesktopFrame label="Desktop · what the session sees afterwards" sidebar={<Sidebar count={countForYou(s.tasks)} activeSession="pulse" />}>
            <SessionDesktop store={s} init={{ panel: false }} />
          </DesktopFrame>
        )} />
        <StoreFrame render={(s) => (
          <PhoneFrame label="Phone · Done from the pinned line">
            <SessionPhoneCompleting store={s} />
          </PhoneFrame>
        )} />
      </div>
    );
  }
  if (scene === "entry") {
    return (
      <div class="tk-frames" key={k}>
        <StoreFrame render={(s) => (
          <DesktopFrame label="Desktop · the entry in the sidebar foot" sidebar={<Sidebar count={countForYou(s.tasks)} activeSession="pulse" />}>
            <SessionDesktop store={s} init={{ panel: false }} />
          </DesktopFrame>
        )} />
        <div class="tk-foot-specimens">
          <figcaption class="tk-caption">The foot in its three states</figcaption>
          <div class="tk-specimen"><SideFoot count={0} inbox={0} /><span class="tk-spec-note">Nothing asked of you: no number.</span></div>
          <div class="tk-specimen"><SideFoot count={3} inbox={2} /><span class="tk-spec-note">3 open requests. Inbox keeps its amber chip; Tasks is a plain number.</span></div>
          <div class="tk-specimen"><SideFoot count={3} inbox={2} tasksActive /><span class="tk-spec-note">Tasks open.</span></div>
        </div>
        <StoreFrame render={(s) => (
          <PhoneFrame label="Phone · drawer">
            <div class="tk-ph is-drawer"><Sidebar count={countForYou(s.tasks)} /></div>
          </PhoneFrame>
        )} />
      </div>
    );
  }
  if (scene === "empty") {
    return (
      <div class="tk-frames" key={k}>
        <StoreFrame mutate={emptyAll} render={(s) => (
          <DesktopFrame label="Desktop · nothing pending" sidebar={<Sidebar tasksActive count={0} />}>
            <GlobalDesktop store={s} variant={variant} />
          </DesktopFrame>
        )} />
        <StoreFrame mutate={emptyAll} render={(s) => (
          <PhoneFrame label="Phone · nothing pending">
            <GlobalPhone store={s} variant={variant} />
          </PhoneFrame>
        )} />
        <StoreFrame mutate={emptyAll} render={(s) => (
          <PhoneFrame label="Phone · a session without tasks">
            <SessionPhone store={s} init={{ sheet: true }} />
          </PhoneFrame>
        )} />
        <StoreFrame mutate={emptyForYou} render={(s) => (
          <PhoneFrame label="Phone · only backlog left">
            <GlobalPhone store={s} variant={variant} />
          </PhoneFrame>
        )} />
      </div>
    );
  }
  return null;
}

function SessionPhoneCompleting({ store }) {
  const ref = useRef(null);
  useEffect(() => {
    const b = ref.current?.querySelector(".tk-pin .tk-btn");
    b?.click();
  }, []);
  return <div ref={ref} style={{ display: "contents" }}><SessionPhone store={store} /></div>;
}

function StoreFrame({ render, mutate }) {
  const s = useTaskStore(mutate);
  return render(s);
}

function Seg({ options, value, onChange, label }) {
  return (
    <div class="tk-lab-seg" role="radiogroup" aria-label={label}>
      {options.map((o) => (
        <button type="button" role="radio" aria-checked={o.id === value} class={`tk-lab-opt${o.id === value ? " is-on" : ""}`} onClick={() => onChange(o.id)}>
          {o.label}
        </button>
      ))}
    </div>
  );
}

export function TasksLabA() {
  const params = new URLSearchParams(location.search);
  const [scene, setScene] = useState(params.get("scene") || "global");
  const [variant, setVariant] = useState("a");
  const sync = (sc, v) => {
    const p = new URLSearchParams(location.search);
    p.set("scene", sc); p.set("v", v);
    history.replaceState(null, "", `?${p.toString()}`);
  };
  const cur = SCENES.find((s) => s.id === scene) || SCENES[0];
  const vcur = VARIANTS.find((v) => v.id === variant) || VARIANTS[0];
  return (
    <div class="tk-lab" style={{ paddingTop: "72px" }}>
      <header class="tk-lab-head">
        <h1>Tasks</h1>
        <p>Lo que un agente te pide, lo que apuntas tú, el backlog de cada proyecto y la checklist de cada sesión. Todo es clicable: abre filas, completa, mueve, edita.</p>
      </header>
      <div class="tk-lab-ctl">
        <Seg label="Scene" options={SCENES} value={cur.id} onChange={(s) => { setScene(s); sync(s, variant); }} />
        <p class="tk-lab-note">{cur.note}</p>
      </div>
      <Scene scene={cur.id} variant={vcur.id} />
    </div>
  );
}
