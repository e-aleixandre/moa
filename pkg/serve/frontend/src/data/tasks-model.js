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

// ── Grouping (the global view) ─────────────────────────────────────────────

const newestFirst = (a, b) => (b.created_at || 0) - (a.created_at || 0);
const byId = (a, b) => (a.id || 0) - (b.id || 0);

// groupTasks lays out the global view: You (requests above notes), one
// Backlog per project, the agents' checklists only behind their filter, and
// what is done folded at the end. Empty groups are left out.
export function groupTasks(list, { agents = false, project = null, sessions = {} } = {}) {
  const all = Array.isArray(list) ? list : [];
  const scoped = project ? all.filter((t) => t.project_key === project) : all;
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
      const rows = bySession.get(sid).sort(byId);
      const s = sessions[sid];
      groups.push({
        id: `agent:${sid}`,
        title: sessionName(sessions, sid),
        agent: true,
        working: s?.state === 'running',
        sub: projectName(s?.cwd || rows[0]?.project_cwd || ''),
        rows,
        progress: `${rows.filter((t) => !isOpen(t)).length}/${rows.length}`,
        drop: { place: 'agent', sessionId: sid },
      });
    }
  }

  const done = scoped.filter((t) => !isOpen(t) && t.place !== 'agent')
    .sort((a, b) => (b.completed_at || 0) - (a.completed_at || 0));
  if (done.length) groups.push({ id: 'done', title: 'Done', rows: done, n: done.length, collapsed: true });
  return groups;
}

// The rows the keyboard walks: every row of an open group, top to bottom.
export function flatRows(groups, doneOpen = false) {
  return groups.filter((g) => !g.collapsed || doneOpen).flatMap((g) => g.rows);
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

// The session panel's Tasks row. It replaces the old "Tasks done/total" fact,
// so it carries both numbers once: requests waiting on you, and the
// checklist's progress.
export function sessionTasksVerdict(data) {
  const reqs = openSessionRequests(data).length;
  const list = Array.isArray(data?.checklist) ? data.checklist : [];
  const parts = [];
  if (reqs) parts.push(`${reqs} for you`);
  if (list.length) parts.push(`${list.filter((t) => t.status === 'done').length}/${list.length}`);
  return parts.join(' · ') || 'none';
}

export function sessionGroups(data, sessionId = '') {
  const { requests: reqs, checklist } = sessionRecords(data, sessionId);
  const list = [...checklist].sort(byId);
  const groups = [];
  if (reqs.length) {
    const open = reqs.filter(isOpen);
    groups.push({ id: 'for-you', title: 'For you', rows: [...open, ...reqs.filter((t) => !isOpen(t))], n: open.length });
  }
  groups.push({
    id: 'session', title: 'This session', rows: list, add: true,
    progress: list.length ? `${list.filter((t) => t.status === 'done').length}/${list.length}` : '',
  });
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
  if (isAgentTask(task) && choice) q.set('deliver', choice);
  return `/api/tasks/${task.id}?${q}`;
}

// Deleting an agent's task tells it; deleting a request or a note does not.
export function deleteNotifies(task) {
  return isAgentTask(task) && !!task.assignee_session_id;
}

// Completing tells the requester (a request) or the assignee (an agent's).
export function completeNotifies(task) {
  return isRequest(task) || deleteNotifies(task);
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
  const add = (key, cwd) => {
    if (!key || seen.has(key)) return;
    seen.add(key);
    out.push({ key, cwd: cwd || '' });
  };
  for (const p of projects || []) add(p.key, p.cwd);
  for (const t of list || []) add(t.project_key, t.project_cwd);
  return out.sort((a, b) => projectLabelOf(a).localeCompare(projectLabelOf(b)));
}

// The count beside the view's title: what is open in You and the backlogs.
export function openOwnCount(list) {
  return (list || []).filter((t) => isOpen(t) && t.place !== 'agent').length;
}
