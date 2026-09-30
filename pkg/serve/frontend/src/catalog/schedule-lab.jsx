import { createPortal } from "preact/compat";
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "preact/hooks";
import {
  AlarmClock, ArrowUpRight, Check, ChevronDown, ChevronRight, Clock3, Globe, Paperclip, Pause, Play, Plus, Repeat, Search, Trash2,
} from "lucide-preact";
import { DesktopShell, ConversationScreen, MobileConversationScreen } from "../layout/index.js";
import { ScreenLab, PHONE_LAB_WIDTH, PHONE_LAB_HEIGHT } from "./desktop-lab.jsx";
import { MobileSheet } from "../layout/mobile/MobileSheet/MobileSheet.jsx";
import { setState, store } from "../data/store.js";
import { setTileSession } from "../data/tileTree.js";
import { closeSessionPanel, openSessionPanel } from "../data/session-panel.js";
import { getToasts, removeToast, subscribeToasts } from "../data/notifications.js";
import { projectName } from "../data/util/format.js";
import { formatShortcut } from "../data/util/shortcut.js";
import { useStore } from "../hooks/useStore.js";
import { selectSessionDirectory, tasksSlice, TASKS_INITIAL } from "../data/tasks.js";
import { groupTasks, openOwnCount, projectOptions } from "../data/tasks-model.js";
import { BackIcon, CloseIcon, Filters, Keycap, MOD_ENTER, TaskGroups, completesInline, useEscape, useRowChecks } from "../components/Tasks/parts.jsx";
import { TaskDetail, taskEyebrow, useTaskLookup } from "../components/Tasks/TaskDetail.jsx";
import {
  dateInputValue, dateLabel, deviceZone, fromInputs, inWords, parseWhen, presets, ruleShort, ruleText, timeInputValue, wallOf, whenLong, whenShort,
  WEEKDAY_NAMES, nextOf,
} from "./schedule-lab-time.js";
import {
  DEFAULT_DELIVERY, LAB_NOW, OWNERS, PROJECTS, labScheduled, labSessions, labTasks, nextRun,
} from "./schedule-lab-data.js";
import { OWNERS as OWNER_FIXTURES } from "./owners-fixtures.js";
import "./schedule-lab.css";

/* Scheduled and recurring tasks (?view=schedule, model B).

   The chrome is production: DesktopShell, ConversationScreen,
   MobileConversationScreen, MobileSheet, SessionPanel, and the Tasks pieces
   that shipped with global tasks (Filters, TaskGroups, TaskDetail,
   PinnedTaskLine), fed through the store and a stub of /api/tasks. What is
   new — the Scheduled group, the scheduled task's detail with When, Send to
   and Delivery, the runs of a recurring one, the line over the composer and
   Send later — is drawn by this file in the tk-* classes it sits next to, and
   portalled into the chrome where it has to live. Nothing in production is
   edited. One screen per URL: scene × device × variant are query params. */

const params = new URLSearchParams(typeof location === "undefined" ? "" : location.search);
const lab = {
  scene: params.get("scene") || "list",
  device: params.get("device") || "desktop",
  list: params.get("list") || "group", // group | agenda
  composer: params.get("composer") || "clock", // clock | plus
  tz: params.get("tz") || deviceZone(),
};
const TZ = lab.tz;
// Wide enough for the dossier to dock beside the list (DesktopShell.css:64
// docks it from a 1456px viewport), as on the owner's screen.
const WIDE_WIDTH = 1480;
const WIDE_HEIGHT = 860;
const NOW = LAB_NOW;

// ── Lab store ───────────────────────────────────────────────────────────────

let scheduled = [];
const listeners = new Set();
const emit = () => listeners.forEach((fn) => fn(scheduled));
function setScheduled(next) { scheduled = typeof next === "function" ? next(scheduled) : next; emit(); }
function patchSched(id, p) { setScheduled((ss) => ss.map((s) => (s.id === id ? { ...s, ...p, next: nextRun({ ...s, ...p }, TZ) } : s))); }
function useScheduled() {
  const [v, setV] = useState(scheduled);
  useEffect(() => { listeners.add(setV); return () => listeners.delete(setV); }, []);
  return v;
}
const byId = (id) => scheduled.find((s) => s.id === id);

// A late run waits for the owner, so the Tasks number in the sidebar foot
// counts it (owner, 30/09). The foot counts open requests from the store's
// list; the lab lends it one request-shaped record per late task, which the
// lists below never draw.
function lateStandIns(ss) {
  return ss.filter((s) => s.state === "late").map((s, i) => ({
    id: 9000 + i, _sched: s.id, title: s.title, place: "you", status: "pending", requester_session_id: targetSessionId(s) || "system",
    created_at: Date.now(), updated_at: Date.now(), subtasks: [], waits_for: [],
  }));
}

function installTasksApi(list) {
  if (globalThis.__schedLabFetch) return;
  globalThis.__schedLabFetch = true;
  const prev = globalThis.fetch.bind(globalThis);
  const json = (body) => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  globalThis.fetch = async (input, init = {}) => {
    const url = new URL(typeof input === "string" ? input : input.url, location.href);
    const m = (init.method || "GET").toUpperCase();
    const p = url.pathname;
    if (m === "GET" && p === "/api/tasks") return json({ tasks: tasksSlice(store.get()).list, counts: TASKS_INITIAL.counts, revision: 1 });
    if (m === "GET" && p === "/api/tasks/projects") return json({ projects: PROJECTS });
    const one = p.match(/^\/api\/tasks\/(\d+)$/);
    if (m === "GET" && one) return json(tasksSlice(store.get()).list.find((t) => String(t.id) === one[1]) || {});
    const ses = p.match(/^\/api\/sessions\/([^/]+)\/tasks$/);
    if (m === "GET" && ses) {
      const all = tasksSlice(store.get()).list;
      return json({
        requests: all.filter((t) => t.place === "you" && t.requester_session_id === ses[1] && !t._sched),
        checklist: all.filter((t) => t.place === "agent" && t.assignee_session_id === ses[1]),
      });
    }
    return prev(input, init);
  };
  return list;
}

// ── Names ───────────────────────────────────────────────────────────────────

function targetSessionId(s) {
  if (s.target.kind === "session") return s.target.id;
  if (s.target.kind === "owner") return OWNERS.find((o) => o.id === s.target.id)?.session_id;
  return null;
}
function targetName(s, sessions) {
  const t = s.target;
  if (t.kind === "owner") return OWNERS.find((o) => o.id === t.id)?.name || "Owner";
  if (t.kind === "new") return `New session · ${t.project}`;
  return sessions[t.id]?.title || "Deleted session";
}
function targetKindWord(s) {
  if (s.target.kind === "owner") return "Owner";
  if (s.target.kind === "new") return null;
  return null;
}
const isRepeat = (s) => s.when.kind === "repeat";
function whenWords(s) {
  if (isRepeat(s)) return ruleText(s.when.rule);
  return whenLong(s.when.at, TZ);
}
function whenRow(s) {
  if (isRepeat(s)) return ruleShort(s.when.rule);
  return null;
}
function eyebrow(s) {
  if (s.state === "late") return "Waiting for you";
  if (s.state === "failed") return "Not sent";
  if (s.state === "paused") return "Paused";
  return isRepeat(s) ? "Recurring" : "Scheduled";
}

// ── Portals into the production chrome ──────────────────────────────────────

function usePortalHost(selector, { before, prepend, after, display = "contents", cls = "" } = {}) {
  const [host] = useState(() => {
    const el = document.createElement("div");
    el.style.display = display;
    el.className = `sl-portal ${cls}`;
    return el;
  });
  const [target, setTarget] = useState(null);
  useLayoutEffect(() => {
    const place = () => {
      const t = document.querySelector(selector);
      if (!t) { if (host.parentNode) host.remove(); setTarget(null); return; }
      let ref = null;
      if (before) ref = t.querySelector(`:scope > ${before}`);
      else if (after) { const a = t.querySelector(`:scope > ${after}`); ref = a ? a.nextSibling : t.firstChild; }
      else if (prepend) ref = t.firstChild;
      if (ref === host) ref = host.nextSibling === null ? null : ref;
      const wrong = host.parentNode !== t || (ref && ref !== host && host.nextSibling !== ref) || (!ref && !prepend && !before && !after && t.lastChild !== host);
      if (wrong && ref !== host) t.insertBefore(host, ref);
      setTarget(t);
    };
    place();
    const mo = new MutationObserver(place);
    mo.observe(document.querySelector(".sl-stage") || document.body, { childList: true, subtree: true });
    return () => { mo.disconnect(); host.remove(); };
  }, [selector]);
  return target ? host : null;
}

// ── Atoms ──────────────────────────────────────────────────────────────────

function SchedGlyph({ s, big }) {
  const cls = `sl-glyph${s.state === "late" || s.state === "failed" ? " is-attn" : ""}${s.state === "paused" ? " is-paused" : ""}${big ? " is-big" : ""}`;
  const I = s.state === "paused" ? Pause : isRepeat(s) ? Repeat : Clock3;
  return <span class={cls} aria-hidden="true"><I size={big ? 16 : 14} strokeWidth={2} /></span>;
}

function Meta({ items }) {
  return <span class="tk-row-meta">{items.map((m, i) => <>{i > 0 && <span class="tk-sep">·</span>}{m}</>)}</span>;
}

// The Scheduled row: production's row, with the clock where the ring is (a
// scheduled task is not done by you; it runs) and the next run where the age
// is. What waits for you says so in amber, in words.
function SchedRow({ s, sessions, selected, onSelect, phone, context }) {
  const meta = [];
  if (s.state === "late") meta.push(<span class="sl-word-wait">Waiting for you</span>);
  if (s.state === "failed") meta.push(<span class="tk-meta-notice">Not sent</span>);
  if (s.state === "paused") meta.push(<span>Paused</span>);
  if (context !== "session") meta.push(<span class="tk-meta-from">{targetName(s, sessions)}</span>);
  if (s.created_by_session_id) meta.push(<span>Set by the agent</span>);
  const rule = whenRow(s);
  if (rule) meta.push(<span class="tk-data">{rule}</span>);
  const right = s.state === "late" ? `was ${whenShort(s.late.due, NOW, TZ).replace(/^Today /, "")}`
    : s.state === "failed" ? whenShort(s.when.at, NOW, TZ).replace(/^Today /, "")
      : s.next ? (s.next - NOW < 3 * 3600000 ? inWords(s.next, NOW) : whenShort(s.next, NOW, TZ)) : "";
  return (
    <div
      class={`tk-row sl-row${selected ? " is-selected" : ""}${phone ? " is-phone" : ""}${s.state === "paused" ? " is-paused" : ""}`}
      data-task={s.id}
      role="option"
      aria-selected={!!selected}
      tabIndex={-1}
      onClick={() => onSelect(s.id)}
    >
      <div class="tk-row-line">
        <SchedGlyph s={s} big={phone && context !== "session"} />
        <span class="tk-row-main">
          <span class="tk-row-title">{s.title}</span>
          {phone && meta.length > 0 && <Meta items={meta} />}
        </span>
        {!phone && meta.length > 0 && <Meta items={meta} />}
        {right && <span class={`tk-row-age tk-data sl-next${s.state === "late" ? " is-attn" : ""}`}>{right}</span>}
      </div>
    </div>
  );
}

const ORDER = { late: 0, failed: 1, scheduled: 2, paused: 3 };
function sortSched(ss) {
  return [...ss].sort((a, b) => (ORDER[a.state] - ORDER[b.state]) || ((a.next || Infinity) - (b.next || Infinity)));
}

// V1 — Scheduled is a group of the list, on top: what needs you first, then
// by next run; paused at the end. Five rows, then "Show all".
function ScheduledGroup({ list, sessions, selected, onSelect, phone, context, limit = 5, onAdd }) {
  const [all, setAll] = useState(false);
  if (!list.length && !onAdd) return null;
  const rows = sortSched(list);
  const shown = all ? rows : rows.slice(0, limit);
  const waiting = rows.filter((s) => s.state === "late").length;
  return (
    <div class={`tk-list sl-list${phone ? " is-phone" : ""}`} role="listbox" aria-label="Scheduled">
      <section class="tk-sec" aria-label="Scheduled">
        <div class={`tk-group${waiting ? " sl-group-attn" : ""}`}>
          <span class="tk-group-t">Scheduled</span>
          {waiting > 0 && <span class="sl-group-wait">{waiting} waiting for you</span>}
          <span class="tk-group-n tk-data">{rows.length}</span>
        </div>
        {shown.map((s) => (
          <SchedRow key={s.id} s={s} sessions={sessions} selected={selected === s.id} onSelect={onSelect} phone={phone} context={context} />
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

// V2 — Scheduled is a filter: the list turns into an agenda by day.
function Agenda({ list, sessions, selected, onSelect, phone }) {
  const buckets = [
    ["needs", "Needs you", (s) => s.state === "late" || s.state === "failed"],
    ["today", "Today", (s) => s.next && wallOf(s.next, TZ).d === wallOf(NOW, TZ).d && s.next - NOW < 86400000],
    ["tomorrow", "Tomorrow", (s) => s.next && s.next - NOW < 2 * 86400000],
    ["week", "Next 7 days", (s) => s.next && s.next - NOW < 7 * 86400000],
    ["later", "Later", (s) => !!s.next],
    ["paused", "Paused", (s) => s.state === "paused"],
  ];
  const left = new Set(list.map((s) => s.id));
  const groups = buckets.map(([id, title, test]) => {
    const rows = sortSched(list.filter((s) => left.has(s.id) && test(s)));
    rows.forEach((s) => left.delete(s.id));
    return { id, title, rows };
  }).filter((g) => g.rows.length);
  return (
    <div class={`tk-list sl-list${phone ? " is-phone" : ""}`} role="listbox" aria-label="Scheduled">
      {groups.map((g) => (
        <section class="tk-sec" key={g.id} aria-label={g.title}>
          <div class={`tk-group${g.id === "needs" ? " sl-group-attn" : ""}`}>
            <span class="tk-group-t">{g.title}</span>
            <span class="tk-group-n tk-data">{g.rows.length}</span>
          </div>
          {g.rows.map((s) => <SchedRow key={s.id} s={s} sessions={sessions} selected={selected === s.id} onSelect={onSelect} phone={phone} />)}
        </section>
      ))}
    </div>
  );
}

// ── When ───────────────────────────────────────────────────────────────────

function valueOf(s) {
  return s.when.kind === "repeat" ? { kind: "repeat", rule: s.when.rule } : { kind: "once", at: s.when.at };
}

const REPEATS = (at) => {
  const w = wallOf(at, TZ);
  return [
    { id: "never", label: "Never" },
    { id: "daily", label: "Every day", rule: { freq: "daily" } },
    { id: "weekdays", label: "Weekdays", rule: { freq: "weekdays" } },
    { id: "weekly", label: `Every ${WEEKDAY_NAMES[w.dow]}`, rule: { freq: "weekly", dow: w.dow } },
    { id: "monthly", label: `Monthly on day ${w.d}`, rule: { freq: "monthly", dom: w.d } },
  ];
};

// WhenEditor — words first, with the date they resolve to underneath, and
// the three fields (date, time, repeat) always in sync with it. The zone is
// this device's, said once, quietly.
function WhenEditor({ value, onChange, text: text0 = "", phone, autoFocus = true, onSubmit }) {
  const [text, setText] = useState(text0);
  const [read, setRead] = useState(() => (text0 ? parseWhen(text0, NOW, TZ) : null));
  const field = useRef(null);
  useEffect(() => {
    if (autoFocus) field.current?.focus({ preventScroll: true });
    if (read && !read.error && !value) onChange(read.kind === "repeat" ? { kind: "repeat", rule: read.rule } : { kind: "once", at: read.at });
  }, []);
  const onText = (t) => {
    setText(t);
    const r = parseWhen(t, NOW, TZ);
    setRead(r);
    if (r && !r.error) onChange(r.kind === "repeat" ? { kind: "repeat", rule: r.rule } : { kind: "once", at: r.at });
  };
  const at = value ? (value.kind === "repeat" ? nextOf(value.rule, NOW, TZ) : value.at) : null;
  const repeatId = value?.kind === "repeat" ? value.rule.freq : "never";
  const setPick = (date, time, rep) => {
    setText(""); setRead(null);
    const base = fromInputs(date, time, TZ);
    const opt = REPEATS(base).find((o) => o.id === rep);
    if (!opt?.rule) { onChange({ kind: "once", at: base }); return; }
    const w = wallOf(base, TZ);
    onChange({ kind: "repeat", rule: { ...opt.rule, h: w.h, mi: w.mi } });
  };
  const date = at ? dateInputValue(at, TZ) : dateInputValue(NOW + 86400000, TZ);
  const time = at ? timeInputValue(at, TZ) : "09:00";
  const errorWords = read?.error === "past" ? "That time has passed. Pick a later one."
    : read?.error ? "Try “in 20 min”, “friday at 18:00” or “every monday at 9”." : null;

  return (
    <div class={`sl-when${phone ? " is-phone" : ""}`} onClick={(e) => e.stopPropagation()}>
      <label class={`tk-search sl-when-field${phone ? " is-phone" : ""}`}>
        <AlarmClock size={phone ? 16 : 15} aria-hidden="true" />
        <input
          ref={field}
          class="tk-field"
          placeholder="Tomorrow at 3, every Monday at 9…"
          aria-label="When"
          value={text}
          onInput={(e) => onText(e.currentTarget.value)}
          onKeyDown={(e) => { if (e.key === "Enter" && onSubmit && value) { e.preventDefault(); onSubmit(); } }}
        />
      </label>
      <div class="sl-when-read" role="status">
        {errorWords ? (
          <span class="sl-when-err">{errorWords}</span>
        ) : value ? (
          <>
            <span class="sl-when-v">{value.kind === "repeat" ? ruleText(value.rule) : whenLong(value.at, TZ)}</span>
            <span class="tk-data sl-when-in">{value.kind === "repeat" ? `next ${whenShort(at, NOW, TZ)}` : inWords(value.at, NOW)}</span>
            {read?.alt && (
              <button type="button" class="tk-link is-accent sl-when-alt" onClick={() => { onChange({ kind: "once", at: read.alt }); setRead({ ...read, alt: read.at, at: read.alt }); }}>
                {timeInputValue(read.alt, TZ)} instead
              </button>
            )}
          </>
        ) : (
          <span class="sl-when-err">Type when, or pick below.</span>
        )}
      </div>
      {!text && (
        <div class="sl-when-presets">
          {presets(NOW, TZ).map((p) => (
            <button key={p.label} type="button" class={`tk-chip${value?.kind === "once" && value.at === p.at ? " is-on" : ""}`} onClick={() => { setText(""); setRead(null); onChange({ kind: "once", at: p.at }); }}>
              {p.label}<span class="tk-data sl-chip-t">{whenShort(p.at, NOW, TZ).replace(/^(Today|Tomorrow) /, "")}</span>
            </button>
          ))}
        </div>
      )}
      <div class="sl-when-pick">
        <label class="sl-pick">
          <span class="sl-pick-k">Date</span>
          <input type="date" class="sl-pick-in" value={date} disabled={value?.kind === "repeat" && value.rule.freq !== "monthly" && false} onInput={(e) => setPick(e.currentTarget.value, time, repeatId)} />
        </label>
        <label class="sl-pick is-time">
          <span class="sl-pick-k">Time</span>
          <input type="time" class="sl-pick-in" value={time} onInput={(e) => setPick(date, e.currentTarget.value, repeatId)} />
        </label>
        <label class="sl-pick is-repeat">
          <span class="sl-pick-k">Repeat</span>
          <select class="sl-pick-in" value={repeatId} onChange={(e) => setPick(date, time, e.currentTarget.value)}>
            {REPEATS(fromInputs(date, time, TZ)).map((o) => <option key={o.id} value={o.id}>{o.label}</option>)}
          </select>
        </label>
      </div>
      <div class="sl-when-tz"><Globe size={12} aria-hidden="true" />{TZ} · this device</div>
    </div>
  );
}

// ── Send to ──────────────────────────────────────────────────────────────────

function TargetList({ current, sessions, phone, onPick }) {
  const [q, setQ] = useState("");
  const needle = q.trim().toLowerCase();
  const live = Object.values(sessions).filter((s) => s.kind !== "owner" && (!needle || s.title.toLowerCase().includes(needle)))
    .sort((a, b) => (a.state === "saved") - (b.state === "saved") || (b.updated || 0) - (a.updated || 0)).slice(0, needle ? 20 : 5);
  const Item = ({ t, label, sub }) => {
    const on = current && JSON.stringify({ kind: current.kind, id: current.id, project: current.project }) === JSON.stringify({ kind: t.kind, id: t.id, project: t.project });
    return (
      <button type="button" class={`tk-pop-item${sub ? " is-two" : ""}${phone ? " is-phone" : ""}`} onClick={() => onPick(t)}>
        <span class="tk-pop-t">{label}</span>
        {sub && <span class="tk-pop-sub">{sub}</span>}
        {on && <Check size={14} class="tk-pop-on" aria-hidden="true" />}
      </button>
    );
  };
  return (
    <div class={`tk-move${phone ? " is-phone" : ""}`} onClick={(e) => e.stopPropagation()}>
      <label class={`tk-search${phone ? " is-phone" : ""}`}>
        <Search size={phone ? 15 : 14} aria-hidden="true" />
        <input class="tk-field" placeholder="Send to…" aria-label="Find a session" value={q} onInput={(e) => setQ(e.currentTarget.value)} />
      </label>
      {!needle && <div class="tk-pop-label">Owner</div>}
      {!needle && OWNERS.map((o) => <Item key={o.id} t={{ kind: "owner", id: o.id }} label={o.name} sub={sessions[o.session_id]?.state === "saved" ? "saved" : null} />)}
      <div class="tk-pop-label">Session</div>
      {live.map((s) => (
        <Item key={s.id} t={{ kind: "session", id: s.id }} label={s.title} sub={<>{projectName(s.cwd)}{s.state === "running" && <> · <span class="tk-word-working">Working</span></>}{s.state === "saved" && " · saved"}</>} />
      ))}
      {!needle && <div class="tk-pop-label">New session in</div>}
      {!needle && PROJECTS.map((p) => <Item key={p.key} t={{ kind: "new", project: p.key, model: "Sonnet 5.5", thinking: "medium" }} label={p.key} />)}
    </div>
  );
}

// ── Delivery: like an event hook's routing, folded ──────────────────────────

const DELIVERY_ROWS = [
  { key: "busy", label: "If it's working", opts: [["steer", "Steer it"], ["wait", "Wait until it's free"]], short: { steer: "Steers if working", wait: "Waits if working" } },
  { key: "saved", label: "If it's saved", opts: [["wake", "Wake it"], ["hold", "Wait until I open it"]], short: { wake: "wakes if saved", hold: "waits if saved" } },
  { key: "late", label: "If it's 10+ min late", opts: [["ask", "Ask me"], ["run", "Run anyway"], ["skip", "Skip it"]], short: { ask: "asks if late", run: "runs if late", skip: "skips if late" } },
];

function deliverySummary(d, target) {
  const rows = target?.kind === "new" ? DELIVERY_ROWS.filter((r) => r.key === "late") : DELIVERY_ROWS;
  const words = rows.map((r) => r.short[d[r.key]]);
  const s = words.join(" · ");
  const lead = target?.kind === "new" ? `${target.model} · ${target.thinking} · ` : "";
  return lead + s.charAt(0).toUpperCase() + s.slice(1);
}

function Delivery({ value, onChange, target, open: open0 = false, phone }) {
  const [open, setOpen] = useState(open0);
  const rows = target?.kind === "new" ? DELIVERY_ROWS.filter((r) => r.key === "late") : DELIVERY_ROWS;
  const custom = JSON.stringify(value) !== JSON.stringify(DEFAULT_DELIVERY);
  return (
    <section class={`tk-block sl-deliv${phone ? " is-phone" : ""}`} aria-label="Delivery">
      <button type="button" class="tk-block-h sl-deliv-h" aria-expanded={open} onClick={() => setOpen(!open)}>
        <ChevronRight size={13} class={`sl-chev${open ? " is-open" : ""}`} aria-hidden="true" />
        Delivery
        {!open && custom && <span class="sl-deliv-dot" aria-label="Changed" />}
      </button>
      {!open && <button type="button" class="sl-deliv-sum" onClick={() => setOpen(true)}>{deliverySummary(value, target)}</button>}
      {open && (
        <div class="sl-deliv-rows">
          {target?.kind === "new" && (
            <>
              <div class="sl-opt"><span class="sl-opt-k">Model</span><span class="sl-seg"><button type="button" class="sl-seg-b is-on">{target.model}<ChevronDown size={12} aria-hidden="true" /></button></span></div>
              <div class="sl-opt"><span class="sl-opt-k">Thinking</span><span class="sl-seg">{["low", "medium", "high"].map((t) => <button key={t} type="button" class={`sl-seg-b${target.thinking === t ? " is-on" : ""}`}>{t}</button>)}</span></div>
            </>
          )}
          {rows.map((r) => (
            <div class="sl-opt" key={r.key}>
              <span class="sl-opt-k">{r.label}</span>
              <span class="sl-seg" role="radiogroup" aria-label={r.label}>
                {r.opts.map(([v, l]) => (
                  <button key={v} type="button" role="radio" aria-checked={value[r.key] === v} class={`sl-seg-b${value[r.key] === v ? " is-on" : ""}`} onClick={() => onChange({ ...value, [r.key]: v })}>{l}</button>
                ))}
              </span>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

// ── Runs of a recurring task ─────────────────────────────────────────────────

const RUN_WORDS = { done: "Done", working: "Working", late: "Waiting for you", failed: "Not sent", skipped: "Skipped" };
function Runs({ s, onGo }) {
  if (!isRepeat(s)) return null;
  return (
    <section class="tk-block sl-runs" aria-label="Runs">
      <div class="tk-block-h">Runs</div>
      {s.next && (
        <div class="sl-run is-next">
          <span class="sl-run-at tk-data">{dateLabel(s.next, TZ)}</span>
          <span class="sl-run-s">Next</span>
        </div>
      )}
      {s.runs.map((r) => (
        <div class="sl-run" key={r.at}>
          <span class="sl-run-at tk-data">{dateLabel(r.at, TZ)}</span>
          <span class={`sl-run-s is-${r.state}`}>{RUN_WORDS[r.state]}</span>
          <span class="sl-run-note">{r.note}</span>
          {(r.state === "done" || r.state === "working") && (
            <button type="button" class="tk-icon is-quiet sl-go" aria-label={`Open ${r.note}`} title="Open its task" onClick={() => onGo?.(r)}>
              <ArrowUpRight aria-hidden="true" />
            </button>
          )}
        </div>
      ))}
    </section>
  );
}

// ── The detail: TaskDetail's anatomy, with When, Send to and Delivery ───────

function GoTo({ name, onGo, phone }) {
  return (
    <button type="button" class="tk-icon sl-goto" aria-label={`Open ${name}`} title={`Open ${name}  O`} onClick={onGo}>
      <ArrowUpRight aria-hidden="true" />
      {!phone && <Keycap>O</Keycap>}
    </button>
  );
}

function SchedDetail({ id, isNew, phone, init = {}, onClose, onCreated, onPushWhen, onPushTarget, onGoSession }) {
  const sessions = useStore(selectSessionDirectory);
  const ss = useScheduled();
  const s = isNew ? null : ss.find((x) => x.id === id);
  const base = s || {
    id: "new", title: init.title || "", description: init.description || "", when: init.when || null,
    target: init.target || { kind: "session", id: "deploy" }, delivery: { ...DEFAULT_DELIVERY, ...(init.delivery || {}) }, runs: [], state: "scheduled",
  };
  const [draft, setDraft] = useState(() => ({
    title: base.title, description: base.description, when: init.when || (s ? valueOf(s) : null), target: base.target, delivery: base.delivery,
  }));
  const [pop, setPop] = useState(init.pop || null); // 'when' | 'target'
  const [whenText] = useState(init.whenText || "");
  useEscape(!!pop, () => setPop(null));
  if (!isNew && !s) return null;
  const set = (p) => setDraft({ ...draft, ...p });
  const dirty = !isNew && (draft.title !== s.title || draft.description !== s.description || JSON.stringify(draft.when) !== JSON.stringify(valueOf(s))
    || JSON.stringify(draft.target) !== JSON.stringify(s.target) || JSON.stringify(draft.delivery) !== JSON.stringify(s.delivery)) || init.dirty;
  const cur = { ...base, ...draft, when: draft.when || base.when };
  const tName = targetName(cur, sessions);
  const tSession = targetSessionId(cur);
  const nextAt = draft.when ? (draft.when.kind === "repeat" ? nextOf(draft.when.rule, NOW, TZ) : draft.when.at) : null;
  const whenTextShown = draft.when ? (draft.when.kind === "repeat" ? ruleText(draft.when.rule) : whenLong(draft.when.at, TZ)) : "Pick a time";

  useEffect(() => {
    if (!tSession || phone) return undefined;
    const onKey = (e) => {
      if (/INPUT|TEXTAREA|SELECT/.test(e.target.tagName) || e.metaKey || e.ctrlKey || e.altKey) return;
      if (e.key === "o" || e.key === "O") { e.preventDefault(); onGoSession?.(tSession); }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [tSession, phone]);

  const save = () => {
    if (isNew) {
      if (!draft.title.trim() || !draft.when) return;
      const rec = { ...base, ...draft, id: `s-${Date.now()}`, created_at: NOW, tz: TZ, when: draft.when, runs: [], state: "scheduled" };
      setScheduled([...scheduled, { ...rec, next: nextRun(rec, TZ) }]);
      onCreated?.(rec.id);
      return;
    }
    patchSched(s.id, { ...draft });
  };

  let foot;
  if (isNew) {
    const ready = draft.title.trim() && draft.when;
    foot = (
      <>
        <span class="tk-grow" />
        <button type="button" class="zl-ask-btn is-primary" disabled={!ready} onClick={save}>
          {draft.when ? `Schedule for ${draft.when.kind === "repeat" ? ruleShort(draft.when.rule) : whenShort(draft.when.at, NOW, TZ)}` : "Schedule"}
          {!phone && <Keycap>{MOD_ENTER}</Keycap>}
        </button>
      </>
    );
  } else if (dirty) {
    foot = (
      <>
        <button type="button" class="zl-ask-btn is-quiet" onClick={() => setDraft({ title: s.title, description: s.description, when: valueOf(s), target: s.target, delivery: s.delivery })}>Discard</button>
        <span class="tk-grow" />
        {isRepeat(s) && nextAt && <span class="tk-foot-fact">From {whenShort(nextAt, NOW, TZ)} on</span>}
        <button type="button" class="zl-ask-btn is-primary" onClick={save}>Save</button>
      </>
    );
  } else if (s.state === "late") {
    foot = (
      <>
        <button type="button" class="tk-icon" aria-label="Delete task"><Trash2 size={15} aria-hidden="true" /></button>
        <span class="tk-grow" />
        <button type="button" class="zl-ask-btn" onClick={() => patchSched(s.id, { state: "scheduled", runs: [] })}>Skip</button>
        <button type="button" class="zl-ask-btn is-primary" onClick={() => patchSched(s.id, { state: "scheduled" })}><Play size={14} aria-hidden="true" />Run now</button>
      </>
    );
  } else if (s.state === "failed") {
    foot = (
      <>
        <button type="button" class="tk-icon" aria-label="Delete task"><Trash2 size={15} aria-hidden="true" /></button>
        <span class="tk-grow" />
        <button type="button" class="zl-ask-btn is-primary" onClick={() => (onPushTarget ? onPushTarget() : setPop("target"))}>Send to another session</button>
      </>
    );
  } else if (s.state === "paused") {
    foot = (
      <>
        <button type="button" class="tk-icon" aria-label="Delete task"><Trash2 size={15} aria-hidden="true" /></button>
        <span class="tk-grow" />
        <button type="button" class="zl-ask-btn is-primary" onClick={() => patchSched(s.id, { state: "scheduled" })}><Play size={14} aria-hidden="true" />Resume</button>
      </>
    );
  } else {
    foot = (
      <>
        <button type="button" class="tk-icon" aria-label="Delete task"><Trash2 size={15} aria-hidden="true" /></button>
        <span class="tk-grow" />
        {isRepeat(s) && <button type="button" class="zl-ask-btn" onClick={() => patchSched(s.id, { state: "paused" })}><Pause size={14} aria-hidden="true" />Pause</button>}
        {isRepeat(s) && <button type="button" class="zl-ask-btn" title="Skips the next run only">Skip next</button>}
        <button type="button" class="zl-ask-btn">{isRepeat(s) ? "Run now" : "Send now"}</button>
      </>
    );
  }

  return (
    <div class={`tk-detail tk-root sl-detail${phone ? " is-phone" : ""}`}>
      <div class="tk-detail-body">
        {!isNew && s.state === "late" && (
          <div class="sl-late" role="status">
            <span class="sl-late-t">Was due {whenShort(s.late.due, NOW, TZ).toLowerCase()}. moa was down until {timeInputValue(s.late.downUntil, TZ)}.</span>
            <span class="sl-late-q">Run it now?</span>
          </div>
        )}
        {!isNew && s.state === "failed" && (
          <div class="tk-notice is-failed sl-failed" role="status">
            <span>Couldn't send at {timeInputValue(s.when.at, TZ)}: {s.failure}.</span>
          </div>
        )}
        {!isNew && s.state === "paused" && (
          <div class="tk-notice" role="status"><span>Paused. It won't run until you resume it.</span></div>
        )}
        <textarea
          class="tk-field tk-title"
          rows={1}
          placeholder="What should it do?"
          aria-label="Title"
          value={draft.title}
          onInput={(e) => set({ title: e.currentTarget.value })}
          ref={(el) => { if (el) { el.style.height = "auto"; el.style.height = `${el.scrollHeight}px`; if (isNew && !el.dataset.f && !init.pop) { el.dataset.f = "1"; el.focus({ preventScroll: true }); } } }}
        />
        <textarea
          class="tk-field tk-desc"
          rows={1}
          placeholder="Add details"
          aria-label="Details"
          value={draft.description}
          onInput={(e) => set({ description: e.currentTarget.value })}
          ref={(el) => { if (el) { el.style.height = "auto"; el.style.height = `${el.scrollHeight}px`; } }}
        />
        <dl class="tk-props">
          <div class="tk-prop">
            <dt>When</dt>
            <dd class="tk-anchor sl-prop-dd">
              <button type="button" class={`tk-prop-btn${draft.when ? "" : " sl-empty"}`} aria-expanded={pop === "when"} onClick={() => (onPushWhen ? onPushWhen() : setPop(pop === "when" ? null : "when"))}>
                {draft.when?.kind === "repeat" ? <Repeat size={13} aria-hidden="true" /> : <Clock3 size={13} aria-hidden="true" />}
                <span class="tk-prop-v">{whenTextShown}</span>
                <ChevronDown size={13} aria-hidden="true" />
              </button>
              {nextAt && s?.state !== "late" && s?.state !== "failed" && s?.state !== "paused" && (
                <span class={`tk-data sl-prop-fact${draft.when?.kind === "repeat" ? " is-under" : ""}`}>{draft.when?.kind === "repeat" ? `next ${whenShort(nextAt, NOW, TZ)}` : inWords(nextAt, NOW)}</span>
              )}
            </dd>
          </div>
          {pop === "when" && (
            <div class="sl-inline-pop">
              <WhenEditor value={draft.when} text={whenText} onChange={(w) => set({ when: w })} onSubmit={() => setPop(null)} />
            </div>
          )}
          <div class="tk-prop">
            <dt>Send to</dt>
            <dd class="tk-anchor sl-prop-dd">
              <button type="button" class="tk-prop-btn" aria-expanded={pop === "target"} onClick={() => (onPushTarget ? onPushTarget() : setPop(pop === "target" ? null : "target"))}>
                {targetKindWord(cur) && <span class="sl-kind">{targetKindWord(cur)}</span>}
                <span class={`tk-prop-v${cur.target.kind === "session" && !sessions[cur.target.id] ? " sl-gone" : ""}`}>{tName}</span>
                <ChevronDown size={13} aria-hidden="true" />
              </button>
              {tSession && sessions[tSession] && <GoTo name={tName} phone={phone} onGo={() => onGoSession?.(tSession)} />}
              {pop === "target" && (
                <div class="tk-pop is-move" role="menu">
                  <TargetList current={draft.target} sessions={sessions} onPick={(t) => { set({ target: t }); setPop(null); }} />
                </div>
              )}
            </dd>
          </div>
          {!isNew && s.created_by_session_id && (
            <div class="tk-prop">
              <dt>Set by</dt>
              <dd>
                <button type="button" class="tk-link" onClick={() => onGoSession?.(s.created_by_session_id)}>
                  <span class="tk-sdot" aria-hidden="true" />The agent · {sessions[s.created_by_session_id]?.title}
                </button>
              </dd>
            </div>
          )}
        </dl>

        <Delivery value={draft.delivery} target={cur.target} open={!!init.deliveryOpen} phone={phone} onChange={(d) => set({ delivery: d })} />
        {!isNew && <Runs s={s} onGo={(r) => r.child && onGoSession?.(r.child)} />}

        <section class="tk-block" aria-label="Subtasks">
          <div class="tk-block-h">Subtasks</div>
          <label class="tk-sub is-add">
            <Plus size={14} aria-hidden="true" />
            <input class="tk-field" placeholder="Add subtask" aria-label="Add subtask" />
          </label>
        </section>
      </div>
      <div class="tk-foot">{foot}</div>
    </div>
  );
}

// ── Desktop: the Tasks view ──────────────────────────────────────────────────

function Dossier({ title, onClose, onBack, children }) {
  return (
    <div class="desktop-dossier is-open tk-dossier sl-dossier">
      <aside class="zl-side zl-side-right is-open" role="dialog" aria-label={title}>
        <div class={`zl-side-head${onBack ? " is-sub" : ""}`}>
          {onBack && <button type="button" class="zl-back" onClick={onBack} aria-label="Back"><BackIcon /></button>}
          <span class={`zl-side-title ${onBack ? "is-page" : "is-eyebrow"}`}>{title}</span>
          <button type="button" class="zl-x" onClick={onClose} aria-label="Close"><CloseIcon /></button>
        </div>
        {children}
      </aside>
    </div>
  );
}

function LabFilters({ agents, setAgents, project, setProject, projects, sched, setSched, phone }) {
  // Production's Filters, plus (variant B) the Scheduled filter beside it.
  const host = usePortalHost(`.sl-stage .tk-filters${phone ? ".is-phone" : ""}`, { display: "contents" });
  return (
    <>
      <Filters agents={agents} setAgents={setAgents} project={project} setProject={setProject} projects={projects} phone={phone} />
      {lab.list === "agenda" && host && createPortal(
        <button type="button" class={`tk-chip${sched ? " is-on" : ""}`} aria-pressed={sched} onClick={() => setSched(!sched)}>
          <Clock3 size={13} aria-hidden="true" />Scheduled<span class="tk-data sl-chip-n">{scheduled.length}</span>
        </button>,
        host,
      )}
    </>
  );
}

function useOrdinaryGroups(agents, project, sched) {
  const slice = useStore(tasksSlice);
  const sessions = useStore(selectSessionDirectory);
  const list = slice.list.filter((t) => !t._sched);
  return useMemo(() => (sched ? [] : groupTasks(list, { agents, project, sessions })), [slice.list, agents, project, sessions, sched]);
}

function TasksMain({ init = {} }) {
  const ss = useScheduled();
  const slice = useStore(tasksSlice);
  const sessions = useStore(selectSessionDirectory);
  const [sel, setSel] = useState(init.sel || null);
  const [creating, setCreating] = useState(!!init.creating);
  const [agents, setAgents] = useState(!!init.agents);
  const [project, setProject] = useState(null);
  const [sched, setSched] = useState(!!init.agenda);
  const [doneOpen, setDoneOpen] = useState(false);
  const checks = useRowChecks(completesInline);
  const lookup = useTaskLookup();
  const groups = useOrdinaryGroups(agents, project, sched);
  const projects = projectOptions(slice.projects.length ? slice.projects : PROJECTS, slice.list);
  const scopedSched = project ? ss.filter((s) => (s.target.project || projectName(sessions[targetSessionId(s)]?.cwd || "")) === project || false) : ss;
  const s = typeof sel === "string" ? byId(sel) : null;
  const t = typeof sel === "number" ? slice.list.find((x) => x.id === sel) : null;
  const count = openOwnCount(slice.list);
  const goSession = (id) => { location.search = `?view=schedule&scene=session&device=desktop&tz=${encodeURIComponent(TZ)}&focus=${id}`; };
  useEffect(() => {
    const onKey = (e) => {
      if (/INPUT|TEXTAREA|SELECT/.test(e.target.tagName) || e.metaKey || e.ctrlKey || e.altKey) return;
      if (e.key === "Escape") { setSel(null); setCreating(false); }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);

  return (
    <>
      <main class="conversation-main tk-main tk-root" aria-label="Tasks">
        <header class="zl-desk-head tk-head">
          <div class="tk-crumb">
            <span class="zl-crumb-title">Tasks</span>
            {count > 0 && <span class="tk-crumb-n zl-data">{count} open</span>}
          </div>
          <span class="tk-grow" />
          <button type="button" class="zl-ask-btn tk-newbtn" onClick={() => { setCreating(true); setSel(null); }}>
            <Plus size={15} aria-hidden="true" />New task<Keycap>C</Keycap>
          </button>
        </header>
        <LabFilters agents={agents} setAgents={setAgents} project={project} setProject={setProject} projects={projects} sched={sched} setSched={setSched} />
        <div class="tk-scroll">
          {lab.list === "agenda" ? (
            sched && <Agenda list={scopedSched} sessions={sessions} selected={sel} onSelect={(id) => { setSel(id); setCreating(false); }} />
          ) : (
            <ScheduledGroup list={scopedSched} sessions={sessions} selected={sel} onSelect={(id) => { setSel(id); setCreating(false); }} />
          )}
          {lab.list === "agenda" && !sched && ss.some((x) => x.state === "late") && (
            <AgendaAttention ss={ss} sessions={sessions} onSelect={setSel} selected={sel} />
          )}
          <TaskGroups
            groups={groups}
            selected={sel}
            onSelect={(id) => { setSel(id); setCreating(false); }}
            sessions={sessions}
            lookup={lookup}
            newSince={slice.newSince}
            completingId={checks.completingId}
            setCompletingId={checks.setCompletingId}
            onCheck={checks.onCheck}
            doneOpen={doneOpen}
            setDoneOpen={setDoneOpen}
          />
        </div>
      </main>
      {creating && (
        <Dossier title="New task" onClose={() => setCreating(false)}>
          <SchedDetail isNew init={init.newInit} onCreated={(id) => { setCreating(false); setSel(id); }} onGoSession={goSession} />
        </Dossier>
      )}
      {s && !creating && (
        <Dossier title={eyebrow(s)} onClose={() => setSel(null)}>
          <SchedDetail key={s.id} id={s.id} init={init.detail || {}} onGoSession={goSession} />
        </Dossier>
      )}
      {t && !creating && (
        <Dossier title={taskEyebrow(t)} onClose={() => setSel(null)}>
          <TaskDetail key={t.id} taskId={t.id} keys onOpenTask={setSel} onClose={() => setSel(null)} />
          <WhereGoTo task={t} sessions={sessions} onGo={goSession} />
        </Dossier>
      )}
    </>
  );
}

// Variant B hides Scheduled behind its filter, so what waits for you still
// has to surface: it joins You, as a request would.
function AgendaAttention({ ss, sessions, onSelect, selected }) {
  const late = ss.filter((s) => s.state === "late" || s.state === "failed");
  return (
    <div class="tk-list sl-list" role="listbox" aria-label="Scheduled, waiting for you">
      <section class="tk-sec">
        <div class="tk-group sl-group-attn"><span class="tk-group-t">You</span><span class="sl-group-wait">from Scheduled</span></div>
        {late.map((s) => <SchedRow key={s.id} s={s} sessions={sessions} selected={selected === s.id} onSelect={onSelect} />)}
      </section>
    </div>
  );
}

// The owner's ask (30/09): from ANY task's detail, one step to the session it
// belongs to. On a production TaskDetail the lab adds it to the Where row.
function WhereGoTo({ task, sessions, onGo }) {
  const sid = task.place === "agent" ? task.assignee_session_id : task.requester_session_id;
  const host = usePortalHost(".sl-dossier .tk-props > .tk-prop:first-child > dd", { display: "contents" });
  useEffect(() => {
    if (!sid) return undefined;
    const onKey = (e) => {
      if (/INPUT|TEXTAREA|SELECT/.test(e.target.tagName) || e.metaKey || e.ctrlKey || e.altKey) return;
      if (e.key === "o" || e.key === "O") { e.preventDefault(); onGo(sid); }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [sid]);
  if (!host || !sid || !sessions[sid] || task.place !== "agent") return null;
  return createPortal(<GoTo name={sessions[sid].title} onGo={() => onGo(sid)} />, host);
}

// ── Desktop: a session ────────────────────────────────────────────────────────

// The line over the composer: what is scheduled INTO this session, the
// nearest first, "+N" for the rest. Quieter than "For you" (nothing is asked
// of you); amber only when a run waits for your OK.
function SchedPin({ sessionId, phone, onOpen }) {
  const ss = useScheduled();
  const host = usePortalHost(phone ? ".sl-stage .mcomposer.zl-dock" : ".sl-stage .conversation-main .zl-dock", { prepend: true, display: "block", cls: "tk-pin-host" });
  const mine = sortSched(ss.filter((s) => targetSessionId(s) === sessionId && s.state !== "paused"));
  if (!host || !mine.length) return null;
  const s = mine[0];
  const late = s.state === "late";
  return createPortal(
    <div class={`tk-pin sl-pin${phone ? " is-phone" : ""}${late ? " is-attn" : ""}`} role="region" aria-label="Scheduled here">
      <div class="tk-pin-bar">
        <span class="sl-pin-ico">{isRepeat(s) ? <Repeat size={14} aria-hidden="true" /> : <Clock3 size={14} aria-hidden="true" />}</span>
        <span class="tk-pin-k">{late ? "Waiting for you" : "Scheduled"}</span>
        <button type="button" class="tk-pin-t" onClick={() => onOpen(s.id)}>{s.title}</button>
        {s.created_by_session_id && !phone && <span class="sl-pin-by">by the agent</span>}
        <span class="tk-data sl-pin-when">{late ? "Run now?" : s.next - NOW < 3 * 3600000 ? inWords(s.next, NOW) : whenShort(s.next, NOW, TZ)}</span>
        {mine.length > 1 && <span class="tk-pin-more tk-data" aria-label={`${mine.length - 1} more`}>+{mine.length - 1}</span>}
      </div>
    </div>,
    host,
  );
}

// The panel's Tasks row, with what is scheduled here in the same verdict.
function PanelTasksRow({ sessionId, onOpen }) {
  const ss = useScheduled();
  const host = usePortalHost(".sl-stage .zl-side-right .zl-prows", { prepend: true });
  const slice = useStore(tasksSlice);
  if (!host) return null;
  const mine = ss.filter((s) => targetSessionId(s) === sessionId && s.state !== "paused");
  const list = slice.list.filter((t) => t.place === "agent" && t.assignee_session_id === sessionId);
  const parts = [];
  if (mine.length) parts.push(`next ${whenShort(sortSched(mine)[0].next, NOW, TZ).replace(/^Today /, "")}`);
  if (list.length) parts.push(`${list.filter((t) => t.status === "done").length}/${list.length}`);
  const verdict = parts.join(" · ") || "none";
  return createPortal(
    <button type="button" class="zl-prow sl-prow" onClick={onOpen} aria-label={`Tasks: ${verdict}`}>
      <svg class="zl-prow-ico" viewBox="0 0 16 16" aria-hidden="true"><path d="M2.5 4.2l1.3 1.3 2.2-2.4M2.5 10.2l1.3 1.3 2.2-2.4M8.5 4.5H14M8.5 10.5H14" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" /></svg>
      <span class="zl-prow-t">Tasks</span>
      <span class="zl-prow-v zl-data">{verdict}</span>
      <svg class="zl-go" viewBox="0 0 12 12" aria-hidden="true"><path d="M4 2.5L7.5 6 4 9.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></svg>
    </button>,
    host,
  );
}

// The session's Tasks page: production's groups, with Scheduled on top.
function PanelScheduled({ sessionId, phone, onOpen }) {
  const ss = useScheduled();
  const sessions = useStore(selectSessionDirectory);
  const host = usePortalHost(".sl-stage .tk-panel-body", { prepend: true, display: "block" });
  if (!host) return null;
  const mine = ss.filter((s) => targetSessionId(s) === sessionId);
  return createPortal(
    <ScheduledGroup list={mine} sessions={sessions} phone onSelect={onOpen} context="session" onAdd={() => onOpen("new")} />,
    host,
  );
}

// ── Composer: Send later ─────────────────────────────────────────────────────

function readDraft(phone) {
  const ta = document.querySelector(phone ? ".sl-stage .mcomposer textarea" : ".sl-stage .conversation-main .zl-dock textarea");
  return ta?.value || "";
}
function writeDraft(phone, text) {
  const ta = document.querySelector(phone ? ".sl-stage .mcomposer textarea" : ".sl-stage .conversation-main .zl-dock textarea");
  if (!ta) return;
  const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value").set;
  setter.call(ta, text);
  ta.dispatchEvent(new Event("input", { bubbles: true }));
}

// Variant A: a clock beside Send, only while there is something to send.
function LaterButton({ phone, on, onOpen }) {
  const host = usePortalHost(phone ? ".sl-stage .mcomposer .zl-controls" : ".sl-stage .conversation-main .zl-dock .zl-controls", { before: ".zl-send" });
  const [has, setHas] = useState(false);
  useEffect(() => {
    const t = setInterval(() => setHas(!!readDraft(phone).trim()), 200);
    return () => clearInterval(t);
  }, []);
  if (!host || !has) return null;
  return createPortal(
    <button
      type="button"
      class={`zl-attach sl-later${on ? " is-on" : ""}`}
      aria-label="Send later"
      title={`Send later  ${formatShortcut("↵", { mod: true, shift: true })}`}
      onClick={onOpen}
    >
      <AlarmClock size={15} aria-hidden="true" />
    </button>,
    host,
  );
}

// Variant B: "Send later…" inside the + menu (on the desktop + becomes a menu).
function PlusMenu({ phone, onPick }) {
  const host = usePortalHost(phone ? ".sl-stage .mcomposer .zl-controls" : ".sl-stage .conversation-main .zl-dock .zl-controls", { prepend: true, display: "contents" });
  if (!host) return null;
  return createPortal(
    <div class="action-menu sl-plus-menu">
      <div class="action-menu-list action-menu-list--up sl-plus-list" role="menu" aria-label="More">
        <button type="button" role="menuitem" class="action-menu-item"><Paperclip size={16} aria-hidden="true" /><span>Attach files</span></button>
        <button type="button" role="menuitem" class="action-menu-item" onClick={onPick}><AlarmClock size={16} aria-hidden="true" /><span>Send later…</span></button>
      </div>
    </div>,
    host,
  );
}

function SendLater({ phone, sessionId, onClose, init = {} }) {
  const [when, setWhen] = useState(init.when || null);
  const [delivery, setDelivery] = useState({ ...DEFAULT_DELIVERY });
  const text = readDraft(phone) || init.draft || "";
  const schedule = () => {
    if (!when) return;
    const title = text.split("\n")[0].slice(0, 90);
    const rec = { id: `s-${Date.now()}`, title, description: text.length > 90 ? text : "", when, target: { kind: "session", id: sessionId }, delivery, runs: [], state: "scheduled", tz: TZ, created_at: NOW, subtasks: [] };
    setScheduled([...scheduled, { ...rec, next: nextRun(rec, TZ) }]);
    writeDraft(phone, "");
    onClose();
  };
  useEscape(!phone, onClose);
  const label = when ? `Schedule for ${when.kind === "repeat" ? ruleShort(when.rule) : whenShort(when.at, NOW, TZ)}` : "Schedule";
  const body = (
    <div class={`sl-later-body tk-root${phone ? " is-phone" : ""}`}>
      <WhenEditor value={when} text={init.text || ""} onChange={setWhen} phone={phone} autoFocus={!phone} onSubmit={schedule} />
      <Delivery value={delivery} onChange={setDelivery} target={{ kind: "session", id: sessionId }} phone={phone} />
      <div class="sl-later-foot">
        {!phone && <button type="button" class="zl-ask-btn is-quiet" onClick={onClose}>Cancel</button>}
        <span class="tk-grow" />
        <button type="button" class={`zl-ask-btn is-primary${phone ? " tk-wide" : ""}`} disabled={!when} onClick={schedule}>
          {label}{!phone && <Keycap>{MOD_ENTER}</Keycap>}
        </button>
      </div>
    </div>
  );
  if (phone) return body;
  return (
    <div class="sl-later-pop" role="dialog" aria-label="Send later">
      <div class="sl-later-head">
        <span class="sl-later-title">Send later</span>
        <span class="sl-later-draft">{text.split("\n")[0]}</span>
        <button type="button" class="tk-icon is-quiet" aria-label="Close" onClick={onClose}><CloseIcon /></button>
      </div>
      {body}
    </div>
  );
}

function DesktopSession({ init = {} }) {
  const sid = init.focus || "ci";
  const [open, setOpen] = useState(init.detail || null);
  const [later, setLater] = useState(!!init.later);
  const [plus, setPlus] = useState(!!init.plus);
  const laterHost = usePortalHost(".sl-stage .conversation-main .zl-dock", { before: ".zl-composer", display: "block", cls: "sl-later-host" });
  useEffect(() => {
    setTimeout(() => writeDraft(false, init.draft || ""), 60);
    if (init.panel) openSessionPanel(sid, init.panel); else closeSessionPanel();
  }, []);
  const s = open && open !== "new" ? byId(open) : null;
  return (
    <>
      <ConversationScreen />
      <SchedPin sessionId={sid} onOpen={(id) => { closeSessionPanel(); setOpen(id); }} />
      <PanelTasksRow sessionId={sid} onOpen={() => openSessionPanel(sid, "tasks")} />
      <PanelScheduled sessionId={sid} onOpen={(id) => setOpen(id)} />
      {lab.composer === "clock" && <LaterButton on={later} onOpen={() => setLater(!later)} />}
      {lab.composer === "plus" && plus && <PlusMenu onPick={() => { setPlus(false); setLater(true); }} />}
      {later && laterHost && createPortal(<SendLater sessionId={sid} init={init.laterInit} onClose={() => setLater(false)} />, laterHost)}
      {open && (
        <Dossier title={open === "new" ? "Schedule a task" : eyebrow(s)} onClose={() => setOpen(null)} onBack={init.panel ? () => setOpen(null) : undefined}>
          {open === "new"
            ? <SchedDetail isNew init={{ target: { kind: "session", id: sid } }} onCreated={() => setOpen(null)} />
            : s && <SchedDetail key={s.id} id={s.id} init={init.detailInit || {}} />}
        </Dossier>
      )}
    </>
  );
}

function DesktopStage({ init }) {
  return (
    <DesktopShell>
      {init.session ? <DesktopSession init={init.session} /> : <TasksMain init={init.main} />}
    </DesktopShell>
  );
}

// ── Phone ────────────────────────────────────────────────────────────────────

function PhoneTasks({ init = {}, onBack }) {
  const ss = useScheduled();
  const slice = useStore(tasksSlice);
  const sessions = useStore(selectSessionDirectory);
  const [stack, setStack] = useState(init.stack || []);
  const [agents, setAgents] = useState(false);
  const [project, setProject] = useState(null);
  const [sched, setSched] = useState(!!init.agenda);
  const [doneOpen, setDoneOpen] = useState(false);
  const checks = useRowChecks(completesInline);
  const lookup = useTaskLookup();
  const groups = useOrdinaryGroups(agents, project, sched);
  const projects = projectOptions(PROJECTS, slice.list);
  const top = stack[stack.length - 1] || null;
  const push = (p) => setStack([...stack, p]);
  const pop = () => setStack(stack.slice(0, -1));
  const back = top ? pop : onBack;
  const [drafts] = useState({});

  let title = "Tasks";
  let body;
  if (!top) {
    body = (
      <div class="tk-phone-body">
        <LabFilters phone agents={agents} setAgents={setAgents} project={project} setProject={setProject} projects={projects} sched={sched} setSched={setSched} />
        {lab.list === "agenda"
          ? sched && <Agenda list={ss} sessions={sessions} phone onSelect={(id) => push({ kind: "sched", id })} />
          : <ScheduledGroup list={ss} sessions={sessions} phone limit={4} onSelect={(id) => push({ kind: "sched", id })} />}
        <TaskGroups
          groups={groups}
          phone
          sessions={sessions}
          lookup={lookup}
          newSince={slice.newSince}
          onSelect={() => {}}
          completingId={checks.completingId}
          setCompletingId={checks.setCompletingId}
          onCheck={checks.onCheck}
          doneOpen={doneOpen}
          setDoneOpen={setDoneOpen}
        />
      </div>
    );
  } else if (top.kind === "sched" || top.kind === "new") {
    title = top.kind === "new" ? "New task" : "";
    body = (
      <SchedDetail
        key={top.id || "new"}
        id={top.id}
        isNew={top.kind === "new"}
        phone
        init={top.init || {}}
        onPushWhen={() => push({ kind: "when", id: top.id || "new" })}
        onPushTarget={() => push({ kind: "target", id: top.id || "new" })}
        onCreated={(id) => setStack([{ kind: "sched", id }])}
      />
    );
  } else if (top.kind === "when") {
    title = "When";
    const s = byId(top.id);
    body = <PhoneWhenPage s={s} init={top.init} drafts={drafts} onDone={pop} />;
  } else if (top.kind === "target") {
    title = "Send to";
    const s = byId(top.id);
    body = <div class="tk-phone-body"><TargetList phone current={s?.target} sessions={sessions} onPick={(t) => { if (s) patchSched(s.id, { target: t }); pop(); }} /></div>;
  }
  return (
    <div class="minbox sl-push">
      <div class="zi-inbox is-phone tk-push-in tk-root">
        <div class="zi-head is-sheet">
          <button type="button" class="zi-back" onClick={back} aria-label="Back"><BackIcon /></button>
          <span class="zi-title">{title}</span>
          {!top && (
            <button type="button" class="zi-x tk-plus" aria-label="New task" onClick={() => push({ kind: "new" })}>
              <Plus size={18} aria-hidden="true" />
            </button>
          )}
        </div>
        <div class="tk-page" key={stack.length}>{body}</div>
      </div>
    </div>
  );
}

function PhoneWhenPage({ s, init = {}, onDone }) {
  const [when, setWhen] = useState(init.when || (s ? valueOf(s) : null));
  return (
    <div class="tk-phone-body sl-when-page">
      <WhenEditor phone value={when} text={init.text || ""} onChange={setWhen} autoFocus={false} />
      {s && isRepeat(s) && <p class="sl-page-fact">Changes apply from the next run.</p>}
      <div class="sl-page-foot">
        <button type="button" class="zl-ask-btn is-primary tk-wide" disabled={!when} onClick={() => { if (s) patchSched(s.id, { when }); onDone(); }}>Done</button>
      </div>
    </div>
  );
}

function PhoneSession({ init = {} }) {
  const sid = init.focus || "ci";
  const [tasksOpen, setTasksOpen] = useState(!!init.tasksOpen);
  const [later, setLater] = useState(!!init.later);
  const [plus, setPlus] = useState(!!init.plus);
  const [detail, setDetail] = useState(init.detail || null);
  const host = usePortalHost(".sl-stage .mconv", { display: "contents" });
  useEffect(() => {
    setTimeout(() => writeDraft(true, init.draft || ""), 60);
    if (init.panel) openSessionPanel(sid, init.panel);
  }, []);
  const s = detail ? byId(detail) : null;
  return (
    <>
      <MobileConversationScreen forceMobile />
      <SchedPin phone sessionId={sid} onOpen={(id) => setDetail(id)} />
      <PanelTasksRow sessionId={sid} onOpen={() => openSessionPanel(sid, "tasks")} />
      <PanelScheduled sessionId={sid} phone onOpen={(id) => { closeSessionPanel(); setDetail(id); }} />
      {lab.composer === "clock" && <LaterButton phone on={later} onOpen={() => setLater(true)} />}
      {lab.composer === "plus" && plus && <PlusMenu phone onPick={() => { setPlus(false); setLater(true); }} />}
      {host && createPortal(
        <MobileSheet open={later} onClose={() => setLater(false)} title="Send later">
          {later && <SendLater phone sessionId={sid} init={init.laterInit} onClose={() => setLater(false)} />}
        </MobileSheet>,
        host,
      )}
      {host && createPortal(
        <MobileSheet open={!!s} onClose={() => setDetail(null)} title={s ? eyebrow(s) : ""} bare>
          {s && (
            <aside class="zl-side zl-side-right is-open is-sheet sl-own">
              <div class="zl-side-head">
                <span class="zl-side-title is-eyebrow">{eyebrow(s)}</span>
                <button type="button" class="zl-x" aria-label="Close" onClick={() => setDetail(null)}><CloseIcon /></button>
              </div>
              <SchedDetail key={s.id} id={s.id} phone init={init.detailInit || {}} />
            </aside>
          )}
        </MobileSheet>,
        host,
      )}
      {tasksOpen && host && createPortal(<PhoneTasks init={init.tasks} onBack={() => setTasksOpen(false)} />, host)}
    </>
  );
}

// ── Scenes ───────────────────────────────────────────────────────────────────

const SCENES = [
  ["list", "Tasks · Scheduled"],
  ["recurring", "Recurring + runs"],
  ["when", "When: words"],
  ["pick", "When: picker"],
  ["new", "New, with delivery"],
  ["edit", "Edit a recurring"],
  ["late", "Late: asks you"],
  ["failed", "Not sent"],
  ["paused", "Paused"],
  ["session", "In the session"],
  ["later", "Send later"],
  ["panel", "Session › Tasks"],
  ["delivered", "Delivered, marked"],
  ["goto", "Go to its session"],
];

function desktopInit(scene) {
  switch (scene) {
    case "list": return { main: { sel: "s-deploy" } };
    case "recurring": return { main: { sel: "s-infra" } };
    case "when": return { main: { sel: "s-deploy", detail: { pop: "when", whenText: "mañana a las 3", when: { kind: "once", at: parseWhen("mañana a las 3", NOW, TZ).at } } } };
    case "pick": return { main: { sel: "s-albaranes", detail: { pop: "when" } } };
    case "new": return { main: { creating: true, newInit: { title: "Revisión semanal de rendimiento", description: "Latencias de /api/sessions, memoria del proceso y tamaño de la base de datos. Compáralo con la semana anterior.", when: { kind: "repeat", rule: { freq: "weekly", dow: 1, h: 9, mi: 0 } }, target: { kind: "owner", id: "own-moa" }, deliveryOpen: true } } };
    case "edit": return { main: { sel: "s-infra", detail: { when: { kind: "repeat", rule: { freq: "weekly", dow: 1, h: 7, mi: 0 } }, deliveryOpen: false } } };
    case "late": return { main: { sel: "s-late" } };
    case "failed": return { main: { sel: "s-failed" } };
    case "paused": return { main: { sel: "s-ventas" } };
    case "session": return { session: { focus: "ci", panel: "root" } };
    case "later": return { session: { focus: "ci", draft: "Si el pipeline de !482 está verde, haz el merge y borra la rama.", later: true, laterInit: { text: "in 1 hour" } } };
    case "panel": return { session: { focus: "ci", panel: "tasks" } };
    case "delivered": return { session: { focus: "deploy", panel: "root" } };
    case "goto": return { main: { sel: 107, agents: true } };
    default: return { main: {} };
  }
}

function phoneInit(scene) {
  const when = (text) => { const r = parseWhen(text, NOW, TZ); return r.kind === "repeat" ? { kind: "repeat", rule: r.rule } : { kind: "once", at: r.at }; };
  switch (scene) {
    case "list": return { tasksOpen: true };
    case "recurring": return { tasksOpen: true, tasks: { stack: [{ kind: "sched", id: "s-infra" }] } };
    case "when": return { tasksOpen: true, tasks: { stack: [{ kind: "sched", id: "s-deploy" }, { kind: "when", id: "s-deploy", init: { text: "mañana a las 3", when: when("mañana a las 3") } }] } };
    case "pick": return { tasksOpen: true, tasks: { stack: [{ kind: "sched", id: "s-albaranes" }, { kind: "when", id: "s-albaranes" }] } };
    case "new": return { tasksOpen: true, tasks: { stack: [{ kind: "new", init: { title: "Revisión semanal de rendimiento", when: when("every monday at 9"), target: { kind: "owner", id: "own-moa" }, deliveryOpen: true } }] } };
    case "edit": return { tasksOpen: true, tasks: { stack: [{ kind: "sched", id: "s-infra" }, { kind: "when", id: "s-infra", init: { text: "cada lunes a las 7", when: when("cada lunes a las 7") } }] } };
    case "late": return { tasksOpen: true, tasks: { stack: [{ kind: "sched", id: "s-late" }] } };
    case "failed": return { tasksOpen: true, tasks: { stack: [{ kind: "sched", id: "s-failed" }] } };
    case "paused": return { tasksOpen: true, tasks: { stack: [{ kind: "sched", id: "s-ventas" }] } };
    case "session": return { focus: "ci" };
    case "later": return { focus: "ci", draft: "Si el pipeline de !482 está verde, haz el merge y borra la rama.", later: true, laterInit: { text: "in 1 hour" } };
    case "panel": return { focus: "ci", panel: "tasks" };
    case "delivered": return { focus: "deploy" };
    case "goto": return { focus: "ci", detail: "s-deploy" };
    default: return {};
  }
}

function seed(scene, device) {
  // Delivered: the deploy already went, so it is no longer scheduled.
  setScheduled(labScheduled(TZ).filter((x) => scene !== "delivered" || x.id !== "s-deploy"));
  const list = labTasks();
  const sessions = labSessions();
  const d = device === "desktop" ? desktopInit(scene) : phoneInit(scene);
  const focus = device === "desktop" ? (d.session ? d.session.focus : null) : (d.focus || "ci");
  setState((s) => ({
    sessions,
    sessionsLoaded: true,
    activeSession: focus || "ci",
    sessionPanel: { open: false, sessionId: null, page: "root" },
    drawerOpen: false,
    inboxOpen: false,
    owners: { ...s.owners, list: OWNER_FIXTURES, loaded: true },
    sidebarMode: "recent",
    tasks: { ...TASKS_INITIAL, list: [...list, ...lateStandIns(scheduled)], loaded: true, projects: PROJECTS, agents: !!d.main?.agents },
    tileTree: setTileSession(s.tileTree, s.focusedTile, focus),
  }));
  installTasksApi();
}

function useNoToasts() {
  useEffect(() => {
    const clear = () => getToasts().forEach((t) => removeToast(t.id));
    clear();
    return subscribeToasts(() => setTimeout(clear, 0));
  }, []);
}

function LabBar() {
  const href = (p) => {
    const q = new URLSearchParams(location.search);
    Object.entries(p).forEach(([k, v]) => q.set(k, v));
    return `?${q.toString()}`;
  };
  const Seg = ({ items, cur, k }) => (
    <div class="sl-lab-seg">
      {items.map(([id, label]) => <a key={id} href={href({ [k]: id })} class={`sl-lab-opt${cur === id ? " is-on" : ""}`}>{label}</a>)}
    </div>
  );
  return (
    <div class="sl-lab-bar">
      <Seg k="device" cur={lab.device} items={[["desktop", "Desktop"], ["phone", "Phone"]]} />
      <Seg k="list" cur={lab.list} items={[["group", "List A · Scheduled group"], ["agenda", "List B · filter + agenda"]]} />
      <Seg k="composer" cur={lab.composer} items={[["clock", "Composer A · clock by Send"], ["plus", "Composer B · in the + menu"]]} />
      <Seg k="scene" cur={lab.scene} items={SCENES} />
    </div>
  );
}

export function ScheduleLab() {
  const [ready, setReady] = useState(false);
  useNoToasts();
  useLayoutEffect(() => { seed(lab.scene, lab.device); setReady(true); }, []);
  const phone = lab.device === "phone";
  const d = phone ? phoneInit(lab.scene) : desktopInit(lab.scene);
  if (lab.composer === "plus" && lab.scene === "later") {
    const t = phone ? d : d.session;
    t.plus = true;
    t.later = false;
  }
  if (lab.list === "agenda") {
    if (phone && lab.scene === "list") d.tasks = { agenda: true };
    else if (!phone && d.main && lab.scene === "list") d.main.agenda = true;
  }
  return (
    <div class="sl-lab">
      <LabBar />
      <div class="sl-stage">
        {ready && (
          <ScreenLab width={phone ? PHONE_LAB_WIDTH : WIDE_WIDTH} height={phone ? PHONE_LAB_HEIGHT : WIDE_HEIGHT} note={null}>
            {phone ? <PhoneSession init={d} /> : <DesktopStage init={d} />}
          </ScreenLab>
        )}
      </div>
    </div>
  );
}
