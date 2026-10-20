import { useMemo, useState } from "preact/hooks";
import { ArrowUpRight, Check, ChevronRight, ListChecks, X } from "lucide-preact";
import { closeTaskRequest } from "../../data/for-you.js";
import { addToast } from "../../data/notifications.js";
import { openTasksView } from "../../data/tasks-view.js";
import { OwnerAvatarFor } from "../Owners/OwnerAvatar.jsx";

// A request older than this folds apart, under its project.
export const OLD_DAYS = 7;
const DAY = 86400000;

const at = (s) => Date.parse(s || "") || 0;
export const isOld = (item, now = Date.now()) => now - at(item.since) >= OLD_DAYS * DAY;

function age(since) {
  const min = Math.floor(Math.max(0, Date.now() - at(since)) / 60000);
  if (min < 60) return `${Math.max(1, min)}m`;
  const h = Math.floor(min / 60);
  return h < 24 ? `${h}h` : `${Math.floor(h / 24)}d`;
}

const CLOSED = { done: "Marked done", dismiss: "Dismissed" };

// closeOne closes a request and says how it went. A 409 means it changed in
// Tasks since For you read it: the row is back as it is now.
export function closeOne(item, how) {
  return closeTaskRequest(item, how).then((outcome) => {
    if (outcome === "ok") addToast({ title: CLOSED[how], detail: item.title, type: "info" });
    else if (outcome === "conflict") addToast({ title: "This request changed", detail: "It is shown as it is now; close it again if it still applies.", type: "info" });
    else addToast({ title: "Could not close the request", detail: "Open it in Tasks and close it there.", type: "error" });
    return outcome;
  });
}

// RequestRow — an agent's request of the user, closed in place: Done tells
// the session that asked, Dismiss tells no one. On the desktop both sit at
// the row's end on hover; on the phone they open with the row.
export function RequestRow({ row, phone, open, onToggle, onLeave, onReturn }) {
  const { item, owner } = row;
  const act = (how) => (e) => {
    e?.stopPropagation?.();
    closeOne(item, how);
  };
  const toTasks = () => {
    onLeave?.();
    openTasksView({ sessionId: item.session_id, back: onReturn || null });
  };
  return (
    <li class={`fy-row fy-req${open ? " is-open" : ""}${isOld(item) ? " is-old" : ""}`} data-item={item.id}>
      <div class="fy-row-head" role="button" tabIndex={0} aria-expanded={open} onClick={onToggle}
        onKeyDown={(e) => { if (e.target === e.currentTarget && (e.key === "Enter" || e.key === " ")) { e.preventDefault(); onToggle?.(); } }}>
        <span class="fy-ico is-task_request"><ListChecks size={15} aria-hidden="true" /></span>
        <span class="fy-main">
          <span class="fy-title">{item.title || "Request"}</span>
          <span class="fy-meta">
            <span class="fy-mi fy-mi-who">
              {owner && <OwnerAvatarFor owner={owner} size={16} />}
              <span class="fy-who">{owner ? `${owner.name} · ${row.session}` : row.session}</span>
            </span>
            <span class="fy-mi fy-age zl-data">{age(item.since)}</span>
          </span>
        </span>
        {!phone && (
          <span class="fy-req-acts">
            <button type="button" class="fy-ibtn" data-act="done" title="Done" aria-label="Done" onClick={act("done")}><Check size={15} aria-hidden="true" /></button>
            <button type="button" class="fy-ibtn" data-act="dismiss" title="Dismiss" aria-label="Dismiss" onClick={act("dismiss")}><X size={15} aria-hidden="true" /></button>
          </span>
        )}
      </div>
      {open && (
        <div class="fy-body">
          {item.description && <p class="fy-news-body">{item.description}</p>}
          <div class="fy-actions">
            <button type="button" class="fy-btn" data-act="dismiss" onClick={act("dismiss")}><X size={15} aria-hidden="true" />Dismiss</button>
            <button type="button" class="fy-btn is-primary" data-act="done" onClick={act("done")}><Check size={15} aria-hidden="true" />Done</button>
          </div>
          <button type="button" class="fy-open" data-act="tasks" onClick={toTasks}>
            Open in Tasks<ArrowUpRight size={14} aria-hidden="true" />
          </button>
        </div>
      )}
    </li>
  );
}

// groupByProject — newest project first, newest request first in each, and
// in each the recent ones apart from those older than OLD_DAYS.
export function groupByProject(rows, now = Date.now()) {
  const m = new Map();
  for (const r of [...rows].sort((a, b) => at(b.item.since) - at(a.item.since))) {
    const k = r.item.project || "Other";
    if (!m.has(k)) m.set(k, []);
    m.get(k).push(r);
  }
  return [...m.entries()].map(([project, list]) => ({
    project,
    recent: list.filter((r) => !isOld(r.item, now)),
    old: list.filter((r) => isOld(r.item, now)),
  }));
}

// The user's own fold survives a trip to Tasks and back. Until he folds or
// unfolds it, the fold follows the list as it is now — also when it arrives
// after the first render (empty, then the GET's answer).
let foldMemory = null;
export function __resetRequestsFoldForTests() { foldMemory = null; }

// Requests — every request, one line saying how many per project, folded
// when there are more than three; open, grouped by project, the old ones
// folded apart and dismissable in one go after a confirmation.
export function Requests({ rows, phone, openId, setOpenId, onLeave, onReturn }) {
  const [chosen, setChosen] = useState(foldMemory);
  const folded = chosen ?? rows.length > 3;
  const setFolded = (f) => { foldMemory = f; setChosen(f); };
  const [showOld, setShowOld] = useState({});
  const [confirm, setConfirm] = useState(false);
  const groups = useMemo(() => groupByProject(rows), [rows]);
  if (rows.length === 0) return null;
  const old = groups.flatMap((g) => g.old);
  const oldest = Math.max(...rows.map((r) => Math.floor((Date.now() - at(r.item.since)) / DAY)));
  const row = (r) => (
    <RequestRow key={r.item.id} row={r} phone={phone} onLeave={onLeave} onReturn={onReturn}
      open={openId === r.item.id} onToggle={() => setOpenId(openId === r.item.id ? null : r.item.id)} />
  );
  const dismissOld = () => {
    setConfirm(false);
    Promise.all(old.map((r) => closeTaskRequest(r.item, "dismiss"))).then((outcomes) => {
      const ok = outcomes.filter((o) => o === "ok").length;
      const left = outcomes.length - ok;
      addToast(left
        ? { title: `Dismissed ${ok} of ${outcomes.length}`, detail: `${left} changed or failed and are still listed.`, type: "info" }
        : { title: `Dismissed ${ok} requests`, type: "info" });
    });
  };
  return (
    <section class="fy-sec fy-reqs">
      <button type="button" class="fy-sec-head fy-sec-fold" aria-expanded={!folded} onClick={() => setFolded(!folded)}>
        <span class="fy-sec-t">Requests · {rows.length}</span>
        <span class="fy-sum">{groups.map((g) => `${g.project} ${g.recent.length + g.old.length}`).join(" · ")}{oldest >= OLD_DAYS ? ` · oldest ${oldest}d` : ""}</span>
        <ChevronRight size={14} class={`fy-fold-chev${folded ? "" : " is-open"}`} aria-hidden="true" />
      </button>
      {!folded && groups.map((g) => {
        return (
          <div class="fy-pgroup" key={g.project}>
            <div class="fy-pgroup-h"><span>{g.project}</span><span class="zl-data">{g.recent.length + g.old.length}</span></div>
            <ul class="fy-list">
              {g.recent.map(row)}
              {showOld[g.project] && g.old.map(row)}
            </ul>
            {g.old.length > 0 && !showOld[g.project] && (
              <button type="button" class="fy-earlier" onClick={() => setShowOld({ ...showOld, [g.project]: true })}>
                {g.old.length} older than a week<ChevronRight size={14} aria-hidden="true" />
              </button>
            )}
          </div>
        );
      })}
      {!folded && old.length > 0 && (
        <div class="fy-bulk">
          {confirm ? (
            <>
              <span>Dismiss {old.length} request{old.length > 1 ? "s" : ""} older than a week? Their sessions are not told.</span>
              <button type="button" class="fy-btn" onClick={() => setConfirm(false)}>Cancel</button>
              <button type="button" class="fy-btn is-danger" data-act="dismiss-old" onClick={dismissOld}>Dismiss {old.length}</button>
            </>
          ) : (
            <button type="button" class="fy-sec-act" data-act="ask-dismiss-old" onClick={() => setConfirm(true)}>Dismiss the {old.length} older than a week…</button>
          )}
        </div>
      )}
    </section>
  );
}
