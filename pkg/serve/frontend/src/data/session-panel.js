// session-panel.js — the session panel's controller and its pure model.
//
// The panel is the session's DOSSIER: what the session IS and what it HAS
// DONE. The status line keeps the controls for the next turn (model, thinking,
// permissions, fast); this is the other end of that rule — everything the line
// sheds as the dock narrows is here, in full, always.
//
// One ephemeral global slice (state.sessionPanel), like the artifacts drawer:
// which conversation it belongs to and which page inside it is showing. It is
// never persisted and never stored inside session metadata.

import { store, setState, SESSION_PANEL_CLOSED } from './store.js';
import { fmtCost, fmtReset, usageForSession } from './util/usage-pills.js';
import { cacheVerdict } from './cache-usage.js';
import { fmtTokens } from './util/format.js';

// The second level. A row on the root pushes one of these INSIDE the panel;
// "back" returns to the root. No modal ever opens over the panel.
// Artifacts is deliberately absent: its row opens the drawer, which is the
// one list of files in the product. It was a page here once, with its own
// shape and its own reader, and two lists of the same thing is one too many.
export const PANEL_PAGES = {
  overview: 'Overview',
  ownerEdit: 'Edit owner',
  book: 'Book',
  usage: 'Usage',
  mcp: 'MCP',
};

// The second level is not always one step deep. Edit owner is entered FROM
// Overview, so its back has to land there and not on the root — it used to be
// a modal opened over the panel, which on a phone stacked a second sheet with
// a second grabber and a second ✕ on top of the first one.
export const PANEL_PAGE_PARENT = {
  ownerEdit: 'overview',
};

// panelPageParent — where "back" goes from a page. The root's parent is the
// root: the panel itself is what closes from there.
export function panelPageParent(page) {
  return PANEL_PAGE_PARENT[page] || 'root';
}

// panelAccessibleName names the surface after the page currently in front of
// the user. At the root, the dossier still names what it belongs to.
export function panelAccessibleName(session, page) {
  return PANEL_PAGES[page] || (session?.kind === 'owner' ? 'This owner' : 'This session');
}

// A pushed page replaces the control that opened it. Put the keyboard at its
// Back button, whose label says exactly where that control returns.
export function focusPanelSubpage({ open, page, backButton }) {
  if (open && page !== 'root') backButton?.focus();
}

export { SESSION_PANEL_CLOSED };

export function sessionPanelSlice(state) {
  return state?.sessionPanel || SESSION_PANEL_CLOSED;
}

// sessionPanelView answers what a given conversation's panel should render:
// closed for anyone who is not the owner, so two panes can never both claim it.
export function sessionPanelView(state, sessionId) {
  const slice = sessionPanelSlice(state);
  const open = !!sessionId && slice.open && slice.ownerSessionId === sessionId;
  return { open, page: open ? slice.page : 'root' };
}

function patch(next) {
  setState((s) => ({ sessionPanel: { ...sessionPanelSlice(s), ...next } }));
}

// openSessionPanel — the crumb (root) and the context ring (usage) are its two
// doors. Opening it for another conversation moves it rather than stacking a
// second one.
export function openSessionPanel(sessionId, page = 'root') {
  if (!sessionId) return;
  patch({
    ownerSessionId: sessionId,
    open: true,
    page: page === 'root' || page in PANEL_PAGES ? page : 'root',
  });
}

export function closeSessionPanel() {
  patch({ open: false, page: 'root' });
}

// toggleSessionPanel — the crumb is a toggle: the same tap that opened the
// panel closes it. Asking for a PAGE while the root is open is a push, not a
// close, so the ring can promote an already-open panel to Usage.
export function toggleSessionPanel(sessionId, page = 'root') {
  const slice = sessionPanelSlice(store.get());
  const mine = slice.open && slice.ownerSessionId === sessionId;
  if (mine && (page === 'root' || slice.page === page)) {
    closeSessionPanel();
    return;
  }
  openSessionPanel(sessionId, page);
}

export function setSessionPanelPage(page) {
  patch({ page: page === 'root' || page in PANEL_PAGES ? page : 'root' });
}

// closeSessionPanelForSession — a deleted conversation cannot keep a dossier
// open. Closing (unloading) does not need this: a saved session still has one.
export function closeSessionPanelForSession(sessionId) {
  const slice = sessionPanelSlice(store.get());
  if (slice.ownerSessionId === sessionId) patch(SESSION_PANEL_CLOSED);
}

/* ── The run facts ─────────────────────────────────────────────────────────
   The other end of the status line's priority rule (statusItemPriority): a
   narrow screen shows fewer things on the LINE, never fewer things known.
   Every fact hides itself when its datum is missing rather than showing an
   invented zero — the same house rule the usage panel keeps.

   Fast is here as a FACT of the run; its control stays in the model picker.
   Context is NOT here: it never leaves the line, and its detail is one row
   down, on the Usage page. */
export function runFacts(session) {
  const s = session || {};
  const facts = [];

  const up = s.runTokensUp;
  const down = s.runTokensDown;
  if ((up || 0) > 0 || (down || 0) > 0) {
    facts.push({ id: 'tokens', label: 'Tokens', value: `↑${fmtTokens(up || 0)} ↓${fmtTokens(down || 0)}` });
  }

  if (typeof s.costUSD === 'number' && s.costUSD > 0) {
    facts.push({ id: 'spend', label: 'Spend', value: fmtCost(s.costUSD) });
  }

  // Turns are counted from the transcript we hold. A truncated history would
  // make the count a lower bound, and a number that is quietly wrong is worse
  // than no number, so it is omitted until the whole conversation is loaded.
  const messages = Array.isArray(s.messages) ? s.messages : [];
  const truncated = !!s.historyTruncated || !!s.olderHistory?.hasMore;
  if (!truncated && messages.length > 0) {
    const turns = messages.filter((m) => m && m.role === 'user').length;
    if (turns > 0) facts.push({ id: 'turns', label: 'Turns', value: String(turns) });
  }

  if (s.fast) facts.push({ id: 'fast', label: 'Fast', value: 'on' });

  if (s.goalActive) {
    facts.push({
      id: 'goal',
      label: 'Goal',
      value: s.goalVerifying ? 'verifying…' : s.goalIteration ? `iteration ${s.goalIteration}` : 'active',
    });
  }

  const tasks = Array.isArray(s.tasks) ? s.tasks : [];
  if (tasks.length > 0) {
    const done = tasks.filter((t) => t.status === 'done').length;
    facts.push({ id: 'tasks', label: 'Tasks', value: `${done}/${tasks.length}` });
  }

  return facts;
}

/* ── The three rows ────────────────────────────────────────────────────────
   A row says its one-line VERDICT — the thing you came to check — and the page
   behind it carries the detail. The verdict may wear state colour when it IS a
   state (an MCP server down); a plain number never does. */

// usageVerdict names the provider, so a quota never reads as global: it is the
// answer to "can THIS session keep going?", and usageForSession already picks
// the window of the provider this session is on.
export function usageVerdict(session, globalUsage) {
  // A cache streak outranks the quota reading. The quota says how much is
  // left; the streak says the session is spending it several times faster
  // than it should, which is the thing you would want to act on first.
  const cache = cacheVerdict(session);
  if (cache.warn) return cache;

  const u = usageForSession(session, globalUsage);
  const provider = session?.provider || 'anthropic';
  const name = provider === 'openai' ? 'OpenAI' : provider === 'anthropic' ? 'Anthropic' : provider;
  const meter = u.fiveHour || u.week;
  if (meter) {
    const label = meter.label || (meter === u.fiveHour ? '5h' : 'week');
    const reset = meter.resetsAt ? ` · ${fmtReset(meter.resetsAt)} left` : '';
    return { text: `${name} · ${label} ${meter.pct}%`, warn: meter.pct >= 90, note: reset };
  }
  if (typeof session?.costUSD === 'number' && session.costUSD > 0) {
    return { text: fmtCost(session.costUSD), warn: false };
  }
  return { text: 'no quota reported', warn: false };
}

export function mcpVerdict(session) {
  const mcp = session?.mcp;
  if (!mcp || !mcp.total) return null; // no servers: no row, not an empty one
  if (mcp.unhealthy > 0) {
    return { text: `${mcp.unhealthy} of ${mcp.total} down`, warn: true };
  }
  const disabled = mcp.disabled > 0 ? ` · ${mcp.disabled} off` : '';
  return { text: `${mcp.total} ready${disabled}`, warn: false };
}

export function artifactsVerdict(count, status) {
  if (status === 'loading' && count === 0) return { text: '…', warn: false };
  if (count === 0) return { text: 'none yet', warn: false };
  return { text: count === 1 ? '1 file' : `${count} files`, warn: false };
}
