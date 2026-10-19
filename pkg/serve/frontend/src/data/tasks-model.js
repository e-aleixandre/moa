// tasks-model.js — the global tasks, as pure decisions (no store, no fetch).
//
// A task lives in one PLACE, and the place says who sees it:
//   you      — the owner's. With requester_session_id it is a REQUEST an agent
//              made; without one it is a private note no agent ever reads.
//   backlog  — a project's pool, which that project's agents can claim.
//   agent    — one session's checklist, or a task assigned to it.
//
// Four gestures tell a session about a task (docs/serve.md, Tasks): assigning
// it, "Save and notify", completing a request, and completing or deleting an
// agent's task. Everything here that decides who hears about a gesture, and
// whether the owner has to choose between waking a saved session and holding
// the notice, is kept in this module so it can be tested without a renderer.

import { projectName, sessionTitle } from './util/format.js';
import { deviceZone, isScheduled, scheduleVerdict } from './schedule-model.js';

export const isOpen = (t) => !!t && t.status !== 'done';
export const isRequest = (t) => !!t && t.place === 'you' && !!t.requester_session_id;
export const isAgentTask = (t) => !!t && t.place === 'agent';

// The number in the sidebar foot: only the requests agents made that are
// still open. Notes, backlog and agent checklists never count.
export function openRequestCount(list) {
  if (!Array.isArray(list)) return 0;
  return list.filter((t) => isRequest(t) && isOpen(t)).length;
}

// The session a gesture on this task would tell: its assignee when it is an
// agent's, the session that asked when it is a request, nobody otherwise.
export function listenerOf(task) {
  if (!task) return '';
  if (isAgentTask(task)) return task.assignee_session_id || '';
  if (isRequest(task)) return task.requester_session_id;
  return '';
}

// sessionState reads a session of the roster as the three states a notice
// cares about. A session the roster does not know is missing.
export function sessionNoticeState(session) {
  if (!session) return 'missing';
  return session.state === 'saved' ? 'saved' : 'live';
}

// recipientFor — who would hear about a gesture now, and whether that session
// is loaded. The server's `recipient` (GET /api/tasks/{id}) wins; without it
// (an older server, or a row from the list) the roster answers.
export function recipientFor(task, sessions = {}) {
  if (task?.recipient?.session_id) return task.recipient;
  const id = listenerOf(task);
  if (!id) return null;
  return { session_id: id, state: sessionNoticeState(sessions[id]) };
}

// A saved session is the only case where the owner chooses: wake it now, or
// leave the notice for when it is opened. Loaded sessions just hear it, and a
// missing one cannot be woken.
export function needsDeliverChoice(recipient) {
  return recipient?.state === 'saved';
}

// startNotifyGesture — a gesture that tells a session. A saved recipient
// makes it wait for the owner's answer (ask); anything else runs it at once
// with no delivery choice.
export function startNotifyGesture(recipient, perform, ask) {
  if (needsDeliverChoice(recipient)) {
    ask((choice) => perform(choice));
    return 'asked';
  }
  perform(null);
  return 'ran';
}

// deliverOptions — the ONE line of decision a gesture that tells a session
// shows. A loaded session just hears it; a saved one offers waking it and
// holding the notice as two answers of equal weight, each of which IS the
// gesture (no second confirmation after it).
export function deliverOptions(verb, recipient) {
  if (needsDeliverChoice(recipient)) {
    return [
      { choice: 'wake', label: `${verb} and wake` },
      { choice: 'hold', label: `${verb}, notify when opened` },
    ];
  }
  return [{ choice: null, label: `${verb} and notify` }];
}

// deliverFields turns the owner's choice into the request field. There is no
// default: without an explicit choice nothing is sent, and the server holds.
export function deliverFields(choice) {
  if (choice === 'wake') return { deliver: 'wake' };
  if (choice === 'hold') return { deliver: 'hold' };
  return {};
}

// ── Names ──────────────────────────────────────────────────────────────────

export function sessionName(sessions, id) {
  if (!id) return '';
  const s = sessions?.[id];
  return s ? sessionTitle(s) : 'Deleted session';
}

export function taskProjectName(task) {
  if (!task) return '';
  return projectName(task.project_cwd || '') || task.project_key || '';
}

export function projectLabelOf(p) {
  return projectName(p?.cwd || '') || p?.key || '';
}

export function placeLabel(task, sessions) {
  if (!task) return '';
  if (task.place === 'you') return 'You';
  if (task.place === 'backlog') return `Backlog · ${taskProjectName(task)}`;
  return sessionName(sessions, task.assignee_session_id);
}

// relAge — the session list's clock, so a task's age never reads like a
// different one.
export function relAge(ms, now = Date.now()) {
  if (!ms) return '';
  const min = Math.floor((now - ms) / 60000);
  if (min < 1) return 'now';
  if (min < 60) return `${min}m`;
  const h = Math.floor(min / 60);
  if (h < 24) return `${h}h`;
  return `${Math.floor(h / 24)}d`;
}

// noteSummary — a closing note as one row line: its first line, without
// markdown marks, at most 110 characters.
export function noteSummary(note, max = 110) {
  const line = String(note || '').split(/\r?\n/)
    .map((l) => l.replace(/[*_`]/g, '').replace(/^[\s#>\-]+/, '').trim())
    .find(Boolean) || '';
  const chars = [...line];
  return chars.length > max ? `${chars.slice(0, max).join('').trimEnd()}…` : line;
}

// ── Grouping (the global view) ─────────────────────────────────────────────

const newestFirst = (a, b) => (b.created_at || 0) - (a.created_at || 0);
const byId = (a, b) => (a.id || 0) - (b.id || 0);

// groupTasks lays out the global view: You (requests above notes), one
// Backlog per project, the agents' checklists only behind their filter, and
// what is done folded at the end. Empty groups are left out.
export function groupTasks(list, { agents = false, project = null, sessions = {}, since = Infinity } = {}) {
  const all = Array.isArray(list) ? list : [];
  // A scheduled task is not the owner's to do: it has its own group
  // (Scheduled) until it is done.
  const scoped = (project ? all.filter((t) => t.project_key === project) : all).filter((t) => !isScheduled(t) || !isOpen(t));
  const groups = [];

  const you = scoped.filter((t) => t.place === 'you' && isOpen(t));
  const requests = you.filter(isRequest).sort(newestFirst);
  const notes = you.filter((t) => !isRequest(t)).sort(newestFirst);
  if (you.length) groups.push({ id: 'you', title: 'You', rows: [...requests, ...notes], n: you.length, drop: { place: 'you' } });

  const backlogs = new Map();
  for (const t of scoped) {
    if (t.place !== 'backlog' || !isOpen(t)) continue;
    const key = t.project_key || '';
    if (!backlogs.has(key)) backlogs.set(key, { key, cwd: t.project_cwd || '', rows: [] });
    backlogs.get(key).rows.push(t);
  }
  const sortedBacklogs = [...backlogs.values()].sort((a, b) => projectLabelOf(a).localeCompare(projectLabelOf(b)));
  for (const b of sortedBacklogs) {
    b.rows.sort(newestFirst);
    groups.push({
      id: `backlog:${b.key}`, title: 'Backlog', sub: projectLabelOf(b), rows: b.rows, n: b.rows.length,
      drop: { place: 'backlog', key: b.key, cwd: b.cwd },
    });
  }

  if (agents) {
    const bySession = new Map();
    for (const t of scoped) {
      if (t.place !== 'agent') continue;
      const sid = t.assignee_session_id || '';
      if (!bySession.has(sid)) bySession.set(sid, []);
      bySession.get(sid).push(t);
    }
    const order = [...bySession.keys()].sort((a, b) => {
      const la = Math.max(...bySession.get(a).map((t) => t.updated_at || 0));
      const lb = Math.max(...bySession.get(b).map((t) => t.updated_at || 0));
      return lb - la;
    });
    for (const sid of order) {
      const all = bySession.get(sid);
      const rows = all.filter(isOpen).sort(byId);
      const s = sessions[sid];
      const name = sessionName(sessions, sid);
      groups.push({
        id: `agent:${sid}`,
        title: name,
        agent: true,
        working: s?.state === 'running',
        sub: projectName(s?.cwd || all[0]?.project_cwd || ''),
        rows,
        progress: rows.length ? `${rows.length} open` : '',
        drop: { place: 'agent', sessionId: sid },
      });
      // What this session finished stays with it: the new on top of its
      // own fold, the rest folded.
      groups.push(...doneGroups(all.filter((t) => !isOpen(t)), since, { key: `:agent:${sid}`, sub: name }));
    }
  }

  groups.push(...doneGroups(scoped.filter((t) => !isOpen(t) && t.place !== 'agent'), since));
  return groups;
}

const byCompleted = (a, b) => (b.completed_at || 0) - (a.completed_at || 0);

// doneGroups folds a list's finished tasks: what was completed after the
// list's mark stays out of the fold ("Done since you last looked"), the rest
// goes into Done. Newest first in both.
export function doneGroups(done, since = Infinity, { key = '', sub = '' } = {}) {
  const sorted = [...done].sort(byCompleted);
  const fresh = sorted.filter((t) => (t.completed_at || 0) > since);
  const older = sorted.filter((t) => (t.completed_at || 0) <= since);
  const out = [];
  if (fresh.length) out.push({ id: `done-new${key}`, title: 'Done since you last looked', sub, rows: fresh, n: fresh.length });
  if (older.length) out.push({ id: `done${key}`, title: 'Done', sub, rows: older, n: older.length, collapsed: true });
  return out;
}

// The rows the keyboard walks: every row of an open group, top to bottom.
export function flatRows(groups, doneOpen = {}) {
  return groups.filter((g) => !g.collapsed || doneOpen[g.id]).flatMap((g) => g.rows);
}

// nextSelection — j/k and the arrows. Nothing selected lands on the first row.
export function nextSelection(rows, selectedId, delta) {
  if (!rows.length) return null;
  const i = rows.findIndex((t) => t.id === selectedId);
  if (i < 0) return rows[0].id;
  return rows[Math.max(0, Math.min(rows.length - 1, i + delta))].id;
}

// A key typed into a field is text, never a shortcut.
export function isTypingTarget(el) {
  if (!el) return false;
  const tag = String(el.tagName || '').toUpperCase();
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || !!el.isContentEditable;
}

// ── One session (panel page and pinned line) ──────────────────────────────
// GET /api/sessions/{id}/tasks answers with the flat projection:
// { requests: [...], checklist: [...] }, status pending|in_progress|done.

// sessionRecords gives the flat rows the place and session they imply, so the
// rows, the detail and the gestures read them like any other task.
export function sessionRecords(data, sessionId = '') {
  const flat = (t, extra) => ({ ...t, waits_for: t.waits_for || t.depends_on || [], ...extra });
  return {
    requests: (Array.isArray(data?.requests) ? data.requests : [])
      .map((t) => flat(t, { place: 'you', requester_session_id: sessionId || t.requester_session_id || 'session' })),
    checklist: (Array.isArray(data?.checklist) ? data.checklist : [])
      .map((t) => flat(t, { place: 'agent', assignee_session_id: sessionId || t.assignee_session_id || '' })),
  };
}

export function openSessionRequests(data) {
  const reqs = Array.isArray(data?.requests) ? data.requests : [];
  return reqs.filter(isOpen).sort((a, b) => (a.created_at || 0) - (b.created_at || 0));
}

// pinnedLine — the first open request of the session and how many more.
export function pinnedLine(data) {
  const open = openSessionRequests(data);
  if (!open.length) return null;
  return { task: open[0], more: open.length - 1 };
}

// checklistWords — what is left of a checklist, and what was finished after
// the device last opened its list: "17 open · +3 done" (long), "17 · +3"
// (short). The finished ones already seen are not counted.
function checklistWords(list, since = Infinity) {
  const open = list.filter(isOpen).length;
  const fresh = list.filter((t) => t.status === 'done' && (t.completed_at || 0) > since).length;
  return {
    long: `${open} open${fresh ? ` · +${fresh} done` : ''}`,
    short: `${open}${fresh ? ` · +${fresh}` : ''}`,
  };
}

// The session panel's Tasks row. It replaces the old "Tasks done/total" fact,
// so it carries every number once: requests waiting on you, runs waiting for
// your OK, the next scheduled run, and the checklist's progress.
export function sessionTasksVerdict(data, now = Date.now(), tz = deviceZone(), since = Infinity) {
  const reqs = openSessionRequests(data).length;
  const list = Array.isArray(data?.checklist) ? data.checklist : [];
  const parts = [];
  if (reqs) parts.push(`${reqs} for you`);
  parts.push(...scheduleVerdict(Array.isArray(data?.scheduled) ? data.scheduled : [], now, tz));
  if (list.length) parts.push(checklistWords(list, since).long);
  return parts.join(' · ') || 'none';
}

// sessionTasksStatus — the status line's Tasks item: the panel row's words,
// or null when the session has nothing to show.
export function sessionTasksStatus(data, now = Date.now(), tz = deviceZone(), since = Infinity) {
  if (!data) return null;
  const verdict = sessionTasksVerdict(data, now, tz, since);
  if (verdict === 'none') return null;
  const forYou = openSessionRequests(data).length;
  const list = Array.isArray(data?.checklist) ? data.checklist : [];
  const done = list.filter((t) => t.status === 'done').length;
  const progress = list.length ? checklistWords(list, since).short : verdict;
  return { text: verdict, forYou, pending: list.length - done, short: forYou ? String(forYou) : progress };
}

export function sessionGroups(data, sessionId = "", since = Infinity) {
  const { requests: reqs, checklist } = sessionRecords(data, sessionId);
  const list = [...checklist].sort(byId);
  const groups = [];
  if (reqs.length) {
    const open = reqs.filter(isOpen);
    groups.push({ id: 'for-you', title: 'For you', rows: [...open, ...reqs.filter((t) => !isOpen(t))], n: open.length });
  }
  const [fresh, fold] = [(g) => g.id.startsWith('done-new'), (g) => g.id === 'done'];
  const done = doneGroups(list.filter((t) => !isOpen(t)), since);
  groups.push(...done.filter(fresh));
  const open = list.filter(isOpen);
  groups.push({ id: 'session', title: 'This session', rows: open, add: true, progress: open.length ? `${open.length} open` : '' });
  groups.push(...done.filter(fold));
  return groups;
}

// ── The editor ────────────────────────────────────────────────────────────

export function editorDraft(task) {
  return {
    title: task?.title || '',
    description: task?.description || '',
    subtasks: (task?.subtasks || []).map((s) => ({ title: s.title, done: !!s.done })),
    waits_for: [...(task?.waits_for || [])],
  };
}

const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);

export function changedFields(draft, task) {
  const base = editorDraft(task);
  return ['title', 'description', 'subtasks', 'waits_for'].filter((k) => !same(draft[k], base[k]));
}

export function draftDirty(draft, task) {
  return changedFields(draft, task).length > 0;
}

// editorActions — the foot of a detail with unsaved changes. "Save" never
// tells anyone; "Save and notify" only exists when there is someone to tell.
export function editorActions({ dirty, recipient }) {
  if (!dirty) return [];
  const out = ['discard', 'save'];
  if (recipient?.session_id && recipient.state !== 'missing') out.push('saveNotify');
  return out;
}

export function savePatch(draft, task, { notify = false, choice = null } = {}) {
  const body = { revision: task.revision };
  for (const k of changedFields(draft, task)) body[k] = k === 'title' ? draft.title.trim() : draft[k];
  if (notify) Object.assign(body, { notify: true }, deliverFields(choice));
  return body;
}

// rebaseDraft — after a 409 the editor shows the task as it is now, and keeps
// what the owner typed in the fields they changed.
export function rebaseDraft(draft, before, current) {
  const next = editorDraft(current);
  for (const k of changedFields(draft, before)) next[k] = draft[k];
  return next;
}

export function completePatch(task, { note = '', choice = null } = {}) {
  const body = { revision: task.revision, status: 'done' };
  if (isRequest(task) && note.trim()) body.completion_note = note.trim();
  return Object.assign(body, deliverFields(choice));
}

export function reopenPatch(task) {
  return { revision: task.revision, status: 'pending' };
}

// A destination of Move: { place:'you' } | { place:'backlog', key, cwd } |
// { place:'agent', sessionId }.
export function movePatch(task, dest, choice = null) {
  const body = { revision: task.revision, place: dest.place };
  if (dest.place === 'backlog') {
    body.project_key = dest.key;
    if (dest.cwd) body.project_cwd = dest.cwd;
  }
  if (dest.place === 'agent') Object.assign(body, { assignee_session_id: dest.sessionId }, deliverFields(choice));
  return body;
}

export function isHere(task, dest) {
  if (!task || task.place !== dest.place) return false;
  if (dest.place === 'backlog') return task.project_key === dest.key;
  if (dest.place === 'agent') return task.assignee_session_id === dest.sessionId;
  return true;
}

export function createBody(draft, dest, choice = null) {
  const body = {
    title: draft.title.trim(),
    description: draft.description || '',
    place: dest.place,
    subtasks: draft.subtasks || [],
    waits_for: draft.waits_for || [],
  };
  if (dest.place === 'backlog') {
    body.project_key = dest.key;
    if (dest.cwd) body.project_cwd = dest.cwd;
  }
  if (dest.place === 'agent') Object.assign(body, { assignee_session_id: dest.sessionId }, deliverFields(choice));
  return body;
}

export function deletePath(task, choice = null) {
  const q = new URLSearchParams({ revision: String(task.revision) });
  if (deleteNotifies(task) && choice) q.set('deliver', choice);
  return `/api/tasks/${task.id}?${q}`;
}

// Deleting an open agent task tells it; a finished task, a request or a note
// does not.
export function deleteNotifies(task) {
  return isAgentTask(task) && !!task.assignee_session_id && task.status !== 'done';
}

// Completing tells the requester (a request) or the assignee (an agent's).
export function completeNotifies(task) {
  return isRequest(task) || (isAgentTask(task) && !!task.assignee_session_id);
}

// ── Notices ───────────────────────────────────────────────────────────────

const REASON_WORDS = {
  session_deleted: 'session deleted',
  session_limit: 'too many sessions are open',
  question_pending: 'the session is waiting on an answer',
  permission_pending: 'the session is waiting on a permission',
  resume_failed: 'the session could not be reopened',
  session_busy: 'the session is busy',
};

export function reasonWords(reason) {
  return REASON_WORDS[reason] || (reason ? String(reason).replace(/_/g, ' ') : 'the session could not be reached');
}

// latestUndelivered — the newest notice, when it has not arrived. A notice
// admitted (sent) or in the transcript (delivered) needs nothing from you.
export function latestUndelivered(notices) {
  const n = Array.isArray(notices) ? notices[0] : null;
  if (!n) return null;
  return n.state === 'held' || n.state === 'pending' || n.state === 'failed' ? n : null;
}

// noticeLine says, in words, what happened to the last notice and what the
// owner can do about it.
export function noticeLine(notice, name) {
  if (!notice) return null;
  if (notice.state === 'held') return { text: `Waiting for ${name} to open`, action: 'Wake now' };
  if (notice.state === 'failed' || notice.reason === 'session_deleted') {
    return { text: `Couldn't notify: ${notice.reason ? reasonWords(notice.reason) : 'the session is gone'}`, action: null };
  }
  if (notice.state === 'pending') return { text: `Not delivered: ${reasonWords(notice.reason)}`, action: 'Retry' };
  return null;
}

// The row's quiet indicator, from GET /api/tasks `notice_state`.
export function noticeStateWords(state) {
  if (state === 'held') return 'Waiting to notify';
  if (state === 'pending') return 'Not delivered';
  if (state === 'failed') return "Couldn't notify";
  return '';
}

// A 409 answers { error, current }; api() carries it as "409: <body>".
export function conflictCurrent(error) {
  if (error?.status !== 409) return null;
  const msg = String(error.message || '');
  const i = msg.indexOf('{');
  if (i < 0) return null;
  try {
    return JSON.parse(msg.slice(i)).current || null;
  } catch {
    return null;
  }
}

export function errorText(error) {
  const msg = String(error?.message || error || '');
  const i = msg.indexOf('{');
  if (i >= 0) {
    try { return JSON.parse(msg.slice(i)).error || msg; } catch { /* fallthrough */ }
  }
  return msg.replace(/^\d{3}:\s*/, '');
}

// projectOptions — the backlogs a task can go to: the projects the server
// knows (tasks and sessions) plus any a listed task names.
export function projectOptions(projects, list) {
  const out = [];
  const seen = new Set();
  const add = (key, cwd, cwds) => {
    if (!key || seen.has(key)) return;
    seen.add(key);
    out.push(cwds?.length ? { key, cwd: cwd || '', cwds } : { key, cwd: cwd || '' });
  };
  for (const p of projects || []) add(p.key, p.cwd, p.cwds);
  for (const t of list || []) add(t.project_key, t.project_cwd);
  return out.sort((a, b) => projectLabelOf(a).localeCompare(projectLabelOf(b)));
}

// The count beside the view's title: what is open in You and the backlogs.
export function openOwnCount(list) {
  return (list || []).filter((t) => isOpen(t) && t.place !== 'agent' && !isScheduled(t)).length;
}

// ── Dependencies ──────────────────────────────────────────────────────────

// waitersOf — every task that already waits for `taskId`, directly or
// through others. Making the task wait for any of them would close a cycle,
// so the "Waits for" picker leaves them out. The graph is whatever the client
// holds: each task's waits_for, plus the unblocks a detail read carries.
export function waitersOf(taskId, records) {
  const waiters = new Map();
  const edge = (waiter, waited) => {
    if (waiter == null || waited == null || waiter === waited) return;
    if (!waiters.has(waited)) waiters.set(waited, new Set());
    waiters.get(waited).add(waiter);
  };
  for (const t of records || []) {
    if (!t) continue;
    for (const w of t.waits_for || t.depends_on || []) edge(t.id, w);
    for (const u of t.unblocks || []) edge(u, t.id);
  }
  const out = new Set();
  const queue = [taskId];
  while (queue.length) {
    for (const w of waiters.get(queue.shift()) || []) {
      if (w === taskId || out.has(w)) continue;
      out.add(w);
      queue.push(w);
    }
  }
  return out;
}

// depCandidates — the open tasks "Waits for" can offer: not this one, not
// one it already waits for, and none that would close a cycle.
export function depCandidates(taskId, records, current = [], query = '') {
  const closers = taskId != null ? waitersOf(taskId, records) : new Set();
  const needle = query.trim().toLowerCase();
  const seen = new Set();
  const out = [];
  for (const t of records || []) {
    if (!t || seen.has(t.id)) continue;
    seen.add(t.id);
    if (!isOpen(t) || t.id === taskId || current.includes(t.id) || closers.has(t.id)) continue;
    if (needle && !String(t.title || '').toLowerCase().includes(needle)) continue;
    out.push(t);
  }
  return out;
}

// nameTaskRefs — the server names tasks as "#8"; the owner knows them by
// their titles.
export function nameTaskRefs(text, lookup) {
  return String(text || '').replace(/#(\d+)/g, (ref, id) => {
    const t = lookup?.(Number(id));
    return t?.title ? `“${t.title}”` : ref;
  });
}

// ── Between owners ────────────────────────────────────────────────────────
// A task one owner sent another carries `handoff` (docs/serve.md, Tasks):
//   awaiting_permission — in You as a request: the owner sends it, allows the
//                         pair for good, or declines;
//   sent                — the receiving owner's task;
//   declined            — closed, as Can't do.
// Closed, `outcome` says whether it was done or could not be done.

export const isHandoff = (t) => !!t?.handoff;
export const awaitingPermission = (t) => isOpen(t) && t?.handoff?.state === 'awaiting_permission';
export const isCantDo = (t) => !!t && t.status === 'done' && t.handoff?.outcome === 'cant_do';

// hasThread — the tasks with a thread: those between owners and the
// requests an agent made of you. Any other task answers 400 to comments.
export function hasThread(t) {
  return isHandoff(t) || isRequest(t) || (isAgentTask(t) && !!t.assignee_session_id);
}

// A task between owners stays with the owner it was sent to: it never moves.
export function canMove(t) {
  return !isHandoff(t);
}

// A declined request is closed for good; any other done task reopens.
export function canReopen(t) {
  return !!t && t.status === 'done' && t.handoff?.state !== 'declined';
}

// The ring of a request still waiting for permission does not complete it:
// its answer is Send, Always allow or Decline.
export function canCheck(t) {
  return !awaitingPermission(t) && (isOpen(t) || canReopen(t));
}

export function ownerById(owners, id) {
  if (!id) return null;
  return (owners || []).find((o) => o?.id === id) || null;
}

// handoffSide — one end of a handoff as the UI draws it: the live owner when
// it still exists (its face and current name), otherwise the name the task
// kept.
export function handoffSide(task, side, owners) {
  const h = task?.handoff;
  if (!h) return null;
  const id = side === 'to' ? h.to_owner_id : h.from_owner_id;
  const stored = side === 'to' ? h.to_owner_name : h.from_owner_name;
  const owner = ownerById(owners, id);
  return { id: id || '', name: owner?.name || stored || 'Deleted owner', owner, gone: !owner };
}

// handoffOrphan — a task sent to an owner that no longer exists: nobody will
// receive it, so it can only be closed as Can't do (which tells the sender)
// or deleted. Only claimed once the owners roster has loaded.
export function handoffOrphan(task, owners, loaded = true) {
  if (!loaded || !isOpen(task) || !isHandoff(task)) return false;
  // A session's projection names the owners without their ids: unknown, not gone.
  if (!task.handoff.to_owner_id) return false;
  return !ownerById(owners, task.handoff.to_owner_id);
}

// handoffActions — the request's three answers, in order. Without the
// receiving owner there is nobody to send to; without the sender there is
// nobody to allow.
export function handoffActions(task, owners, loaded = true) {
  if (!awaitingPermission(task)) return [];
  const from = handoffSide(task, 'from', owners);
  const to = handoffSide(task, 'to', owners);
  const out = [];
  if (!loaded || !to.gone) {
    out.push({ action: 'send', label: `Send to ${to.name}` });
    if (!loaded || !from.gone) out.push({ action: 'allow', label: `Always allow ${from.name} → ${to.name}` });
  }
  out.push({ action: 'decline', label: 'Decline' });
  return out;
}

export function handoffBody(task, action) {
  return { action, revision: task.revision };
}

// closeReady — Can't do needs the reason; Done takes a note or nothing.
export function closeReady(outcome, note) {
  return outcome !== 'cant_do' || !!String(note || '').trim();
}

// closePatch — closing a task between owners from the UI, with its outcome.
export function closePatch(task, { outcome = 'done', note = '', choice = null } = {}) {
  const body = { revision: task.revision, status: 'done', outcome };
  const text = String(note || '').trim();
  if (text) body.completion_note = text;
  return Object.assign(body, deliverFields(choice));
}

// rowFrom — the chip a task between owners wears in a row and in its detail.
export function rowFrom(task, owners) {
  return isHandoff(task) ? handoffSide(task, 'from', owners) : null;
}

// commentAuthor — who wrote a comment, as the thread names them. The human
// is You. In a task between owners an agent writes as its owner: found by
// the owner's session, then by the name the comment kept against the two
// ends of the handoff; an owner since deleted keeps that name. A request's
// agent is named after its session.
export function commentAuthor(comment, task, { owners = [], sessions = {} } = {}) {
  if (comment?.author_kind === 'human') return { kind: 'human', name: 'You', owner: null };
  const sid = comment?.author_session_id || '';
  const bySession = sid ? (owners || []).find((o) => o?.session_id === sid) : null;
  if (bySession) return { kind: 'owner', name: bySession.name, owner: bySession };
  const stored = comment?.author_name || '';
  const h = task?.handoff;
  if (stored && h) {
    const id = stored === h.from_owner_name ? h.from_owner_id : stored === h.to_owner_name ? h.to_owner_id : '';
    const owner = ownerById(owners, id);
    return { kind: 'owner', name: owner?.name || stored, owner: owner || { name: stored } };
  }
  if (stored) return { kind: 'owner', name: stored, owner: { name: stored } };
  return { kind: 'session', name: sessionName(sessions, sid) || 'Agent', owner: null };
}

// threadPlaceholder — the composer says who will read what is written.
export function threadPlaceholder(task, { owners = [], sessions = {} } = {}) {
  if (isHandoff(task)) {
    const from = handoffSide(task, 'from', owners).name;
    if (task.handoff.state !== 'sent' || handoffOrphan(task, owners, owners.length > 0)) return `Reply to ${from}…`;
    return `Reply to ${from} and ${handoffSide(task, 'to', owners).name}…`;
  }
  if (isRequest(task)) return `Reply to ${sessionName(sessions, task.requester_session_id)}…`;
  if (isAgentTask(task) && task.assignee_session_id) return `Tell ${sessionName(sessions, task.assignee_session_id)}…`;
  return 'Write a comment…';
}
