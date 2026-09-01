import { focusedSessionId } from "../../data/selectors.js";
import { sessionDisplayDotState, sessionTitle, shortPath } from "../../data/util/format.js";
import { allTileIds, findTile } from "../../data/tileTree.js";

function relAge(updated) {
  if (!updated) return "";
  const diff = Date.now() - updated;
  const min = Math.floor(diff / 60000);
  if (min < 1) return "now";
  if (min < 60) return `${min}m`;
  const h = Math.floor(min / 60);
  if (h < 24) return `${h}h`;
  const d = Math.floor(h / 24);
  return `${d}d`;
}

function spineBrief(sess) {
  const dot = sessionDisplayDotState(sess);
  if (dot === "permission") return "Needs you";
  if (dot === "error") return sess.error || "Error";
  if (dot === "unseen") return "New result";
  if (sess.briefProgress) return sess.briefProgress;
  if (sess.briefAttempting) return sess.briefAttempting;
  if (sess.state === "running") return "Working…";
  return "";
}

function toSpineRow(s, extra = {}) {
  const brief = spineBrief(s);
  return {
    id: s.id,
    title: sessionTitle(s),
    state: sessionDisplayDotState(s),
    unseen: !!s.unseen,
    when: relAge(s.updated),
    brief,
    path: brief ? "" : (shortPath(s.cwd) || s.cwd || ""),
    origin: s.origin || undefined,
    cwd: s.cwd || "",
    updated: s.updated || 0,
    ...extra,
  };
}

// spineSessions — open vs saved, newest first. Optional paneOf (session id →
// "P1") is the grid's badge; conversation view omits it.
export function spineSessions(sessions, paneOf) {
  const all = Object.values(sessions || {});
  const active = all
    .filter((s) => s.state !== "saved")
    .sort((a, b) => (b.updated || 0) - (a.updated || 0))
    .map((s) => toSpineRow(s, paneOf?.get(s.id) ? { pane: paneOf.get(s.id) } : {}));
  const saved = all
    .filter((s) => s.state === "saved")
    .sort((a, b) => (b.updated || 0) - (a.updated || 0))
    .map((s) => toSpineRow(s, { saved: true }));
  return { active, saved };
}

export function paneBadges(tileTree) {
  const paneOf = new Map();
  if (!tileTree) return paneOf;
  for (const [i, tileId] of allTileIds(tileTree).entries()) {
    const tile = findTile(tileTree, tileId);
    if (tile && tile.sessionId) paneOf.set(tile.sessionId, `P${i + 1}`);
  }
  return paneOf;
}

export function focusedTileSessionId(state) {
  const t = findTile(state.tileTree, state.focusedTile);
  return t ? t.sessionId : null;
}

function spineRowSig(row) {
  return [
    row.id, row.title, row.state, row.unseen ? 1 : 0, row.when, row.brief,
    row.path, row.origin || "", row.pane || "", row.saved ? 1 : 0,
  ].join("\0");
}

function spineListSig(list) {
  return (list || []).map(spineRowSig).join("\n");
}

function desktopChromeEqual(a, b) {
  return a.activeId === b.activeId
    && a.groupByProject === b.groupByProject
    && a.soundEnabled === b.soundEnabled
    && spineListSig(a.active) === spineListSig(b.active)
    && spineListSig(a.saved) === spineListSig(b.saved);
}

// selectDesktopChrome — roster the sidebar actually paints. Streaming text,
// tokens and messages are not on a row, so a token on any session returns the
// previous object (Object.is) and DesktopShell does not re-render.
export function selectDesktopChrome(state) {
  const inGrid = state.view === "grid";
  const paneOf = inGrid ? paneBadges(state.tileTree) : undefined;
  const { active, saved } = spineSessions(state.sessions, paneOf);
  const next = {
    active,
    saved,
    activeId: inGrid ? focusedTileSessionId(state) : focusedSessionId(state),
    groupByProject: !!state.groupByProject,
    soundEnabled: !!state.soundEnabled,
  };
  const prev = selectDesktopChrome._prev;
  if (prev && desktopChromeEqual(prev, next)) return prev;
  selectDesktopChrome._prev = next;
  return next;
}

export function __resetDesktopChromeForTests() {
  selectDesktopChrome._prev = null;
}
