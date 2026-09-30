import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import {
  AlarmClock, ArrowUpRight, ChevronDown, ChevronRight, Clock3, Globe, Pause, Play, Plus, Repeat, Trash2,
} from "lucide-preact";
import { useStore } from "../../hooks/useStore.js";
import { api } from "../../data/api.js";
import { addToast } from "../../data/notifications.js";
import { ownersSlice } from "../../data/owners.js";
import { ensureModelCatalog, modelCatalog } from "../../data/model-catalog.js";
import { hasBlockingOverlay } from "../../data/overlays.js";
import {
  confirmRun, createTask, deleteTaskAt, loadTaskProjects, parseWhen, patchDraft, patchTask, pauseSchedule, peekDraft, rerouteRun,
  resumeSchedule, runScheduleNow, selectSessionDirectory, skipScheduleNext, stashDraft, takeDraft, tasksSlice, watchTask,
} from "../../data/tasks.js";
import { conflictCurrent, deletePath, errorText, isTypingTarget, projectOptions, sessionName } from "../../data/tasks-model.js";
import {
  canReroute, clock, createWhenPreview, dateInputValue, dateLabel, deliveryRows, deliverySummary, deviceZone, failedRun, failureWords,
  inWords, isDefaultDelivery, isRepeat, lateBanner, lateRun, modelLabel, pickedWhen, presets, rebaseSchedDraft, repeatOptions, ruleShort,
  ruleText, runOpenSession, runRows, schedActions, schedDirty, schedDraft, schedEyebrow, schedRight, scheduleBody, schedulePatch,
  scheduledRows, stateOf, targetFromDest, targetName, targetSessionId, waitingCount, whenButton, whenLong, whenNext, whenShort,
} from "../../data/schedule-model.js";
import { CloseIcon, Keycap, MOD_ENTER, MoveList, OpenSessionButton, useEscape } from "./parts.jsx";

// The scheduled and recurring tasks (docs/serve.md, Scheduled tasks): the
// Scheduled group of the Tasks lists, a scheduled task's detail (When, Send
// to, Delivery, its runs), and the When editor the composer's Send later
// shares. What decides lives in data/schedule-model.js; the server owns the
// calendar and reads what is typed into When.

function failed(title, error) {
  addToast({ title, detail: errorText(error), type: "error" });
}

// useNow — relative words ("in 20 min") stay true while a view is open.
export function useNow(every = 30000) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), every);
    return () => clearInterval(t);
  }, [every]);
  return now;
}

function autosize(el) {
  if (!el) return;
  el.style.height = "auto";
  el.style.height = `${el.scrollHeight}px`;
}

// ── Rows ─────────────────────────────────────────────────────────────────

export function SchedGlyph({ t, big }) {
  const s = stateOf(t);
  const cls = `sch-glyph${s === "late" || s === "failed" ? " is-attn" : ""}${s === "paused" ? " is-paused" : ""}${big ? " is-big" : ""}`;
  const I = s === "paused" ? Pause : isRepeat(t) ? Repeat : Clock3;
  return <span class={cls} aria-hidden="true"><I size={big ? 16 : 14} strokeWidth={2} /></span>;
}

function Meta({ items }) {
  return <span class="tk-row-meta">{items.map((m, i) => <>{i > 0 && <span class="tk-sep">·</span>}{m}</>)}</span>;
}

// SchedRow — the list's row, with a clock where the ring is (a scheduled task
// is not yours to do; it runs) and the next run where the age is. What waits
// for you says so in amber, in words.
export function SchedRow({ t, selected, onSelect, phone, context, sessions, owners, details, now, tz }) {
  const s = stateOf(t);
  const meta = [];
  if (s === "late") meta.push(<span class="sch-word-wait">Waiting for you</span>);
  if (s === "failed") meta.push(<span class="tk-meta-notice">Not sent</span>);
  if (s === "paused") meta.push(<span>Paused</span>);
  if (context !== "session") meta.push(<span class="tk-meta-from">{targetName(t.target, sessions, owners)}</span>);
  if (t.created_by_session_id) meta.push(<span>Set by the agent</span>);
  if (isRepeat(t)) meta.push(<span class="tk-data">{ruleShort(t.when.rule)}</span>);
  const right = schedRight(t, now, tz, lateRun(details?.[t.id]));
  return (
    <div
      class={`tk-row sch-row${selected ? " is-selected" : ""}${phone ? " is-phone" : ""}${s === "paused" ? " is-paused" : ""}`}
      data-task={t.id}
      role="option"
      aria-selected={!!selected}
      tabIndex={-1}
      onClick={() => onSelect(t.id)}
    >
      <div class="tk-row-line">
        <SchedGlyph t={t} big={phone && context !== "session"} />
        <span class="tk-row-main">
          <span class="tk-row-title">{t.title}</span>
          {phone && meta.length > 0 && <Meta items={meta} />}
        </span>
        {!phone && meta.length > 0 && <Meta items={meta} />}
        {right && <span class={`tk-row-age tk-data sch-next${s === "late" ? " is-attn" : ""}`}>{right}</span>}
      </div>
    </div>
  );
}

// ScheduledGroup — Scheduled, on top of the list: what needs you first, then
// by next run, paused at the end. Five rows, then "Show all".
export function ScheduledGroup({ list, selected, onSelect, phone, context, limit = 5, onAdd }) {
  const [all, setAll] = useState(false);
  const sessions = useStore(selectSessionDirectory);
  const owners = useStore((st) => ownersSlice(st).list);
  const details = useStore((st) => tasksSlice(st).details);
  const now = useNow();
  const tz = deviceZone();
  const rows = scheduledRows(list);
  if (!rows.length && !onAdd) return null;
  const shown = all ? rows : rows.slice(0, limit);
  const waiting = waitingCount(rows);
  return (
    <div class={`tk-list sch-list${phone ? " is-phone" : ""}`} role="listbox" aria-label="Scheduled">
      <section class="tk-sec" aria-label="Scheduled">
        <div class={`tk-group${waiting ? " sch-group-attn" : ""}`}>
          <span class="tk-group-t">Scheduled</span>
          {waiting > 0 && <span class="sch-group-wait">{waiting} waiting for you</span>}
          {rows.length > 0 && <span class="tk-group-n tk-data">{rows.length}</span>}
        </div>
        {shown.map((t) => (
          <SchedRow key={t.id} t={t} sessions={sessions} owners={owners} details={details} now={now} tz={tz} selected={selected === t.id} onSelect={onSelect} phone={phone} context={context} />
        ))}
        {rows.length > shown.length && (
          <button type="button" class={`tk-add${phone ? " is-phone" : ""}`} onClick={() => setAll(true)}>
            <ChevronDown size={14} aria-hidden="true" />Show all {rows.length}
          </button>
        )}
        {onAdd && (
          <button type="button" class={`tk-add${phone ? " is-phone" : ""}`} onClick={onAdd}>
            <Plus size={14} aria-hidden="true" />Schedule a task
          </button>
        )}
      </section>
    </div>
  );
}

// ── When ─────────────────────────────────────────────────────────────────

// WhenEditor — words first, read by the server, with the date they resolve
// to underneath; and the three fields (date, time, repeat) always in sync
// with it. The zone is this device's, said once, quietly.
export function WhenEditor({ value, onChange, text: text0 = "", phone, autoFocus = true, onSubmit }) {
  const tz = deviceZone();
  const now = useNow();
  const [text, setText] = useState(text0);
  const [read, setRead] = useState(null);
  const field = useRef(null);
  const timer = useRef(null);
  const change = useRef(onChange);
  change.current = onChange;
  const typed = useRef(text0);
  const preview = useMemo(() => createWhenPreview((body) => parseWhen(body.text, body.tz), (r) => {
    setRead(r);
    if (r?.when) change.current(r.when, typed.current);
  }), []);
  useEffect(() => {
    if (autoFocus) field.current?.focus({ preventScroll: true });
    if (text0) preview(text0, tz);
    return () => clearTimeout(timer.current);
  }, []);
  const onText = (t) => {
    setText(t);
    typed.current = t;
    clearTimeout(timer.current);
    if (!t.trim()) { setRead(null); preview("", tz); return; }
    timer.current = setTimeout(() => preview(t, tz), 250);
  };
  const at = whenNext(value, now, tz);
  const repeatId = value?.kind === "repeat" ? value.rule.freq : "never";
  const date = at ? dateInputValue(at, tz) : dateInputValue(now + 86400000, tz);
  const time = at ? clock(at, tz) : "09:00";
  const setPick = (d, t, rep) => {
    setText(""); setRead(null);
    const w = pickedWhen(d, t, rep, tz);
    if (w) onChange(w);
  };

  return (
    <div class={`sch-when${phone ? " is-phone" : ""}`} onClick={(e) => e.stopPropagation()}>
      <label class={`tk-search sch-when-field${phone ? " is-phone" : ""}`}>
        <AlarmClock size={phone ? 16 : 15} aria-hidden="true" />
        <input
          ref={field}
          class="tk-field"
          placeholder="Tomorrow at 3, every Monday at 9…"
          aria-label="When"
          value={text}
          onInput={(e) => onText(e.currentTarget.value)}
          onKeyDown={(e) => { if (e.key === "Enter" && onSubmit && value && !e.metaKey && !e.ctrlKey) { e.preventDefault(); onSubmit(); } }}
        />
      </label>
      <div class="sch-when-read" role="status">
        {read?.error && text.trim() ? (
          <span class="sch-when-err">{read.error}</span>
        ) : value ? (
          <>
            <span class="sch-when-v">{value.kind === "repeat" ? ruleText(value.rule) : whenLong(value.at, tz)}</span>
            <span class="tk-data sch-when-in">{value.kind === "repeat" ? (at ? `next ${whenShort(at, now, tz)}` : "") : inWords(value.at, now)}</span>
            {read?.adjusted && <span class="sch-when-in">Moved by the clock change</span>}
            {read?.alt && (
              <button
                type="button"
                class="tk-link is-accent sch-when-alt"
                onClick={() => { const alt = read.alt; setRead({ ...read, alt: value.kind === "once" ? value.at : null }); onChange({ kind: "once", at: alt }); }}
              >
                {clock(read.alt, tz)} instead
              </button>
            )}
          </>
        ) : (
          <span class="sch-when-err">Type when, or pick below.</span>
        )}
      </div>
      {!text && (
        <div class="sch-when-presets">
          {presets(now, tz).map((p) => (
            <button key={p.label} type="button" class={`tk-chip${value?.kind === "once" && value.at === p.at ? " is-on" : ""}`} onClick={() => { setText(""); setRead(null); onChange({ kind: "once", at: p.at }); }}>
              {p.label}<span class="tk-data sch-chip-t">{whenShort(p.at, now, tz).replace(/^(Today|Tomorrow) /, "")}</span>
            </button>
          ))}
        </div>
      )}
      <div class="sch-when-pick">
        <label class="sch-pick">
          <span class="sch-pick-k">Date</span>
          <input type="date" class="sch-pick-in" value={date} onInput={(e) => setPick(e.currentTarget.value, time, repeatId)} />
        </label>
        <label class="sch-pick is-time">
          <span class="sch-pick-k">Time</span>
          <input type="time" class="sch-pick-in" value={time} onInput={(e) => setPick(date, e.currentTarget.value, repeatId)} />
        </label>
        <label class="sch-pick is-repeat">
          <span class="sch-pick-k">Repeat</span>
          <select class="sch-pick-in" value={repeatId} onChange={(e) => setPick(date, time, e.currentTarget.value)}>
            {repeatOptions(at || now + 86400000, tz).map((o) => <option key={o.id} value={o.id}>{o.label}</option>)}
          </select>
        </label>
      </div>
      <div class="sch-when-tz"><Globe size={12} aria-hidden="true" />{tz} · this device</div>
    </div>
  );
}

// ── Delivery: like an event hook's routing, folded ──────────────────────

// useDefaultModel — the model a new session would get: the server's default,
// as New session and the owners' form read it.
function useDefaultModel() {
  const catalog = useStore(modelCatalog);
  const [def, setDef] = useState("");
  useEffect(() => { ensureModelCatalog(); }, []);
  const models = catalog.entries || [];
  useEffect(() => {
    let live = true;
    api("GET", "/api/capabilities").catch(() => ({})).then((caps) => { if (live) setDef(caps?.defaultModel || models[0]?.id || ""); });
    return () => { live = false; };
  }, [models.length]);
  return { models, def };
}

const THINKING = ["low", "medium", "high"];

export function Delivery({ value, onChange, target, onTarget, open: open0 = false, phone }) {
  const [open, setOpen] = useState(open0);
  const { models } = useDefaultModel();
  const rows = deliveryRows(target);
  const custom = !isDefaultDelivery(value);
  return (
    <section class={`tk-block sch-deliv${phone ? " is-phone" : ""}`} aria-label="Delivery">
      <button type="button" class="tk-block-h sch-deliv-h" aria-expanded={open} onClick={() => setOpen(!open)}>
        <ChevronRight size={13} class={`sch-chev${open ? " is-open" : ""}`} aria-hidden="true" />
        Delivery
        {!open && custom && <span class="sch-deliv-dot" aria-label="Changed" />}
      </button>
      {!open && <button type="button" class="sch-deliv-sum" onClick={() => setOpen(true)}>{deliverySummary(value, target, models)}</button>}
      {open && (
        <div class="sch-deliv-rows">
          {target?.kind === "new" && (
            <>
              <div class="sch-opt">
                <span class="sch-opt-k">Model</span>
                <select class="sch-pick-in" aria-label="Model" value={target.model} onChange={(e) => onTarget?.({ ...target, model: e.currentTarget.value })}>
                  {!models.some((m) => m.id === target.model) && <option value={target.model}>{modelLabel(target.model, models)}</option>}
                  {models.map((m) => <option key={m.id} value={m.id}>{m.name || m.id}</option>)}
                </select>
              </div>
              <div class="sch-opt">
                <span class="sch-opt-k">Thinking</span>
                <span class="sch-seg" role="radiogroup" aria-label="Thinking">
                  {THINKING.map((l) => (
                    <button key={l} type="button" role="radio" aria-checked={target.thinking === l} class={`sch-seg-b${target.thinking === l ? " is-on" : ""}`} onClick={() => onTarget?.({ ...target, thinking: l })}>{l}</button>
                  ))}
                </span>
              </div>
            </>
          )}
          {rows.map((r) => (
            <div class="sch-opt" key={r.key}>
              <span class="sch-opt-k">{r.label}</span>
              <span class="sch-seg" role="radiogroup" aria-label={r.label}>
                {r.opts.map(([v, l]) => (
                  <button key={v} type="button" role="radio" aria-checked={value[r.key] === v} class={`sch-seg-b${value[r.key] === v ? " is-on" : ""}`} onClick={() => onChange({ ...value, [r.key]: v })}>{l}</button>
                ))}
              </span>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

// ── Runs of a recurring task ────────────────────────────────────────────

function Runs({ detail, sessions, tz, onOpenSession }) {
  const rows = runRows(detail);
  if (!rows.length) return null;
  return (
    <section class="tk-block sch-runs" aria-label="Runs">
      <div class="tk-block-h">Runs</div>
      {rows.map((r) => {
        const sid = r.run ? runOpenSession(r.run) : "";
        const go = sid && sessions[sid] && onOpenSession && (r.word === "Done" || r.word === "Working");
        return (
          <div class={`sch-run${r.id === "next" ? " is-next" : ""}`} key={r.id}>
            <span class="sch-run-at tk-data">{dateLabel(r.at, tz)}</span>
            <span class={`sch-run-s is-${r.cls}`}>{r.word}</span>
            <span class="sch-run-note">{r.note}</span>
            {go && (
              <button type="button" class="tk-icon is-quiet sch-go" aria-label={`Open ${sessionName(sessions, sid)}`} title="Open the session" onClick={() => onOpenSession(sid)}>
                <ArrowUpRight aria-hidden="true" />
              </button>
            )}
          </div>
        );
      })}
    </section>
  );
}

// ── Send to ──────────────────────────────────────────────────────────────

// TargetList — Send to reuses Move's levels (owners, recent sessions,
// projects) without You or a backlog, and a project offers a new session in
// each of its directories. `reroute` offers existing sessions only.
export function TargetList({ target, phone, reroute = false, onPick, level, onLevel }) {
  const slice = useStore(tasksSlice);
  const sessions = useStore(selectSessionDirectory);
  const owners = useStore((st) => ownersSlice(st).list);
  useEffect(() => { loadTaskProjects(); }, []);
  return (
    <MoveList
      task={null}
      target={target}
      mode={reroute ? "reroute" : "target"}
      phone={phone}
      direct
      projects={projectOptions(slice.projects, slice.list)}
      sessions={sessions}
      owners={owners}
      level={level}
      onLevel={onLevel}
      onPick={onPick}
    />
  );
}

// ── The detail ───────────────────────────────────────────────────────────

// SchedDetail — a scheduled task's detail IS its editor, with TaskDetail's
// anatomy: title, details, When, Send to, who set it, Delivery (folded), the
// runs of a recurring one, subtasks. A new one is scheduled from its foot.
// Saving sends the revision the edit started from; a 409 shows the task as it
// is now and keeps what was typed.
export function SchedDetail({
  taskId = null, isNew = false, phone = false, keys = false, init = {}, hereSessionId = null,
  onClose, onCreated, onOpenSession, onPushWhen, onPushTarget, onPushReroute,
}) {
  const slice = useStore(tasksSlice);
  const sessions = useStore(selectSessionDirectory);
  const owners = useStore((st) => ownersSlice(st).list);
  const now = useNow();
  const tz = deviceZone();
  const rec = isNew ? null : (slice.details[taskId] || slice.list.find((t) => t.id === taskId)
    || Object.values(slice.bySession).flatMap((d) => d.scheduled || []).find((t) => t.id === taskId) || null);
  useEffect(() => (isNew ? undefined : watchTask(taskId)), [taskId, isNew]);
  const { def: defModel } = useDefaultModel();

  const draftKey = isNew ? "new" : taskId;
  const [stashed] = useState(() => takeDraft(draftKey));
  const [base, setBase] = useState(stashed?.base || rec);
  const [draft, setDraft] = useState(() => {
    if (stashed?.draft?.when !== undefined || stashed?.draft?.target !== undefined) return { ...schedDraft(null), ...stashed.draft };
    if (isNew) return { ...schedDraft(null), target: hereSessionId ? { kind: "session", id: hereSessionId } : null, ...(init.draft || {}) };
    return schedDraft(rec);
  });
  const [pop, setPop] = useState(init.pop || null); // 'when' | 'target' | 'reroute'
  const [sub, setSub] = useState("");
  const [busy, setBusy] = useState(false);
  const [conflict, setConflict] = useState(false);
  useEscape(!!pop, () => setPop(null));

  const dirty = !isNew && !!base && schedDirty(draft, base);
  useEffect(() => {
    if (isNew || !rec || rec.gone) return;
    if (!base || (!dirty && rec !== base)) { setBase(rec); setDraft(schedDraft(rec)); }
  }, [rec, isNew]);

  const task = isNew ? null : (rec || base);
  const tSession = targetSessionId(draft.target, owners);
  const openable = !!tSession && !!sessions[tSession] && tSession !== hereSessionId && !!onOpenSession;
  const keysRef = useRef({});
  keysRef.current = { open: openable ? () => onOpenSession(tSession) : null };
  useEffect(() => {
    if (!keys || phone) return undefined;
    const onKey = (e) => {
      if (isTypingTarget(e.target) || hasBlockingOverlay() || e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey) return;
      if ((e.key === "o" || e.key === "O") && keysRef.current.open) { e.preventDefault(); keysRef.current.open(); }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [keys, phone]);

  if (!isNew && rec?.gone) {
    return <div class={`tk-detail${phone ? " is-phone" : ""}`}><div class="tk-detail-body"><p class="tk-empty-t">This task is no longer available.</p></div></div>;
  }
  if (!isNew && !task) return <div class={`tk-detail${phone ? " is-phone" : ""}`} aria-busy="true" />;

  const set = (p) => setDraft({ ...draft, ...p });
  const stash = () => stashDraft(draftKey, { base, draft });
  const openWhen = () => { if (onPushWhen) { stash(); onPushWhen(); } else setPop(pop === "when" ? null : "when"); };
  const openTarget = () => { if (onPushTarget) { stash(); onPushTarget(); } else setPop(pop === "target" ? null : "target"); };
  const pickTarget = (dest) => {
    const prev = draft.target?.kind === "new" ? draft.target : null;
    set({ target: targetFromDest(dest, { model: prev?.model || defModel, thinking: prev?.thinking || "medium" }) });
    setPop(null);
  };

  const detail = task ? slice.details[task.id] || task : null;
  const late = lateRun(detail);
  const failRun = failedRun(detail);
  const nextAt = draft.when ? (!dirty && task?.next ? task.next : whenNext(draft.when, now, tz)) : null;
  const ready = !!draft.title.trim() && !!draft.when && !!draft.target && (draft.target.kind !== "new" || !!draft.target.model);

  const save = async () => {
    setBusy(true);
    try {
      const next = await patchTask(base.id, schedulePatch(draft, base, tz));
      setBase(next); setDraft(schedDraft(next)); setConflict(false);
    } catch (error) {
      const current = conflictCurrent(error);
      if (current) { setDraft(rebaseSchedDraft(draft, base, current)); setBase(current); setConflict(true); } else failed("Could not save the task", error);
    } finally {
      setBusy(false);
    }
  };
  const create = () => {
    if (!ready || busy) return;
    setBusy(true);
    createTask(scheduleBody(draft, tz))
      .then((r) => onCreated?.(r?.id))
      .catch((error) => failed("Could not schedule the task", error))
      .finally(() => setBusy(false));
  };
  const act = (title, run) => {
    setBusy(true);
    run().catch((error) => failed(title, error)).finally(() => setBusy(false));
  };
  const remove = () => deleteTaskAt(deletePath(task), task.id).then(() => onClose?.()).catch((error) => failed("Could not delete the task", error));
  const reroute = (dest) => {
    setPop(null);
    if (!failRun || !dest?.sessionId) return;
    act("Could not send it", () => rerouteRun(failRun, dest.sessionId, task.id));
  };

  const del = <button type="button" class="tk-icon" aria-label="Delete task" disabled={busy} onClick={remove}><Trash2 size={15} aria-hidden="true" /></button>;
  let foot;
  if (isNew) {
    foot = (
      <>
        <span class="tk-grow" />
        <button type="button" class="zl-ask-btn is-primary" disabled={!ready || busy} onClick={create}>
          {draft.when ? `Schedule for ${whenButton(draft.when, now, tz)}` : "Schedule"}
          {!phone && <Keycap>{MOD_ENTER}</Keycap>}
        </button>
      </>
    );
  } else if (dirty) {
    foot = (
      <>
        <button type="button" class="zl-ask-btn is-quiet" disabled={busy} onClick={() => { setDraft(schedDraft(base)); setConflict(false); }}>Discard</button>
        <span class="tk-grow" />
        {isRepeat(task) && draft.when?.kind === "repeat" && nextAt && <span class="tk-foot-fact">From {whenShort(nextAt, now, tz)} on</span>}
        <button type="button" class="zl-ask-btn is-primary" disabled={busy || !draft.title.trim() || !draft.target} onClick={save}>Save</button>
      </>
    );
  } else {
    const acts = schedActions(task);
    foot = (
      <>
        {del}
        <span class="tk-grow" />
        {acts.includes("lateSkip") && <button type="button" class="zl-ask-btn" disabled={busy || !late} onClick={() => act("Could not skip the run", () => confirmRun(late, "skip", task.id))}>Skip</button>}
        {acts.includes("lateRun") && <button type="button" class="zl-ask-btn is-primary" disabled={busy || !late} onClick={() => act("Could not run it", () => confirmRun(late, "run", task.id))}><Play size={14} aria-hidden="true" />Run now</button>}
        {acts.includes("reroute") && canReroute(failRun) && (
          <button type="button" class="zl-ask-btn is-primary" disabled={busy} onClick={() => (onPushReroute ? onPushReroute(failRun) : setPop(pop === "reroute" ? null : "reroute"))}>Send to another session</button>
        )}
        {acts.includes("resume") && <button type="button" class="zl-ask-btn is-primary" disabled={busy} onClick={() => act("Could not resume it", () => resumeSchedule(task))}><Play size={14} aria-hidden="true" />Resume</button>}
        {acts.includes("pause") && <button type="button" class="zl-ask-btn" disabled={busy} onClick={() => act("Could not pause it", () => pauseSchedule(task))}><Pause size={14} aria-hidden="true" />Pause</button>}
        {acts.includes("skip") && <button type="button" class="zl-ask-btn" disabled={busy || !task.next} title="Skips the next run only" onClick={() => act("Could not skip the next run", () => skipScheduleNext(task))}>Skip next</button>}
        {(acts.includes("runNow") || acts.includes("sendNow")) && (
          <button type="button" class="zl-ask-btn" disabled={busy || !task.next} onClick={() => act("Could not run it", () => runScheduleNow(task))}>{acts.includes("runNow") ? "Run now" : "Send now"}</button>
        )}
      </>
    );
  }

  const s = task ? stateOf(task) : "scheduled";
  const failAt = failRun?.at || (task?.when?.kind === "once" ? task.when.at : 0);
  const tName = draft.target ? targetName(draft.target, sessions, owners) : "Pick where it goes";

  return (
    <div class={`tk-detail tk-root sch-detail${phone ? " is-phone" : ""}`}>
      <div class="tk-detail-body">
        {conflict && <p class="tk-conflict" role="status">Changed elsewhere. This is the latest version, with your edits kept.</p>}
        {!isNew && s === "late" && (
          <div class="sch-late" role="status">
            <span class="sch-late-t">{late ? lateBanner(late, now, tz) : "A run is waiting for your OK."}</span>
            <span class="sch-late-q">Run it now?</span>
          </div>
        )}
        {!isNew && s === "failed" && (
          <div class="tk-notice is-failed sch-failed" role="status">
            <span>Couldn't send{failAt ? ` at ${clock(failAt, tz)}` : ""}: {failureWords(task.failure?.reason || failRun?.reason, task.failure?.note || failRun?.note)}.</span>
          </div>
        )}
        {pop === "reroute" && (
          <div class="tk-pop is-move is-inline sch-reroute" role="menu">
            <TargetList reroute onPick={reroute} />
          </div>
        )}
        {!isNew && s === "paused" && (
          <div class="tk-notice" role="status"><span>Paused. It won't run until you resume it.</span></div>
        )}
        <textarea
          class="tk-field tk-title"
          rows={1}
          placeholder="What should it do?"
          aria-label="Title"
          value={draft.title}
          onInput={(e) => { set({ title: e.currentTarget.value }); autosize(e.currentTarget); }}
          onKeyDown={(e) => {
            if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && isNew) { e.preventDefault(); create(); }
            else if (e.key === "Enter" && !e.shiftKey) e.preventDefault();
          }}
          ref={(el) => { autosize(el); if (el && isNew && !init.draft?.when && !el.dataset.focused) { el.dataset.focused = "1"; el.focus({ preventScroll: true }); } }}
        />
        <textarea
          class="tk-field tk-desc"
          rows={1}
          placeholder="Add details"
          aria-label="Details"
          value={draft.description}
          ref={autosize}
          onInput={(e) => { set({ description: e.currentTarget.value }); autosize(e.currentTarget); }}
        />
        <dl class="tk-props">
          <div class="tk-prop">
            <dt>When</dt>
            <dd class="tk-anchor sch-prop-dd">
              <button type="button" class={`tk-prop-btn${draft.when ? "" : " sch-empty"}`} aria-expanded={pop === "when"} onClick={openWhen}>
                {draft.when?.kind === "repeat" ? <Repeat size={13} aria-hidden="true" /> : <Clock3 size={13} aria-hidden="true" />}
                <span class="tk-prop-v">{draft.when ? (draft.when.kind === "repeat" ? ruleText(draft.when.rule) : whenLong(draft.when.at, tz)) : "Pick a time"}</span>
                <ChevronDown size={13} aria-hidden="true" />
              </button>
              {nextAt && (isNew || s === "scheduled") && (
                <span class={`tk-data sch-prop-fact${draft.when?.kind === "repeat" ? " is-under" : ""}`}>{draft.when?.kind === "repeat" ? `next ${whenShort(nextAt, now, tz)}` : inWords(nextAt, now)}</span>
              )}
            </dd>
          </div>
          {pop === "when" && (
            <div class="sch-inline-pop">
              <WhenEditor value={draft.when} text={init.whenText || ""} onChange={(w) => set({ when: w })} onSubmit={() => setPop(null)} />
            </div>
          )}
          <div class="tk-prop">
            <dt>Send to</dt>
            <dd class="tk-anchor sch-prop-dd">
              <button type="button" class={`tk-prop-btn${draft.target ? "" : " sch-empty"}`} aria-expanded={pop === "target"} onClick={openTarget}>
                {draft.target?.kind === "owner" && <span class="sch-kind">Owner</span>}
                <span class={`tk-prop-v${draft.target?.kind === "session" && !sessions[draft.target.id] ? " sch-gone" : ""}`}>{tName}</span>
                <ChevronDown size={13} aria-hidden="true" />
              </button>
              {openable && <OpenSessionButton sessions={sessions} id={tSession} phone={phone} onOpen={onOpenSession} />}
              {pop === "target" && (
                <div class="tk-pop is-move" role="menu">
                  <TargetList target={draft.target} onPick={pickTarget} />
                </div>
              )}
            </dd>
          </div>
          {!isNew && task.created_by_session_id && (
            <div class="tk-prop">
              <dt>Set by</dt>
              <dd>
                <button type="button" class="tk-link" disabled={!onOpenSession} onClick={() => onOpenSession?.(task.created_by_session_id)}>
                  <span class="tk-sdot" aria-hidden="true" />The agent · {sessionName(sessions, task.created_by_session_id)}
                </button>
              </dd>
            </div>
          )}
        </dl>

        <Delivery value={draft.delivery} target={draft.target} open={!!init.deliveryOpen} phone={phone} onChange={(d) => set({ delivery: d })} onTarget={(t) => set({ target: t })} />
        {!isNew && isRepeat(task) && <Runs detail={detail} sessions={sessions} tz={tz} onOpenSession={onOpenSession} />}

        <section class="tk-block" aria-label="Subtasks">
          <div class="tk-block-h">Subtasks{draft.subtasks.length > 0 && <span class="tk-data">{draft.subtasks.length}</span>}</div>
          {draft.subtasks.map((st, i) => (
            <div key={i} class="tk-sub">
              <span class="tk-sub-t">{st.title}</span>
              <button type="button" class="tk-icon is-quiet" aria-label={`Remove ${st.title}`} onClick={() => set({ subtasks: draft.subtasks.filter((_, j) => j !== i) })}><CloseIcon /></button>
            </div>
          ))}
          <label class="tk-sub is-add">
            <Plus size={14} aria-hidden="true" />
            <input
              class="tk-field"
              placeholder="Add subtask"
              aria-label="Add subtask"
              value={sub}
              onInput={(e) => setSub(e.currentTarget.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && sub.trim()) {
                  e.preventDefault();
                  set({ subtasks: [...draft.subtasks, { title: sub.trim(), done: false }] });
                  setSub("");
                }
              }}
            />
          </label>
        </section>
      </div>
      <div class="tk-foot">{foot}</div>
    </div>
  );
}

// schedTitle — the dossier head over a scheduled task.
export function schedTitle(t) {
  return schedEyebrow(t);
}

// ── Phone pages ──────────────────────────────────────────────────────────

// WhenPage — When as a page of the phone's sheet. The editor left its draft
// behind (stashDraft); Done writes the time into that draft and goes back,
// where the editor takes it up again unsaved.
export function WhenPage({ draftKey, onBack }) {
  const entry = peekDraft(draftKey);
  const [when, setWhen] = useState(entry?.draft?.when || null);
  const repeat = entry?.base?.when?.kind === "repeat";
  return (
    <div class="tk-phone-body tk-root sch-when-page">
      <WhenEditor phone value={when} onChange={setWhen} autoFocus={false} />
      {repeat && <p class="sch-page-fact">Changes apply from the next run.</p>}
      <div class="sch-page-foot">
        <button type="button" class="zl-ask-btn is-primary tk-wide" disabled={!when} onClick={() => { patchDraft(draftKey, { when }); onBack(); }}>Done</button>
      </div>
    </div>
  );
}

// TargetPage — Send to as a page: Move's levels, a project a page of its own
// when `onLevel` is given.
export function TargetPage({ draftKey, level, onLevel, onDone }) {
  const entry = peekDraft(draftKey);
  const { def } = useDefaultModel();
  const prev = entry?.draft?.target;
  return (
    <div class="tk-phone-body tk-root">
      <TargetList
        phone
        target={prev}
        level={level}
        onLevel={onLevel}
        onPick={(dest) => {
          const keep = prev?.kind === "new" ? prev : null;
          patchDraft(draftKey, { target: targetFromDest(dest, { model: keep?.model || def, thinking: keep?.thinking || "medium" }) });
          onDone();
        }}
      />
    </div>
  );
}

// ReroutePage — "Send to another session" for a run that was not sent.
export function ReroutePage({ taskId, onDone }) {
  const slice = useStore(tasksSlice);
  const run = failedRun(slice.details[taskId]);
  return (
    <div class="tk-phone-body tk-root">
      <TargetList
        phone
        reroute
        onPick={(dest) => {
          if (!run || !dest?.sessionId) return;
          rerouteRun(run, dest.sessionId, taskId).then(onDone).catch((error) => failed("Could not send it", error));
        }}
      />
    </div>
  );
}

// ── Over the composer ────────────────────────────────────────────────────

// SchedPinBar — what is scheduled into this session, in the line over the
// composer: quieter than "For you" (nothing is asked of you), amber only when
// a run waits for your OK.
export function SchedPinBar({ pin, phone, onOpenTask }) {
  const t = pin.task;
  return (
    <div class={`tk-pin sch-pin${phone ? " is-phone" : ""}${pin.late ? " is-attn" : ""}`} role="region" aria-label="Scheduled here">
      <div class="tk-pin-bar">
        <span class="sch-pin-ico">{isRepeat(t) ? <Repeat size={14} aria-hidden="true" /> : <Clock3 size={14} aria-hidden="true" />}</span>
        <span class="tk-pin-k">{pin.key}</span>
        <button type="button" class="tk-pin-t" onClick={() => onOpenTask?.(t.id)}>{t.title}</button>
        {pin.byAgent && !phone && <span class="sch-pin-by">by the agent</span>}
        <span class="tk-data sch-pin-when">{pin.when}</span>
        {pin.more > 0 && <span class="tk-pin-more tk-data" aria-label={`${pin.more} more`}>+{pin.more}</span>}
      </div>
    </div>
  );
}
