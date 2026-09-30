import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import { ArrowUpRight, Check, ChevronDown, ChevronLeft, ChevronRight, Link2, Plus, Search } from "lucide-preact";
import { Kbd } from "../../primitives/Kbd/Kbd.jsx";
import { formatShortcut } from "../../data/util/shortcut.js";
import { addToast } from "../../data/notifications.js";
import { loadTask, patchTask, isNewRequest } from "../../data/tasks.js";
import {
  completeNotifies, completePatch, deliverOptions, errorText, isHere, isOpen, isRequest, needsDeliverChoice, noticeStateWords,
  projectLabelOf, recipientFor, relAge, sessionName, startNotifyGesture, taskProjectName,
} from "../../data/tasks-model.js";
import { moveIndex, moveSearch } from "../../data/tasks-move.js";
import { isTargetHere } from "../../data/schedule-model.js";
import { projectName } from "../../data/util/format.js";
import { openSession } from "../../data/tile-actions.js";
import { openOwnerConversation, ownersSlice } from "../../data/owners.js";
import { store } from "../../data/store.js";
import { ownerState } from "../../data/owners-model.js";
import { OwnerAvatarFor } from "../Owners/OwnerAvatar.jsx";
import { TasksGlyph } from "./TasksGlyph.jsx";
import "./Tasks.css";

// The pieces the Tasks surfaces share: the row and its groups, completing
// with a note, the wake-or-hold choice, Move and the filters. Everything that
// DECIDES lives in data/tasks-model.js; these only draw and wire it.

export const MOD_ENTER = formatShortcut("↵", { mod: true });

export function Keycap({ children }) {
  return <Kbd class="kbd tk-kbd">{children}</Kbd>;
}

export { TasksGlyph };

export function BackIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M10 3.5L5.5 8l4.5 4.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  );
}

export function CloseIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" />
    </svg>
  );
}

export function CheckRing({ title, status, onToggle, big, disabled }) {
  const done = status === "done";
  return (
    <button
      type="button"
      class={`tk-check${done ? " is-done" : ""}${status === "in_progress" ? " is-working" : ""}${big ? " is-big" : ""}`}
      aria-label={done ? `Reopen ${title}` : `Complete ${title}`}
      disabled={disabled}
      onClick={(e) => { e.stopPropagation(); onToggle(); }}
    >
      <Check size={10} strokeWidth={3.2} aria-hidden="true" />
    </button>
  );
}

// useEscape — while a transient piece of a gesture is showing, Escape backs
// out of THAT piece and nothing under it (a page, the detail, a panel) hears
// the key. Pieces nest (a page, and a decision line on it), so the handlers
// are a stack and only the newest one answers: one key, one step back.
const escapeStack = [];
function onEscapeKey(e) {
  if (e.key !== "Escape" || !escapeStack.length) return;
  e.preventDefault();
  e.stopPropagation();
  escapeStack[escapeStack.length - 1].current();
}

export function useEscape(active, onEscape) {
  const ref = useRef(onEscape);
  ref.current = onEscape;
  useEffect(() => {
    if (!active) return undefined;
    if (!escapeStack.length) document.addEventListener("keydown", onEscapeKey, true);
    escapeStack.push(ref);
    return () => {
      const i = escapeStack.lastIndexOf(ref);
      if (i >= 0) escapeStack.splice(i, 1);
      if (!escapeStack.length) document.removeEventListener("keydown", onEscapeKey, true);
    };
  }, [active]);
}

// DeliverChoice — the gesture tells a session saved on disk, so its
// confirmation IS the owner's answer about the notice: "<verb> and wake" or
// "<verb>, notify when opened". Two answers of equal weight, in the place the
// gesture was made, with no default and no second step after either; Escape
// (or ✕) backs out of the gesture. Without onCancel there is nothing to back
// out of (a new task's foot), so there is no ✕ either.
export function DeliverChoice({ name, verb = "Notify", onChoose, onCancel, phone, inline, busy, autoFocus = true }) {
  const ref = useRef(null);
  useEffect(() => { if (autoFocus) ref.current?.focus({ preventScroll: true }); }, []);
  useEscape(!!onCancel, () => onCancel?.());
  const options = deliverOptions(verb, { state: "saved" });
  return (
    <div
      class={`tk-choice${inline ? " is-inline" : ""}${phone ? " is-phone" : ""}`}
      role="group"
      aria-label={`${name} is saved`}
      tabIndex={-1}
      ref={ref}
      onClick={(e) => e.stopPropagation()}
    >
      <div class="tk-choice-q">
        <span><b>{name}</b> is saved</span>
        {onCancel && <button type="button" class="tk-icon is-quiet" aria-label="Cancel" onClick={onCancel}><CloseIcon /></button>}
      </div>
      <div class="tk-choice-acts">
        {options.map((o) => (
          <button key={o.choice} type="button" class="zl-ask-btn" disabled={busy} onClick={() => onChoose(o.choice)}>{o.label}</button>
        ))}
      </div>
    </div>
  );
}

// useNotifyGesture — run a gesture that tells a session. When that session is
// saved, the gesture's one confirmation is DeliverChoice ("<verb> and wake" /
// "<verb>, notify when opened"); otherwise it runs at once, with no delivery
// choice at all.
export function useNotifyGesture() {
  const [pending, setPending] = useState(null);
  const run = (recipient, perform, verb = "Notify") => startNotifyGesture(recipient, perform, () => setPending({ recipient, perform, verb }));
  const cancel = () => setPending(null);
  const choose = (choice) => {
    const p = pending;
    setPending(null);
    p?.perform(choice);
  };
  return { pending, run, cancel, choose };
}

// CompleteNote — Done with an optional note. When the session that hears it
// is saved, Done itself becomes the two answers (DeliverChoice's labels), so
// the note and the wake/hold choice are one line of decision, not two.
export function CompleteNote({ who, saved = false, onCancel, onDone, inline, phone, preset = "", busy }) {
  const [note, setNote] = useState(preset);
  const ref = useRef(null);
  useEffect(() => { ref.current?.focus({ preventScroll: true }); }, []);
  useEscape(true, onCancel);
  return (
    <div class={`tk-note${inline ? " is-inline" : ""}${phone ? " is-phone" : ""}`} onClick={(e) => e.stopPropagation()}>
      <textarea
        ref={ref}
        class="tk-field tk-note-field"
        rows={2}
        placeholder={who ? `Note for ${who} (optional)` : "Note (optional)"}
        aria-label="Note"
        value={note}
        onInput={(e) => setNote(e.currentTarget.value)}
        onKeyDown={(e) => {
          // A saved session has no default answer, so ⌘↵ does nothing there.
          if (e.key === "Enter" && (e.metaKey || e.altKey || e.ctrlKey) && !saved) { e.preventDefault(); onDone(note, null); }
        }}
      />
      {saved ? (
        <>
          <div class="tk-choice-q"><span><b>{who}</b> is saved</span></div>
          <div class="tk-choice-acts">
            {deliverOptions("Done", { state: "saved" }).map((o) => (
              <button key={o.choice} type="button" class="zl-ask-btn" disabled={busy} onClick={() => onDone(note, o.choice)}>{o.label}</button>
            ))}
          </div>
          <div class="tk-note-acts">
            <button type="button" class="zl-ask-btn is-quiet" onClick={onCancel}>Cancel</button>
          </div>
        </>
      ) : (
        <div class="tk-note-acts">
          <button type="button" class="zl-ask-btn is-quiet" onClick={onCancel}>Cancel</button>
          <button type="button" class="zl-ask-btn is-primary" disabled={busy} onClick={() => onDone(note, null)}>
            Done{!phone && <Keycap>{MOD_ENTER}</Keycap>}
          </button>
        </div>
      )}
    </div>
  );
}

function failed(title, error) {
  addToast({ title, detail: errorText(error), type: "error" });
}

// CompletionFlow — completing a task that tells someone. The task is read
// fresh at once (its revision, and whether the session to tell is loaded right
// now), while a request already shows its note. When that session is saved,
// the confirmation is the wake/hold answer itself: one line of decision.
export function CompletionFlow({ task, sessions, phone, inline, preset, onDone, onCancel }) {
  const request = isRequest(task);
  const [fresh, setFresh] = useState(null);
  const [busy, setBusy] = useState(false);
  const loading = useRef(null);
  const read = () => {
    if (!loading.current) {
      loading.current = loadTask(task.id).then((rec) => {
        if (rec) setFresh(rec);
        return rec;
      });
    }
    return loading.current;
  };
  useEffect(() => { read(); }, []);

  const recipient = recipientFor(fresh || task, sessions);
  const saved = needsDeliverChoice(recipient);

  const finish = async (note, choice) => {
    setBusy(true);
    const rec = await read();
    if (!rec) {
      addToast({ title: "Could not complete the task", detail: "It is no longer available.", type: "error" });
      onCancel?.();
      return;
    }
    // Answered before the fresh read said the session is saved: never pick
    // for the owner. Stay, now showing the two answers.
    if (choice == null && needsDeliverChoice(recipientFor(rec, sessions))) {
      setBusy(false);
      return;
    }
    try {
      await patchTask(rec.id, completePatch(rec, { note, choice }));
      onDone?.();
    } catch (error) {
      failed("Could not complete the task", error);
      onCancel?.();
    }
  };

  if (request) {
    return (
      <CompleteNote
        who={sessionName(sessions, task.requester_session_id)}
        saved={saved}
        inline={inline}
        phone={phone}
        preset={preset}
        busy={busy}
        onCancel={onCancel}
        onDone={finish}
      />
    );
  }
  if (!fresh) return null;
  if (saved) {
    return (
      <DeliverChoice
        name={sessionName(sessions, recipient.session_id)}
        verb="Done"
        inline={inline}
        phone={phone}
        busy={busy}
        onChoose={(choice) => finish("", choice)}
        onCancel={onCancel}
      />
    );
  }
  return <AutoRun run={() => finish("", null)} />;
}

// AutoRun — nothing to ask: the gesture runs once, as soon as it is known.
function AutoRun({ run }) {
  useEffect(() => { run(); }, []);
  return null;
}

// ── Rows ─────────────────────────────────────────────────────────────────

function rowMeta({ task, context, sessions, lookup }) {
  const meta = [];
  if (task.status === "in_progress") meta.push(<span class="tk-word-working">Working</span>);
  const waiting = (task.waits_for || []).map(lookup).find((b) => b && isOpen(b));
  if (waiting && isOpen(task)) meta.push(<span class="tk-meta-wait"><Link2 size={12} aria-hidden="true" />Waits for {waiting.title}</span>);
  const notice = noticeStateWords(task.notice_state);
  if (notice) meta.push(<span class="tk-meta-notice">{notice}</span>);
  if (task.status === "done" && task.completion_note) meta.push(<span class="tk-meta-note">“{task.completion_note}”</span>);
  if (isRequest(task) && context !== "session") meta.push(<span class="tk-meta-from">{sessionName(sessions, task.requester_session_id)}</span>);
  if (context !== "session" && task.place === "you" && task.project_key) meta.push(<span>{taskProjectName(task)}</span>);
  if (task.subtasks?.length) meta.push(<span class="tk-data">{task.subtasks.filter((s) => s.done).length}/{task.subtasks.length}</span>);
  return meta;
}

function Meta({ items }) {
  return <span class="tk-row-meta">{items.map((m, i) => <>{i > 0 && <span class="tk-sep">·</span>}{m}</>)}</span>;
}

export function TaskRow({ task, selected, onSelect, phone, context, sessions, lookup, newSince, completing, onCheck, completion, draggable, onDragStart, onDragEnd, dragging }) {
  const meta = rowMeta({ task, context, sessions, lookup });
  const age = relAge(task.status === "done" ? task.completed_at || task.updated_at : task.created_at);
  return (
    <div
      class={`tk-row${selected ? " is-selected" : ""}${task.status === "done" ? " is-done" : ""}${completing ? " is-completing" : ""}${phone ? " is-phone" : ""}${dragging ? " is-dragging" : ""}`}
      data-task={task.id}
      role="option"
      aria-selected={!!selected}
      tabIndex={-1}
      draggable={!!draggable && !completing}
      onDragStart={onDragStart}
      onDragEnd={onDragEnd}
      onClick={() => !completing && onSelect(task.id)}
    >
      <div class="tk-row-line">
        {isNewRequest(task, newSince) && <span class="tk-new" aria-label="New" />}
        <CheckRing title={task.title} status={task.status} onToggle={() => onCheck(task)} big={phone && context !== "session"} />
        <span class="tk-row-main">
          <span class="tk-row-title">{task.title}</span>
          {phone && meta.length > 0 && <Meta items={meta} />}
        </span>
        {!phone && meta.length > 0 && <Meta items={meta} />}
        {age && <span class="tk-row-age tk-data">{age}</span>}
      </div>
      {completing && completion}
    </div>
  );
}

function GroupHead({ g, open, onToggle, dragging, onDrop }) {
  const [over, setOver] = useState(false);
  const live = !!g.drop && !!dragging;
  const hint = g.drop?.place === "agent" ? "Assign" : g.drop?.place === "backlog" ? "Move to backlog" : "Move to You";
  const body = (
    <>
      {g.collapsed && <ChevronDown size={13} class={`tk-group-chev${open ? "" : " is-closed"}`} aria-hidden="true" />}
      <span class="tk-group-t">{g.title}</span>
      {g.sub && <span class="tk-group-sub">{g.sub}</span>}
      {g.working && <span class="tk-word-working">Working</span>}
      {g.n > 0 && <span class="tk-group-n tk-data">{g.n}</span>}
      {g.progress && <span class="tk-group-n tk-data">{g.progress}</span>}
      {live && over && <span class="tk-drop-hint">{hint}</span>}
    </>
  );
  const cls = `tk-group${g.agent ? " is-agent" : ""}${live ? " is-drop" : ""}${live && over ? " is-over" : ""}`;
  const drag = {
    onDragOver: (e) => { if (live) { e.preventDefault(); setOver(true); } },
    onDragLeave: () => setOver(false),
    onDrop: (e) => { e.preventDefault(); setOver(false); if (live) onDrop(g.drop); },
  };
  if (g.collapsed) {
    return <button type="button" class={cls} aria-expanded={open} onClick={onToggle}>{body}</button>;
  }
  return <div class={cls} {...drag}>{body}</div>;
}

// TaskGroups — the list: group heads and their rows. Completing happens in
// the row itself (CompletionFlow), and drag-and-drop onto a group head moves.
export function TaskGroups({ groups, selected, onSelect, phone, context, sessions, lookup, newSince, completingId, setCompletingId, onCheck, onAdd, onDropTask, doneOpen, setDoneOpen, draggable }) {
  const [dragging, setDragging] = useState(null);
  const visible = groups.filter((g) => g.rows.length || g.add);
  if (!visible.length) return null;
  return (
    <div class={`tk-list${phone ? " is-phone" : ""}`} role="listbox" aria-label="Tasks">
      {visible.map((g) => {
        const open = !g.collapsed || doneOpen;
        return (
          <section class="tk-sec" key={g.id} aria-label={g.sub ? `${g.title} ${g.sub}` : g.title}>
            <GroupHead
              g={g}
              open={open}
              onToggle={() => setDoneOpen?.(!doneOpen)}
              dragging={dragging}
              onDrop={(dest) => { const id = dragging; setDragging(null); onDropTask?.(id, dest); }}
            />
            {open && g.rows.map((t) => (
              <TaskRow
                key={t.id}
                task={t}
                selected={selected === t.id}
                onSelect={onSelect}
                phone={phone}
                context={context}
                sessions={sessions}
                lookup={lookup}
                newSince={newSince}
                completing={completingId === t.id}
                completion={completingId === t.id && (
                  <CompletionFlow
                    task={t}
                    sessions={sessions}
                    phone={phone}
                    inline
                    onDone={() => setCompletingId(null)}
                    onCancel={() => setCompletingId(null)}
                  />
                )}
                onCheck={onCheck}
                draggable={draggable}
                dragging={dragging === t.id}
                onDragStart={(e) => { e.dataTransfer.setData("text/plain", String(t.id)); e.dataTransfer.effectAllowed = "move"; setDragging(t.id); }}
                onDragEnd={() => setDragging(null)}
              />
            ))}
            {g.add && onAdd && (
              <button type="button" class={`tk-add${phone ? " is-phone" : ""}`} onClick={onAdd}>
                <Plus size={14} aria-hidden="true" />Add a task
              </button>
            )}
          </section>
        );
      })}
    </div>
  );
}

// completesInline — the tasks whose ring opens a flow in the row instead of
// completing at once: a request (optional note) and an agent's task (it tells
// the agent, and a saved agent asks wake or hold).
export function completesInline(task) {
  return isRequest(task) || completeNotifies(task);
}

// useRowChecks — the ring on a row. Reopening and completing a plain task
// happen at once; completing one that tells a session opens CompletionFlow in
// the row.
export function useRowChecks(inline = completesInline) {
  const [completingId, setCompletingId] = useState(null);
  const onCheck = async (task) => {
    if (task.status === "done") {
      const fresh = task.revision ? task : await loadTask(task.id);
      if (!fresh) return;
      patchTask(fresh.id, { revision: fresh.revision, status: "pending" }).catch((error) => failed("Could not reopen the task", error));
      return;
    }
    if (inline(task)) {
      setCompletingId(completingId === task.id ? null : task.id);
      return;
    }
    const fresh = task.revision ? task : await loadTask(task.id);
    if (!fresh) return;
    patchTask(fresh.id, completePatch(fresh)).catch((error) => failed("Could not complete the task", error));
  };
  return { completingId, setCompletingId, onCheck };
}

export function Filters({ agents, setAgents, project, setProject, projects, phone, session, onClearSession }) {
  const [menu, setMenu] = useState(false);
  useEscape(menu, () => setMenu(false));
  const current = projects.find((p) => p.key === project);
  // Narrowed to one session (its status line opened the view): that is the
  // only filter, and clearing it shows every task again.
  if (session) {
    return (
      <div class={`tk-filters${phone ? " is-phone" : ""}`}>
        <button type="button" class="tk-chip is-on tk-chip-session" aria-label={`Show every task, not only ${session}'s`} onClick={onClearSession}>
          <span class="tk-chip-t">{session}</span><CloseIcon />
        </button>
      </div>
    );
  }
  return (
    <div class={`tk-filters${phone ? " is-phone" : ""}`}>
      <div class="tk-anchor">
        <button type="button" class={`tk-chip${project ? " is-on" : ""}`} aria-expanded={menu} aria-haspopup="menu" onClick={() => setMenu(!menu)}>
          {current ? projectLabelOf(current) : "All projects"}<ChevronDown size={13} aria-hidden="true" />
        </button>
        {menu && (
          <div class="tk-pop is-narrow" role="menu">
            {[null, ...projects].map((p) => (
              <button
                key={p?.key || "all"}
                type="button"
                role="menuitemradio"
                aria-checked={(p?.key || null) === project}
                class={`tk-pop-item${phone ? " is-phone" : ""}`}
                onClick={() => { setProject(p?.key || null); setMenu(false); }}
              >
                <span class="tk-pop-t">{p ? projectLabelOf(p) : "All projects"}</span>
                {(p?.key || null) === project && <Check size={14} class="tk-pop-on" aria-hidden="true" />}
              </button>
            ))}
          </div>
        )}
      </div>
      <button type="button" class={`tk-chip${agents ? " is-on" : ""}`} aria-pressed={agents} onClick={() => setAgents(!agents)}>
        Agents' tasks
      </button>
    </div>
  );
}

export function EmptyState({ onNew, phone, compact, text = "Nothing pending." }) {
  return (
    <div class={`tk-empty${phone ? " is-phone" : ""}${compact ? " is-compact" : ""}`}>
      <span class="tk-empty-mark" aria-hidden="true"><TasksGlyph /></span>
      <p class="tk-empty-t">{text}</p>
      {onNew && (
        <button type="button" class="zl-ask-btn" onClick={onNew}>
          <Plus size={15} aria-hidden="true" />New task{!phone && <Keycap>C</Keycap>}
        </button>
      )}
    </div>
  );
}

// ── Move ─────────────────────────────────────────────────────────────────

// openTaskSession — a task's session, opened as the sidebar opens it: shown,
// never prompted. An owner's own conversation is not in the session roster
// until it is asked for, so it goes through the owner.
export function openTaskSession(id) {
  if (!id) return false;
  if (openSession(id)) return true;
  const own = ownersSlice(store.get()).list.find((o) => o.session_id === id);
  return own ? openOwnerConversation(own) : false;
}

// OpenSessionButton — the way from a task to the session it belongs to (its
// assignee) or that asked for it. Opening shows the session, as the sidebar
// would: it does not start a turn.
export function OpenSessionButton({ sessions, id, phone, onOpen }) {
  const name = sessionName(sessions, id);
  const owner = sessions[id]?.kind === "owner";
  return (
    <button type="button" class={`tk-open${phone ? " is-phone" : ""}`} aria-label={`Open ${name}`} title={owner ? `Open ${name}` : "Open the session"} onClick={() => onOpen?.(id)}>
      Open<ArrowUpRight size={phone ? 16 : 14} aria-hidden="true" />
    </button>
  );
}

const STATE_WORDS = { running: "Working", permission: "Waiting on you" };

function stateWord(state) {
  const w = STATE_WORDS[state];
  if (!w) return null;
  return <span class={state === "running" ? "tk-word-working" : "tk-word-waiting"}>{w}</span>;
}

function joinMeta(parts) {
  const items = parts.filter(Boolean);
  return items.map((m, i) => <>{i > 0 && <span class="tk-sep">·</span>}{m}</>);
}

// MoveList — where a task goes: You, an owner, a recent session, or a
// project, which holds its backlog and its sessions. Hundreds of sessions
// never make one list: a project opens as its own level, and the search
// reaches every level at once.
//
// Choosing a session is not the move yet: it asks to confirm, because
// assigning tells that session (and, when it is saved, asks wake or hold
// instead). `direct` is for a task being written: there the choice only sets
// where it will go, and the confirmation is the create button itself.
//
// The project level is the picker's own unless `onLevel` is given: the
// phone's Tasks screen makes it a page of the sheet, so ‹ walks it back.
//
// A scheduled task's Send to walks the same levels (`mode="target"`): no You
// and no backlog, and a project offers a new session in each of its
// directories. Rerouting a run that was not sent (`mode="reroute"`) offers
// existing sessions only. Both pick directly: the schedule's own foot is the
// confirmation.
export function MoveList({
  task, projects, sessions, owners = [], phone, onPick, direct = false, pending: pending0 = null,
  level: levelProp = null, onLevel, mode = "move", target: schedTarget = null,
}) {
  const moving = mode === "move";
  const [q, setQ] = useState("");
  const [pending, setPending] = useState(pending0);
  const [ownLevel, setOwnLevel] = useState(null);
  const [full, setFull] = useState(null);
  const level = onLevel ? levelProp : ownLevel;
  const goLevel = (key) => { setQ(""); setFull(null); if (onLevel) onLevel(key); else setOwnLevel(key); };
  const target = pending ? sessions[pending] : null;
  const savedTarget = needsDeliverChoice({ state: target?.state === "saved" ? "saved" : "live" });
  // A loaded session: Assign and notify confirms. A saved one: the
  // confirmation already offers wake or hold (DeliverChoice), nothing after.
  const confirm = (choice = null) => {
    if (!pending || (savedTarget && choice == null)) return;
    onPick({ place: "agent", sessionId: pending }, choice);
  };
  useEscape(!!pending && !direct, () => setPending(null));
  useEscape(!pending && !onLevel && !!level, () => goLevel(null));
  useEffect(() => {
    if (!pending || phone) return undefined;
    const onKey = (e) => {
      if (e.key !== "Enter" || e.metaKey || e.altKey || savedTarget) return;
      if (String(e.target?.tagName).toUpperCase() === "BUTTON") return;
      e.preventDefault();
      confirm();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [pending, phone, savedTarget]);

  const index = useMemo(() => moveIndex({ sessions, owners, projects }), [sessions, owners, projects]);
  // A new level starts at its top (its back row, its backlog), not where the
  // list above it was scrolled to.
  const root = useRef(null);
  const shownLevel = useRef(level);
  useEffect(() => {
    if (shownLevel.current === level) return;
    shownLevel.current = level;
    let el = root.current?.parentElement;
    while (el && el.scrollHeight <= el.clientHeight) el = el.parentElement;
    if (el && el !== document.documentElement && el !== document.body) el.scrollTop = 0;
  }, [level]);
  const project = level ? index.projects.find((p) => p.key === level) : null;
  const cls = (extra = "") => `tk-pop-item${extra}${phone ? " is-phone" : ""}`;

  const Item = ({ dest, label, sub, lead }) => (
    <button
      type="button"
      class={cls(`${sub ? " is-two" : ""}${lead ? " has-lead" : ""}${dest.place === "agent" && pending === dest.sessionId ? " is-pending" : ""}`)}
      onClick={() => (dest.place === "agent" && !direct ? setPending(dest.sessionId) : onPick(dest, null))}
    >
      {lead}
      <span class="tk-pop-main">
        <span class="tk-pop-t">{label}</span>
        {sub && <span class="tk-pop-sub">{sub}</span>}
      </span>
      {(moving ? isHere(task, dest) : isTargetHere(schedTarget, dest, owners)) && <Check size={14} class="tk-pop-on" aria-hidden="true" />}
    </button>
  );
  const SessionItem = ({ s, withProject = true }) => (
    <Item
      key={s.id}
      dest={{ place: "agent", sessionId: s.id }}
      label={s.title || "Untitled"}
      sub={joinMeta([withProject && index.projectOfSession(s), stateWord(s.state) || (s.updated ? relAge(s.updated) : null)])}
    />
  );
  const OwnerItem = ({ o }) => (
    <Item
      key={o.id}
      dest={{ place: "agent", sessionId: o.id, owner: o.owner }}
      label={o.name}
      lead={<span class="tk-pop-lead"><OwnerAvatarFor owner={o.owner} state={ownerState(o.owner)} size={phone ? 24 : 20} /></span>}
      sub={stateWord(o.state)}
    />
  );
  const backlog = (p) => (
    <Item key={`b-${p.key}`} dest={{ place: "backlog", key: p.key, cwd: p.cwd }} label={level ? "Backlog" : `Backlog · ${p.label}`} />
  );
  // A new session in a project: one per directory when it has several, so
  // the choice of checkout is made here and stored.
  const newRows = (p) => {
    const dirs = p.cwds?.length ? p.cwds : [p.cwd];
    return dirs.map((cwd) => (
      <Item
        key={`n-${p.key}-${cwd}`}
        dest={{ place: "new", key: p.key, cwd }}
        label={`New session in ${p.label}`}
        sub={dirs.length > 1 ? cwd.split("/").filter(Boolean).slice(-1)[0] || projectName(cwd) : null}
      />
    ));
  };
  const projectSub = (p) => joinMeta([
    p.sessions.length ? `${p.sessions.length} session${p.sessions.length === 1 ? "" : "s"}` : "No sessions",
    p.working ? <span class="tk-word-working">{p.working} working</span> : null,
  ]);
  const ProjectRow = ({ p }) => (
    <button key={p.key} type="button" class={cls(" is-two is-nav")} onClick={() => goLevel(p.key)}>
      <span class="tk-pop-main">
        <span class="tk-pop-t">{p.label}</span>
        <span class="tk-pop-sub">{projectSub(p)}</span>
      </span>
      <ChevronRight size={15} class="tk-pop-chev" aria-hidden="true" />
    </button>
  );
  const PAGE = 30;
  const sessionsOf = (p) => {
    const shown = full === p.key ? p.sessions : p.sessions.slice(0, PAGE);
    return (
      <>
        {shown.map((s) => <SessionItem key={s.id} s={s} withProject={false} />)}
        {shown.length < p.sessions.length && (
          <button type="button" class={cls(" is-more")} onClick={() => setFull(p.key)}>
            Show all {p.sessions.length}
          </button>
        )}
      </>
    );
  };

  let body;
  const needle = q.trim();
  if (project) {
    const hits = moveSearch(index, q, project.key);
    body = (
      <>
        {!needle && (moving ? backlog(project) : mode === "target" ? newRows(project) : null)}
        {project.sessions.length > 0 && <div class="tk-pop-label">Sessions</div>}
        {needle ? hits.sessions.map((s) => <SessionItem key={s.id} s={s} withProject={false} />) : sessionsOf(project)}
        {needle && hits.more > 0 && <div class="tk-pop-none">{hits.more} more. Keep typing to narrow it.</div>}
        {needle && hits.sessions.length === 0 && <div class="tk-pop-none">No session matches.</div>}
      </>
    );
  } else if (needle) {
    const hits = moveSearch(index, q);
    const none = !hits.owners.length && !hits.projects.length && !hits.sessions.length;
    body = (
      <>
        {hits.owners.length > 0 && <div class="tk-pop-label">Owners</div>}
        {hits.owners.map((o) => <OwnerItem key={o.id} o={o} />)}
        {moving && hits.projects.length > 0 && <div class="tk-pop-label">Backlogs</div>}
        {moving && hits.projects.map((p) => backlog(p))}
        {mode === "target" && hits.projects.length > 0 && <div class="tk-pop-label">New session in</div>}
        {mode === "target" && hits.projects.map((p) => newRows(p))}
        {hits.sessions.length > 0 && <div class="tk-pop-label">Sessions</div>}
        {hits.sessions.map((s) => <SessionItem key={s.id} s={s} />)}
        {hits.more > 0 && <div class="tk-pop-none">{hits.more} more. Keep typing to narrow it.</div>}
        {none && <div class="tk-pop-none">Nothing matches.</div>}
      </>
    );
  } else {
    body = (
      <>
        {moving && <Item dest={{ place: "you" }} label="You" />}
        {index.owners.length > 0 && <div class="tk-pop-label">Owners</div>}
        {index.owners.map((o) => <OwnerItem key={o.id} o={o} />)}
        {index.recent.length > 0 && <div class="tk-pop-label">Recent</div>}
        {index.recent.map((s) => <SessionItem key={s.id} s={s} />)}
        {index.projects.length > 0 && <div class="tk-pop-label">Projects</div>}
        {index.projects.map((p) => <ProjectRow key={p.key} p={p} />)}
      </>
    );
  }

  const search = (
    <label class={`tk-search${phone ? " is-phone" : ""}`}>
      <Search size={phone ? 15 : 14} aria-hidden="true" />
      <input
        class="tk-field"
        placeholder={project ? `Search ${project.label}` : moving ? "Search owners, projects, sessions" : "Send to…"}
        aria-label={moving ? "Search where to move it" : "Find where to send it"}
        value={q}
        onInput={(e) => setQ(e.currentTarget.value)}
      />
    </label>
  );
  return (
    <div class={`tk-move${phone ? " is-phone" : ""}`} ref={root} onClick={(e) => e.stopPropagation()}>
      {project && !onLevel && (
        <button type="button" class={cls(" is-back")} onClick={() => goLevel(null)}>
          <ChevronLeft size={15} aria-hidden="true" /><span class="tk-pop-t">{project.label}</span>
        </button>
      )}
      {search}
      {body}
      {pending && (
        <div class="tk-move-confirm">
          {savedTarget ? (
            <DeliverChoice
              key={pending}
              name={target?.title || "The session"}
              verb="Assign"
              phone={phone}
              autoFocus={false}
              onChoose={confirm}
              onCancel={() => setPending(null)}
            />
          ) : (
            <button type="button" class="zl-ask-btn is-primary tk-wide" onClick={() => confirm(null)}>
              Assign and notify{!phone && <Keycap>↵</Keycap>}
            </button>
          )}
        </div>
      )}
    </div>
  );
}
