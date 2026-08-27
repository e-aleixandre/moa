import { projectKey, projectLabel, shortPath } from "./format.js";

export const PROJECT_SAVED_PREVIEW_LIMIT = 5;

const updated = (session) => session.updated || 0;
const isSaved = (session) => session.state === "saved" || session.saved;
const attention = (session) => session.state === "permission" ? "permission" : session.state === "error" ? "error" : null;

function sessionOrder(a, b) {
  const aAttention = attention(a);
  const bAttention = attention(b);
  if (aAttention && !bAttention) return -1;
  if (!aAttention && bAttention) return 1;
  if (isSaved(a) !== isSaved(b)) return isSaved(a) ? 1 : -1;
  return updated(b) - updated(a);
}

export function projectSectionOrder(a, b) {
  if (!a.key) return 1;
  if (!b.key) return -1;
  return b.updated - a.updated;
}

// groupProjectSessions is deliberately presentation-agnostic: callers can pass
// server sessions or their own row projections as long as cwd/state/updated exist.
export function groupProjectSessions(sessions) {
  const groups = new Map();
  for (const session of sessions) {
    const key = projectKey(session.cwd);
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(session);
  }
  return [...groups.entries()].map(([key, groupSessions]) => {
    const openCount = groupSessions.filter((s) => !isSaved(s)).length;
    const savedCount = groupSessions.length - openCount;
    const hasPermission = groupSessions.some((s) => attention(s) === "permission");
    const hasError = groupSessions.some((s) => attention(s) === "error");
    const attentionCount = groupSessions.filter((s) => attention(s) === (hasPermission ? "permission" : "error")).length;
    return {
      key,
      label: projectLabel(key),
      path: shortPath(key),
      sessions: [...groupSessions].sort(sessionOrder),
      openCount,
      savedCount,
      attention: hasPermission ? "permission" : hasError ? "error" : null,
      attentionCount: hasPermission || hasError ? attentionCount : 0,
      updated: Math.max(...groupSessions.map(updated)),
    };
  }).sort(projectSectionOrder);
}

export function defaultProjectCollapsed(section) {
  return section.openCount === 0;
}

export function projectCollapsed(section, drawerCollapsed, searching) {
  if (searching) return false;
  return Object.hasOwn(drawerCollapsed, section.key)
    ? drawerCollapsed[section.key]
    : defaultProjectCollapsed(section);
}

export function pruneDrawerCollapsed(drawerCollapsed, sessions) {
  const keys = new Set(sessions.map((session) => projectKey(session.cwd)));
  return Object.fromEntries(Object.entries(drawerCollapsed).filter(([key]) => keys.has(key)));
}

function searchText(value) {
  return String(value || "")
    .normalize("NFD")
    .replace(/\p{M}/gu, "")
    .toLowerCase();
}

// Session titles are sentence-length and the list can hold hundreds of rows,
// so palette-style subsequence matching turns ordinary words into noise. Match
// every whitespace-delimited term as a literal substring instead. NFD folding
// is cheap here and lets Spanish keyboard input find both "sesión" and
// "sesion" without changing command actions' deliberately fuzzy matching.
//
// The model name stays in the haystack because the palette has always let you
// find sessions by it; the drawer simply never had a model to match against.
export function sessionSearchMatch(query, session) {
  const terms = searchText(query).trim().split(/\s+/).filter(Boolean);
  if (!terms.length) return true;
  const haystack = searchText(`${session.title || ""} ${session.model || ""} ${session.path || ""} ${session.cwd || ""}`);
  return terms.every((term) => haystack.includes(term));
}

// A project should reveal its shape before one archive consumes the viewport.
// Open rows stay unconditionally visible: hiding a running or attention-needed
// session behind an affordance is worse than a long group. The five newest
// saved rows give useful recent context while leaving room for neighbouring
// projects on a phone; searching and an explicit expansion reveal the archive.
export function visibleProjectSessions(section, expanded = false, searching = false) {
  if (expanded || searching) return section.sessions;
  let savedShown = 0;
  return section.sessions.filter((session) => {
    if (!isSaved(session)) return true;
    if (savedShown >= PROJECT_SAVED_PREVIEW_LIMIT) return false;
    savedShown++;
    return true;
  });
}

export function hiddenProjectSavedCount(section, expanded = false, searching = false) {
  return section.sessions.length - visibleProjectSessions(section, expanded, searching).length;
}

// The recency view lists every project in one flat run, so it needs its own
// cap: without one the drawer builds a card per saved session on open, which a
// phone pays for in dropped frames before the first row is readable.
//
// The limit is higher than a project section's because this caps the whole
// roster rather than one folder's tail, and it only applies to the resting
// view: an explicit expansion or an active search means the user asked for
// those rows, so they all render.
export const SAVED_PREVIEW_LIMIT = 20;

export function previewSavedSessions(sessions, { expanded = false, searching = false } = {}) {
  if (expanded || searching || sessions.length <= SAVED_PREVIEW_LIMIT) {
    return { visible: sessions, hidden: 0 };
  }
  return { visible: sessions.slice(0, SAVED_PREVIEW_LIMIT), hidden: sessions.length - SAVED_PREVIEW_LIMIT };
}

// Search stays global: it filters rows, retains their project metadata, and
// leaves the persisted accordion state untouched for the caller to restore.
export function filterProjectSections(sections, query) {
  if (!query.trim()) return sections;
  return sections.flatMap((section) => {
    const sessions = section.sessions.filter((session) => sessionSearchMatch(query, session));
    return sessions.length ? [{ ...section, sessions }] : [];
  });
}
