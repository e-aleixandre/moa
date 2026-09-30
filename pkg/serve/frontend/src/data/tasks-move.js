// tasks-move.js — where a task can go, arranged so that hundreds of sessions
// stay reachable without a list of hundreds: You, the owners, a few recent
// sessions, and the projects, each holding its backlog and its sessions.
// Pure: the Move picker draws what these return.

import { projectName } from './util/format.js';

export const RECENT_MAX = 5;
export const RECENT_WINDOW_MS = 24 * 3600 * 1000;
export const SEARCH_MAX = 40;

const WORKING = new Set(['running', 'permission']);

export const isWorking = (s) => !!s && WORKING.has(s.state);

const byActivity = (a, b) => (isWorking(b) - isWorking(a)) || ((b.updated || 0) - (a.updated || 0));

// projectLabels — a project is named as the sidebar names its folder. Two
// with the same name (a repository and one of its checkouts) add the folder
// that tells them apart, then the parent if that is not enough.
export function projectLabels(projects) {
  const segs = (p) => String(p.cwd || '').replace(/\/+$/, '').split('/').filter(Boolean);
  const base = (p) => projectName(p.cwd || '') || p.key || '';
  const label = (p) => {
    const s = segs(p);
    const last = s[s.length - 1] || '';
    return last && last !== base(p) ? `${base(p)} · ${last}` : base(p);
  };
  const tally = (f) => {
    const m = new Map();
    for (const p of projects) m.set(f(p), (m.get(f(p)) || 0) + 1);
    return m;
  };
  const byBase = tally(base);
  const byLabel = tally(label);
  const out = new Map();
  for (const p of projects) {
    if (byBase.get(base(p)) < 2) out.set(p.key, base(p));
    else if (byLabel.get(label(p)) < 2) out.set(p.key, label(p));
    else out.set(p.key, segs(p).slice(-2).join('/') || base(p));
  }
  return out;
}

// moveIndex — the picker's levels. `owners` is the owners roster (their own
// sessions are listed as owners, never again as sessions); `projects` come
// from /api/tasks/projects, whose `cwds` put each session under its project.
export function moveIndex({ sessions = {}, owners = [], projects = [], now = Date.now() } = {}) {
  const ownerIds = new Set(owners.map((o) => o.session_id).filter(Boolean));
  const ownerRows = owners
    .filter((o) => o.session_id)
    .map((o) => {
      const s = sessions[o.session_id];
      return { id: o.session_id, name: o.name || 'Owner', owner: o, state: s?.state || o.session_state || 'saved' };
    })
    .sort((a, b) => a.name.localeCompare(b.name));

  const labels = projectLabels(projects);
  const keyOf = new Map();
  for (const p of projects) {
    for (const cwd of p.cwds?.length ? p.cwds : [p.cwd]) if (cwd && !keyOf.has(cwd)) keyOf.set(cwd, p.key);
  }
  const groups = new Map(projects.map((p) => [p.key, { key: p.key, cwd: p.cwd || '', label: labels.get(p.key), sessions: [] }]));
  const all = Object.values(sessions).filter((s) => s?.id && !ownerIds.has(s.id) && s.kind !== 'owner');
  for (const s of all) {
    const key = keyOf.get(s.cwd);
    const g = key ? groups.get(key) : null;
    if (g) g.sessions.push(s);
  }
  const projectRows = [...groups.values()].map((g) => {
    g.sessions.sort(byActivity);
    return {
      ...g,
      working: g.sessions.filter(isWorking).length,
      updated: g.sessions.reduce((m, s) => Math.max(m, s.updated || 0), 0),
    };
  }).sort((a, b) => (b.updated - a.updated) || a.label.localeCompare(b.label));

  const recent = all
    .filter((s) => isWorking(s) || (s.state && s.state !== 'saved') || now - (s.updated || 0) < RECENT_WINDOW_MS)
    .sort(byActivity)
    .slice(0, RECENT_MAX);

  const labelOf = new Map(projectRows.map((p) => [p.key, p.label]));
  const projectOfSession = (s) => labelOf.get(keyOf.get(s.cwd)) || projectName(s.cwd || '') || '';
  return { owners: ownerRows, recent, projects: projectRows, sessions: all, projectOfSession };
}

// moveSearch — one query over every level: owners by name, backlogs by
// project, sessions by title or project. Sessions are capped; `more` says
// how many matched past the cap, so typing more is the way to reach them.
export function moveSearch(index, q, projectKey = null) {
  const needle = String(q || '').trim().toLowerCase();
  const has = (v) => String(v || '').toLowerCase().includes(needle);
  if (projectKey) {
    const p = index.projects.find((x) => x.key === projectKey);
    const hits = (p?.sessions || []).filter((s) => !needle || has(s.title));
    return { owners: [], projects: [], sessions: hits.slice(0, SEARCH_MAX), more: Math.max(0, hits.length - SEARCH_MAX) };
  }
  if (!needle) return { owners: [], projects: [], sessions: [], more: 0 };
  const owners = index.owners.filter((o) => has(o.name));
  const projects = index.projects.filter((p) => has(p.label));
  const sessions = index.sessions
    .filter((s) => has(s.title) || has(index.projectOfSession(s)))
    .sort((a, b) => (has(b.title) - has(a.title)) || byActivity(a, b));
  return { owners, projects, sessions: sessions.slice(0, SEARCH_MAX), more: Math.max(0, sessions.length - SEARCH_MAX) };
}
