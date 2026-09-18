import { projectKey, projectLabel, shortPath } from "./format.js";

export const PROJECT_SAVED_PREVIEW_LIMIT = 5;

// The two ways the sidebar arranges what it shows: by recency, or grouped by
// folder. The segmented says ORDER, and nothing else — the project owners are
// a SECTION of the list (docs/owners.md), not a third ordering of it. A mode
// is chosen and kept, which is why it is persisted, and it lives here rather
// than in a controller so the store can validate a restored value without
// importing the module that reads it back.
export const SIDEBAR_MODES = ["recent", "project"];

// The sections of the Recent list that can be folded away. Needs attention is
// deliberately absent: it is a PROMOTION rather than a list you keep — empty
// when nothing is wrong — and a collapsed alarm is an alarm you have chosen
// not to hear.
export const COLLAPSIBLE_SECTIONS = ["owners", "active", "saved"];

// sectionCollapsed reads the persisted accordion, defaulting to open. Same
// shape as projectCollapsed so the column has one collapsing gesture.
export function sectionCollapsed(key, collapsedSections = {}) {
  return collapsedSections?.[key] === true;
}

// isOrdinarySession excludes a project owner's conversation from the lists of
// SESSIONS. The roster holds it — it is opened, streamed and read like any
// other session — but it is not work waiting for you, and an owner listed
// among the sessions it is responsible for would be one of its own rows. It
// has a row of its own in the OWNERS section instead (docs/owners.md).
export function isOrdinarySession(session) {
  return (session?.kind || "") !== "owner";
}

// ordinarySessions is the filter every session list applies to the roster.
export function ordinarySessions(sessions = []) {
  return sessions.filter(isOrdinarySession);
}

const updated = (session) => session.updated || 0;
const isSaved = (session) => session.state === "saved" || session.saved;
const attention = (session) => session.state === "permission" ? "permission" : session.state === "error" ? "error" : null;

function sessionOrder(a, b) {
  // Unseen is a needs-you state in the list (attentionKind), so it rises to
  // the top of its project the same way permission and error do. The older
  // `attention()` helper still drives the heading's permission/error mark.
  const aKind = attentionKind(a);
  const bKind = attentionKind(b);
  if (aKind || bKind) {
    const aRank = aKind ? ATTENTION_RANK[aKind] : 99;
    const bRank = bKind ? ATTENTION_RANK[bKind] : 99;
    if (aRank !== bRank) return aRank - bRank;
  }
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
export function groupProjectSessions(sessions, owners = []) {
  const groups = new Map();
  for (const session of sessions) {
    const key = projectKey(session.cwd);
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(session);
  }
  // A project with an owner but no session still has a group: the owner is
  // its first row, and without the group it would be nowhere in this order.
  for (const owner of owners) {
    const root = owner?.root;
    if (!root) continue;
    const key = projectKey(root);
    const covered = [...groups.keys()].some((k) => k === key || k.startsWith(`${key}/`) || key.startsWith(`${k}/`));
    if (!covered) groups.set(key, []);
  }
  const ownerKeys = new Set(owners.map((o) => o?.root && projectKey(o.root)).filter(Boolean));
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
      updated: groupSessions.length ? Math.max(...groupSessions.map(updated)) : 0,
      hasOwner: [...ownerKeys].some((k) => k === key || k.startsWith(`${key}/`) || key.startsWith(`${k}/`)),
    };
  }).sort(projectSectionOrder);
}

export function defaultProjectCollapsed(section) {
  return section.openCount === 0 && !section.hasOwner;
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

/* ── Needs attention ───────────────────────────────────────────────────────
   The sidebar's first group. Three states qualify, and the order is how much
   of your work is stopped rather than when it happened: a session blocked on a
   question, then one that died, then an answer nobody has read.

   `unseen` joins the two states this file already counted because
   sessionDisplayDotState treats it as a display state of its own, and because
   a finished answer you have not read is the third way a session waits for
   you. It is deliberately limited to non-saved sessions: a saved session is
   parked on purpose and asks for nothing. */
const ATTENTION_RANK = { permission: 0, error: 1, unseen: 2 };

export function attentionKind(session) {
  if (!session) return null;
  /* An owner never rises into Needs attention. A session leaves Active for the
     promotion and comes back once it is answered, which works because a
     session is a piece of work that ends; an owner is STANDING, and a
     permanent row that moves between sections is a row you have to find again
     every time its state changes. Its state is painted on its own row instead.
     The roster filter (isOrdinarySession) already keeps owners out of these
     lists — this is the same rule stated where the split is made, so a caller
     that hands over a raw roster cannot promote one. */
  if ((session.kind || "") === "owner") return null;
  if (session.state === 'permission' || session.pendingPerm || session.pendingAsk) return 'permission';
  if (session.state === 'error') return 'error';
  if (session.unseen && !isSaved(session)) return 'unseen';
  return null;
}

/** Split a session list into the ones that stop without you and the rest.
 *  Both halves keep the caller's order; only the attention half is re-sorted,
 *  by urgency and then by the list's own recency. */
export function partitionByAttention(sessions = []) {
  const needs = [], rest = [];
  for (const s of sessions) (attentionKind(s) ? needs : rest).push(s);
  needs.sort((a, b) => {
    const d = ATTENTION_RANK[attentionKind(a)] - ATTENTION_RANK[attentionKind(b)];
    return d !== 0 ? d : updated(b) - updated(a);
  });
  return { needs, rest };
}
