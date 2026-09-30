// tasks.js — the global tasks on the client: one store slice, the REST calls,
// and the /api/tasks/ws invalidation.
//
// The database is the authority. The socket only says "something changed"
// (tasks_changed); every answer to it, and every reconnect, is a fresh GET, so
// losing the socket loses nothing. What is on screen asks to be kept fresh:
// the list is always loaded (the sidebar counts from it), a session's tasks
// while its conversation or panel is showing, a task's detail while it is open.

import { api } from './api.js';
import { store, setState, TASKS_INITIAL } from './store.js';
import { sessionRecords } from './tasks-model.js';

export { TASKS_INITIAL };

export function tasksSlice(state) {
  return state?.tasks || TASKS_INITIAL;
}

function patch(next) {
  setState((s) => ({ tasks: { ...tasksSlice(s), ...(typeof next === 'function' ? next(tasksSlice(s)) : next) } }));
}

// ── Reads ─────────────────────────────────────────────────────────────────

let listSeq = 0;
export function loadTasks() {
  const seq = ++listSeq;
  const agents = tasksSlice(store.get()).agents;
  return api('GET', `/api/tasks${agents ? '?include_agents=1' : ''}`)
    .then((res) => {
      if (seq !== listSeq) return;
      patch({
        list: Array.isArray(res?.tasks) ? res.tasks : [],
        counts: res?.counts || TASKS_INITIAL.counts,
        revision: res?.revision || 0,
        loaded: true,
        error: null,
      });
    })
    .catch((error) => {
      if (seq !== listSeq) return;
      patch({ error: String(error?.message || error) });
    });
}

export function setTasksAgents(on) {
  patch({ agents: !!on });
  return loadTasks();
}

export function loadTaskProjects() {
  return api('GET', '/api/tasks/projects')
    .then((res) => patch({ projects: Array.isArray(res?.projects) ? res.projects : [] }))
    .catch(() => {});
}

export function loadSessionTasks(sessionId) {
  if (!sessionId) return Promise.resolve();
  return api('GET', `/api/sessions/${encodeURIComponent(sessionId)}/tasks`)
    .then((res) => patch((t) => ({
      bySession: { ...t.bySession, [sessionId]: { requests: res?.requests || [], checklist: res?.checklist || [], scheduled: res?.scheduled || [], loaded: true } },
    })))
    .catch(() => {});
}

export function loadTask(id) {
  if (!id) return Promise.resolve(null);
  return api('GET', `/api/tasks/${id}`)
    .then((rec) => {
      storeTask(rec);
      return rec;
    })
    .catch((error) => {
      if (error?.status === 404) patch((t) => ({ details: { ...t.details, [id]: { id, gone: true } } }));
      return null;
    });
}

function storeTask(rec) {
  if (!rec?.id) return;
  patch((t) => ({
    details: { ...t.details, [rec.id]: rec },
    list: t.list.some((x) => x.id === rec.id) ? t.list.map((x) => (x.id === rec.id ? { ...x, ...rec } : x)) : t.list,
  }));
}

// ── What stays fresh ─────────────────────────────────────────────────────

const watchedSessions = new Map();
const watchedTasks = new Map();

function watch(map, key, load) {
  map.set(key, (map.get(key) || 0) + 1);
  load(key);
  return () => {
    const n = (map.get(key) || 1) - 1;
    if (n <= 0) map.delete(key); else map.set(key, n);
  };
}

export function watchSessionTasks(sessionId) {
  if (!sessionId) return () => {};
  return watch(watchedSessions, sessionId, loadSessionTasks);
}

export function watchTask(id) {
  if (!id) return () => {};
  return watch(watchedTasks, id, loadTask);
}

let refreshTimer = null;
export function refreshTasks() {
  clearTimeout(refreshTimer);
  refreshTimer = setTimeout(() => {
    loadTasks();
    for (const id of watchedSessions.keys()) loadSessionTasks(id);
    for (const id of watchedTasks.keys()) loadTask(id);
  }, 120);
}

// ── The invalidation socket ──────────────────────────────────────────────

let socket = null;
let backoff = 1000;
let retry = null;
let started = false;

function connect() {
  clearTimeout(retry);
  if (typeof WebSocket === 'undefined' || typeof location === 'undefined') return;
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  let ws;
  try {
    ws = new WebSocket(`${proto}//${location.host}/api/tasks/ws`);
  } catch (_) {
    schedule();
    return;
  }
  socket = ws;
  ws.onmessage = (e) => {
    if (socket !== ws) return;
    backoff = 1000;
    let evt = null;
    try { evt = JSON.parse(e.data); } catch (_) { return; }
    if (evt?.type === 'tasks_changed') refreshTasks();
  };
  ws.onclose = () => {
    if (socket !== ws) return;
    socket = null;
    schedule();
  };
}

function schedule() {
  if (!started) return;
  clearTimeout(retry);
  retry = setTimeout(connect, backoff);
  backoff = Math.min(backoff * 2, 30000);
}

// startTasksSync — once, at bootstrap. The server answers every connection
// with the current revision, which is itself an invalidation: a reconnect
// always re-reads.
export function startTasksSync() {
  if (started) return;
  started = true;
  patch({ newSince: readSeen() });
  loadTasks();
  connect();
}

// Back in the foreground: re-read, and redial at once if the socket died
// while the app was away (a backgrounded PWA loses it).
export function resumeTasksSync() {
  if (!started) return;
  refreshTasks();
  if (!socket || socket.readyState > 1) {
    backoff = 1000;
    connect();
  }
}

// ── Writes ────────────────────────────────────────────────────────────────
// Each returns the server's answer and rejects with api()'s error, so the
// caller can read a 409 (conflictCurrent) and keep what the owner typed.

function after(rec) {
  if (rec?.id) storeTask(rec);
  refreshTasks();
  return rec;
}

export function createTask(body) {
  return api('POST', '/api/tasks', body).then(after);
}

export function patchTask(id, body) {
  return api('PATCH', `/api/tasks/${id}`, body).then(after);
}

export function deleteTaskAt(path, id) {
  return api('DELETE', path).then(() => {
    patch((t) => {
      const details = { ...t.details };
      delete details[id];
      return { details, list: t.list.filter((x) => x.id !== id) };
    });
    refreshTasks();
  });
}

// ── Scheduled tasks ──────────────────────────────────────────────────────
// A template's controls answer the committed record or run; a 409 carries
// the current one (conflictCurrent), like every other write here.

export function parseWhen(text, tz) {
  return api('POST', '/api/tasks/when/parse', { text, tz });
}

function control(path, body, id) {
  return api('POST', path, body).then((res) => {
    if (id) loadTask(id);
    refreshTasks();
    return res;
  });
}

export function runScheduleNow(task) {
  return control(`/api/tasks/${task.id}/run-now`, { revision: task.revision, due_at: task.next }, task.id);
}

export function skipScheduleNext(task) {
  return control(`/api/tasks/${task.id}/skip`, { revision: task.revision, due_at: task.next }, task.id);
}

export function pauseSchedule(task) {
  return control(`/api/tasks/${task.id}/pause`, { revision: task.revision }, task.id).then(after);
}

export function resumeSchedule(task) {
  return control(`/api/tasks/${task.id}/resume`, { revision: task.revision }, task.id).then(after);
}

export function confirmRun(run, action, taskId) {
  return control(`/api/tasks/occurrences/${run.id}/confirm`, { revision: run.revision, action }, taskId);
}

export function rerouteRun(run, sessionId, taskId) {
  return control(`/api/tasks/occurrences/${run.id}/reroute`, { revision: run.revision, session_id: sessionId }, taskId);
}

export function deliverNotice(noticeId) {
  return api('POST', `/api/tasks/notices/${encodeURIComponent(noticeId)}/deliver`).then((n) => {
    refreshTasks();
    return n;
  });
}

// ── What is new ──────────────────────────────────────────────────────────
// A request is new until the owner has looked at the Tasks view once since it
// arrived. The mark is per device, like the inbox's arrivals.

const SEEN_KEY = 'moa-tasks-seen-at';

function readSeen() {
  try { return Number(localStorage.getItem(SEEN_KEY)) || 0; } catch (_) { return 0; }
}

// markTasksSeen — opening the view: what arrived before now stops being new
// the NEXT time, so the dots survive the visit that shows them.
export function markTasksSeen() {
  const before = readSeen();
  patch({ newSince: before });
  try { localStorage.setItem(SEEN_KEY, String(Date.now())); } catch (_) { /* ignore */ }
}

export function isNewRequest(task, newSince) {
  return !!task && task.place === 'you' && !!task.requester_session_id && task.status !== 'done'
    && (task.created_at || 0) > (newSince || 0);
}

// ── The sessions a task can name ─────────────────────────────────────────
// Rows name the session that asked, Move lists where a task can go, and a
// notice needs to know whether that session is loaded. The roster changes on
// every streamed token, so this keeps one reference until something a task
// shows (id, title, state, folder, kind) actually changes.

// Owners' own conversations are left out of the session roster
// (GET /api/sessions), yet a task can be asked by, or assigned to, an owner:
// the owners roster fills them in, named after the owner, so a task never
// calls one of them a deleted session.

let directory = { sessions: null, owners: null, sig: '', value: {} };

export function selectSessionDirectory(state) {
  const sessions = state?.sessions || {};
  const owners = state?.owners?.list || [];
  if (sessions === directory.sessions && owners === directory.owners) return directory.value;
  const ownerOf = new Map(owners.filter((o) => o?.session_id).map((o) => [o.session_id, o]));
  const entry = (s) => {
    const own = ownerOf.get(s.id);
    return {
      id: s.id, title: own?.name || s.title || '', state: s.state || '', cwd: s.cwd || own?.root || '',
      kind: own ? 'owner' : (s.kind || ''), updated: s.updated || 0,
    };
  };
  const all = Object.values(sessions).filter((s) => s?.id).map(entry);
  for (const own of ownerOf.values()) {
    if (!sessions[own.session_id]) all.push({ ...entry({ id: own.session_id }), state: own.session_state || 'saved' });
  }
  const rows = all.map((s) => [s.id, s.title, s.state, s.cwd, s.kind, s.updated]);
  const sig = JSON.stringify(rows);
  if (sig !== directory.sig) {
    const value = {};
    for (const s of all) value[s.id] = s;
    directory = { sessions, owners, sig, value };
  } else {
    directory = { ...directory, sessions, owners };
  }
  return directory.value;
}

export function selectSessionTasks(state, sessionId) {
  return tasksSlice(state).bySession[sessionId] || null;
}

// knownTasks — every task the client holds, freshest read first: the details
// (they carry unblocks), the global list, and the sessions' own lists. The
// dependency picker offers from it and checks cycles against it.
export function knownTasks(slice) {
  const out = [];
  for (const t of Object.values(slice.details || {})) if (t && !t.gone) out.push(t);
  out.push(...(slice.list || []));
  for (const [sid, data] of Object.entries(slice.bySession || {})) {
    const { requests, checklist } = sessionRecords(data, sid);
    out.push(...requests, ...checklist);
  }
  return out;
}

// ── An editor's draft across its own pages ───────────────────────────────
// On the phone "Waits for" is a page of the same sheet, so the editor is
// unmounted while it shows. Its unsaved draft waits here and is taken back
// when the editor mounts again. Keyed by task id, or 'new'.

const drafts = new Map();

export function stashDraft(key, entry) {
  drafts.set(String(key), entry);
}

export function peekDraft(key) {
  return drafts.get(String(key)) || null;
}

export function takeDraft(key) {
  const entry = drafts.get(String(key)) || null;
  drafts.delete(String(key));
  return entry;
}

export function addDraftWait(key, id) {
  const entry = drafts.get(String(key));
  if (!entry || entry.draft.waits_for.includes(id)) return;
  drafts.set(String(key), { ...entry, draft: { ...entry.draft, waits_for: [...entry.draft.waits_for, id] } });
}

// setDraftDest — Where, chosen on its own page (phone) for a task still being
// written: the stashed draft keeps it until the editor takes the draft back.
// patchDraft — a scheduled task's When or Send to, chosen on its own page
// (phone): the stashed draft keeps it until the editor takes it back.
export function patchDraft(key, fields) {
  const entry = drafts.get(String(key));
  if (!entry) return;
  drafts.set(String(key), { ...entry, draft: { ...entry.draft, ...fields } });
}

export function setDraftDest(key, dest) {
  const entry = drafts.get(String(key));
  if (!entry) return;
  drafts.set(String(key), { ...entry, dest });
}

// setTasksSession — narrow the Tasks view to one session's tasks (what it
// asked of you and its checklist), or widen it back with null.
export function setTasksSession(sessionId) {
  patch({ session: sessionId || null });
}
