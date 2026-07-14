// activity.js — derives the activity-indicator label shown while the agent
// works. Three coarse phases (thinking / working / waiting) plus the special
// compacting and auto-verify states. In the "working" phase a rotating list of
// playful gerunds keeps a long run from looking stuck, cycling by elapsed time
// so it advances on its own without extra state. The gerunds are cosmetic; they
// never claim to describe a specific tool (those are visible in the chat).

export const WORKING_GERUNDS = [
  'Percolating',
  'Noodling',
  'Simmering',
  'Cooking',
  'Brewing',
  'Tinkering',
  'Crunching',
  'Churning',
  'Wrangling',
  'Conjuring',
  'Marinating',
  'Whirring',
];

const GERUND_PERIOD_MS = 4000;

// gerundFor picks the working-phase word for a given elapsed time so the label
// advances every GERUND_PERIOD_MS. elapsedMs <= 0 (or missing) yields the first
// word for a stable initial frame.
export function gerundFor(elapsedMs) {
  const e = Number.isFinite(elapsedMs) && elapsedMs > 0 ? elapsedMs : 0;
  const i = Math.floor(e / GERUND_PERIOD_MS) % WORKING_GERUNDS.length;
  return WORKING_GERUNDS[i];
}

// formatElapsed renders a compact mm:ss-ish counter: "8s", "1m03s", "12m".
export function formatElapsed(elapsedMs) {
  if (!Number.isFinite(elapsedMs) || elapsedMs < 0) return '';
  const total = Math.floor(elapsedMs / 1000);
  if (total < 60) return `${total}s`;
  const m = Math.floor(total / 60);
  const s = total % 60;
  if (m < 60) return s === 0 ? `${m}m` : `${m}m${String(s).padStart(2, '0')}s`;
  const h = Math.floor(m / 60);
  const mm = m % 60;
  return mm === 0 ? `${h}h` : `${h}h${String(mm).padStart(2, '0')}m`;
}

// activityPhase classifies what the agent is doing into a coarse phase used by
// the indicator. Returns one of: 'compacting', 'verifying', 'waiting',
// 'thinking', 'working', or null when there is no activity to show.
export function activityPhase(session) {
  if (!session) return null;
  if (session.compacting) return 'compacting';
  if (session.autoVerifying) return 'verifying';
  // Blocked on the user — reclaims attention. A permission prompt flips the
  // session state to 'permission'; ask_user keeps the run 'running' but sets
  // pendingAsk. Either way the agent is waiting on a human, so both map to the
  // same phase for parity with the TUI's "Waiting for you".
  if (session.state === 'permission' || session.pendingAsk) return 'waiting';
  if (session.state !== 'running') return null;
  if (session.thinkingText) return 'thinking';
  return 'working';
}

// activityLabel builds the human text for the indicator. In the working phase
// it rotates the gerunds by elapsed time. Special phases keep their fixed copy.
export function activityLabel(phase, elapsedMs) {
  switch (phase) {
    case 'compacting':
      return 'Compacting context';
    case 'verifying':
      return 'Running auto-verify';
    case 'waiting':
      return 'Waiting for you';
    case 'thinking':
      return 'Thinking';
    case 'working':
      return gerundFor(elapsedMs);
    default:
      return null;
  }
}
