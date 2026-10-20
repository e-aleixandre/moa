import { useEffect, useMemo, useState } from "preact/hooks";
import { ChevronDown, ChevronRight, Pause, Play, Trash2, Repeat, Clock3, AlertTriangle } from "lucide-preact";
import { useStore } from "../../hooks/useStore.js";
import { addToast } from "../../data/notifications.js";
import { ownersSlice } from "../../data/owners.js";
import { deleteTaskAt, keepSchedule, pauseSchedule, resumeSchedule, tasksSlice } from "../../data/tasks.js";
import { deletePath, errorText } from "../../data/tasks-model.js";
import { deviceZone, isRepeat, lateRun, schedRight, stateOf } from "../../data/schedule-model.js";
import {
  byProject, bucketOf, costWords, creatorWords, lastRunWords, nextWords, openTemplates, projectOf, recurrenceWords, runsWords,
  staleKind, staleWords, watchList,
} from "../../data/scheduled-visible.js";
import { useNow } from "./Scheduled.jsx";
import "./ScheduledVisible.css";

// Scheduled tasks always in sight (docs/serve.md, Scheduled tasks): the
// Scheduled section of Tasks, by project, and For you's "Running on its own".
// What decides lives in data/scheduled-visible.js.

function fail(title, error) {
  addToast({ title, detail: errorText(error), type: "error" });
}

// RowActions — Pause/Resume (a recurring task) and Delete on the row itself.
// Delete asks once, in place.
function RowActions({ t, phone }) {
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (!confirm) return undefined;
    const id = setTimeout(() => setConfirm(false), 3500);
    return () => clearTimeout(id);
  }, [confirm]);
  const paused = stateOf(t) === "paused";
  const stale = !!staleKind(t);
  const stop = (e) => e.stopPropagation();
  const toggle = (e) => {
    stop(e);
    setBusy(true);
    (paused ? resumeSchedule(t) : pauseSchedule(t)).catch((err) => fail(paused ? "Could not resume" : "Could not pause", err)).finally(() => setBusy(false));
  };
  const del = (e) => {
    stop(e);
    if (!confirm) { setConfirm(true); return; }
    setBusy(true);
    deleteTaskAt(deletePath(t), t.id).catch((err) => { fail("Could not delete", err); setBusy(false); });
  };
  return (
    <span class={`sv-acts${phone ? " is-phone" : ""}`} onClick={stop}>
      {!stale && isRepeat(t) && (
        <button type="button" class="sv-act" disabled={busy} onClick={toggle} aria-label={paused ? `Resume ${t.title}` : `Pause ${t.title}`}>
          {paused ? <Play size={14} aria-hidden="true" /> : <Pause size={14} aria-hidden="true" />}
          {!phone && <span class="sv-act-t">{paused ? "Resume" : "Pause"}</span>}
        </button>
      )}
      <button type="button" class={`sv-act is-del${confirm ? " is-confirm" : ""}`} disabled={busy} onClick={del} aria-label={confirm ? `Confirm delete ${t.title}` : `Delete ${t.title}`}>
        <Trash2 size={14} aria-hidden="true" />
        {(!phone || confirm) && <span class="sv-act-t">{confirm ? "Delete?" : "Delete"}</span>}
      </button>
    </span>
  );
}

// AgendaRow — what it does, when it fires next, how often, who set it and in
// which project, and how its last run went.
export function AgendaRow({ t, phone, onSelect, selected, showProject = true, now, tz, owners, details }) {
  const stale = staleKind(t);
  const paused = stateOf(t) === "paused";
  const late = stateOf(t) === "late";
  const repeat = isRepeat(t);
  const I = stale || late ? AlertTriangle : paused ? Pause : repeat ? Repeat : Clock3;
  const runs = runsWords(t, tz);
  const meta = stale
    ? [staleWords(t, now, tz), showProject && projectOf(t), creatorWords(t, owners)]
    : [late && <span class="sv-wait">Waiting for you</span>, recurrenceWords(t, tz), showProject && projectOf(t), creatorWords(t, owners)];
  const last = stale || (!repeat && !t.run_stats) ? null : [lastRunWords(t, now, tz), runs, (repeat || t.run_stats?.ran) ? costWords(t) : ""].filter(Boolean);
  const next = stale ? "" : late ? schedRight(t, now, tz, lateRun(details?.[t.id])) : nextWords(t, now, tz);
  return (
    <div
      class={`sv-row${phone ? " is-phone" : ""}${selected ? " is-selected" : ""}${paused ? " is-paused" : ""}${stale ? " is-stale" : ""}${late ? " is-late" : ""}${repeat ? " is-repeat" : ""}`}
      data-task={t.id}
      role="option"
      aria-selected={!!selected}
      tabIndex={-1}
      onClick={() => onSelect?.(t.id)}
    >
      <span class="sv-glyph" aria-hidden="true"><I size={15} strokeWidth={2} /></span>
      <span class="sv-main">
        <span class="sv-top">
          <span class="sv-title">{t.title}</span>
          {next && !phone && <span class={`sv-next${paused ? " is-paused" : ""}`}>{next}</span>}
        </span>
        {phone && next && <span class={`sv-next${paused ? " is-paused" : ""}`}>{repeat || paused ? next : `Next ${next}`}</span>}
        <span class="sv-meta">{meta.filter(Boolean).map((m, i) => <>{i > 0 && <span class="sv-sep">·</span>}<span class={m === recurrenceWords(t, tz) && repeat ? "sv-rule" : ""}>{m}</span></>)}</span>
        {last && last.length > 0 && <span class="sv-meta sv-last">{last.map((m, i) => <>{i > 0 && <span class="sv-sep">·</span>}<span class={m === "cost unknown" ? "sv-unknown" : ""}>{m}</span></>)}</span>}
      </span>
      <RowActions t={t} phone={phone} />
    </div>
  );
}

function useCtx() {
  const owners = useStore((st) => ownersSlice(st).list);
  const details = useStore((st) => tasksSlice(st).details);
  const now = useNow();
  const tz = deviceZone();
  return { owners, now, tz, details };
}

function Fold({ label, n, open, setOpen, attn, phone }) {
  return (
    <button type="button" class={`sv-fold${attn ? " is-attn" : ""}${phone ? " is-phone" : ""}`} aria-expanded={open} onClick={() => setOpen(!open)}>
      {open ? <ChevronDown size={14} aria-hidden="true" /> : <ChevronRight size={14} aria-hidden="true" />}
      <span>{label}</span><span class="sv-fold-n">{n}</span>
    </button>
  );
}

function Summary({ rows }) {
  const rec = rows.filter((t) => bucketOf(t) === "recurring");
  const up = rows.filter((t) => bucketOf(t) === "upcoming");
  const stale = rows.filter((t) => bucketOf(t) === "stale");
  const waiting = rows.filter((t) => stateOf(t) === "late").length;
  const parts = [];
  if (waiting) parts.push(<span class="sv-sum-attn">{waiting} waiting for you</span>);
  if (rec.length) parts.push(<span class="sv-sum-rec">{rec.length} recurring{rec.some((t) => stateOf(t) !== "paused") ? ` · ${rec.filter((t) => stateOf(t) !== "paused").length} active` : ""}</span>);
  if (up.length) parts.push(<span>{up.length} upcoming</span>);
  if (stale.length) parts.push(<span class="sv-sum-attn">{stale.length} didn't run</span>);
  return <span class="sv-sum">{parts.map((p, i) => <>{i > 0 && <span class="sv-sep">·</span>}{p}</>)}</span>;
}

// ScheduledByProject — Scheduled on top of Tasks, grouped by project: what
// waits for you and every recurring task always shown (never behind "Show
// all"), two once tasks per project then "N more", and the once tasks that
// didn't run folded at the end, until the user deletes them.
export function ScheduledByProject({ list, phone, onSelect, selected, single }) {
  const ctx = useCtx();
  const rows = openTemplates(list);
  const live = rows.filter((t) => bucketOf(t) !== "stale");
  const stale = rows.filter((t) => bucketOf(t) === "stale");
  const [more, setMore] = useState({});
  const [staleOpen, setStaleOpen] = useState(false);
  const groups = useMemo(() => byProject(live), [live.map((t) => `${t.id}:${t.revision}`).join(",")]);
  if (!rows.length) return null;
  return (
    <div class={`sv-wrap${phone ? " is-phone" : ""}`} role="listbox" aria-label="Scheduled">
      <div class="sv-head"><span class="sv-head-t">Scheduled</span><Summary rows={rows} /></div>
      {groups.map((g) => {
        const pinned = (t) => isRepeat(t) || stateOf(t) === "late";
        const rec = g.rows.filter(pinned);
        const once = g.rows.filter((t) => !pinned(t));
        const limit = more[g.name] ? once.length : 2;
        return (
          <section class="sv-proj" key={g.name}>
            {!single && <div class="sv-proj-t">{g.name}</div>}
            {rec.map((t) => <AgendaRow key={t.id} t={t} phone={phone} onSelect={onSelect} selected={selected === t.id} showProject={false} {...ctx} />)}
            {once.slice(0, limit).map((t) => <AgendaRow key={t.id} t={t} phone={phone} onSelect={onSelect} selected={selected === t.id} showProject={false} {...ctx} />)}
            {once.length > limit && (
              <button type="button" class={`sv-more${phone ? " is-phone" : ""}`} onClick={() => setMore({ ...more, [g.name]: true })}>
                {once.length - limit} more in {g.name}
              </button>
            )}
          </section>
        );
      })}
      {stale.length > 0 && (
        <section class="sv-proj">
          <Fold label="Didn't run" n={stale.length} open={staleOpen} setOpen={setStaleOpen} attn phone={phone} />
          {staleOpen && stale.map((t) => <AgendaRow key={t.id} t={t} phone={phone} onSelect={onSelect} selected={selected === t.id} {...ctx} />)}
        </section>
      )}
    </div>
  );
}

// RecurringWatch — For you's "Running on its own": a recurring task an agent
// set, that has run three times or more, asks once whether to keep it.
export function RecurringWatch({ phone }) {
  const list = useStore((st) => tasksSlice(st).list);
  const ctx = useCtx();
  const rows = watchList(list);
  if (!rows.length) return null;
  return (
    <section class="fy-sec sv-watch">
      <div class="fy-sec-head"><span class="fy-sec-t">Running on its own</span></div>
      {rows.map((t) => (
        <div class={`sv-card${phone ? " is-phone" : ""}`} key={t.id}>
          <div class="sv-card-t"><Repeat size={15} aria-hidden="true" />{t.title}</div>
          <div class="sv-card-b">
            {recurrenceWords(t, ctx.tz)} since {new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "short", timeZone: ctx.tz }).format(t.run_stats.first_at)}
            {" · "}{t.run_stats.ran} runs · <span class="sv-unknown">{costWords(t)}</span>
          </div>
          <div class="sv-card-b">{projectOf(t)} · {creatorWords(t, ctx.owners)} · next {nextWords(t, ctx.now, ctx.tz)}</div>
          <div class="sv-card-acts">
            <button type="button" class="sv-btn" onClick={() => keepSchedule(t).catch((err) => fail("Could not keep it", err))}>Keep running</button>
            <RowActions t={t} phone={phone} />
          </div>
        </div>
      ))}
    </section>
  );
}
