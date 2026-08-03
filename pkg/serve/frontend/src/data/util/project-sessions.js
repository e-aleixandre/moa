import { fuzzyMatch } from "../fuzzy.js";
import { projectKey, projectLabel, shortPath } from "./format.js";

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

// Search stays global: it filters rows, retains their project metadata, and
// leaves the persisted accordion state untouched for the caller to restore.
export function filterProjectSections(sections, query) {
  const q = query.trim().toLowerCase();
  if (!q) return sections;
  return sections.flatMap((section) => {
    const sessions = section.sessions.filter((s) =>
      fuzzyMatch(q, `${s.title || ""} ${s.path || ""} ${s.cwd || ""}`.toLowerCase())
    );
    return sessions.length ? [{ ...section, sessions }] : [];
  });
}
