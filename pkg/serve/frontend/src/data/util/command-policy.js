// command-policy.js — client-side mirror of pkg/bus/queue_policy.go.
//
// Classifies how a slash command behaves when typed while the session is BUSY
// (a run is in flight) or the queue rail is non-empty. When the session is idle
// AND the queue is empty every command runs immediately regardless of policy;
// the policy only decides what happens to a command issued mid-run. The backend
// enforces the same table (ExecCommand gates on bus.ClassifyCommand) — this copy
// lets the UI choose the right optimistic feedback without a round-trip.

export const POLICY_INSTANT = 'instant';
export const POLICY_QUEUE = 'queue';
export const POLICY_REJECT = 'reject';

// Rewrite/reconfigure the run — must wait for idle (enqueued as a barrier).
const QUEUE = new Set(['compact', 'clear', 'model', 'thinking', 'verify']);
// Mode transitions / destructive rewind — refused while busy.
const REJECT = new Set(['undo', 'branch', 'back', 'plan']);

// splitCommand normalizes a raw slash line into [name, rest]. A leading slash is
// optional; surrounding whitespace is trimmed. Mirrors bus.splitCommand.
function splitCommand(raw) {
  let s = (raw || '').trim();
  if (s.startsWith('/')) s = s.slice(1);
  s = s.trim();
  if (!s) return ['', ''];
  const i = s.search(/[ \t]/);
  if (i >= 0) return [s.slice(0, i).toLowerCase(), s.slice(i + 1).trim()];
  return [s.toLowerCase(), ''];
}

// classifyCommand returns the queue policy for a raw slash command issued while
// busy. goal is argument-dependent: "goal"/"goal status"/"goal stop" run
// instantly; "goal <objective>" (or "goal start") starts a run and must wait.
export function classifyCommand(raw) {
  const [name, rest] = splitCommand(raw);
  if (!name) return POLICY_INSTANT;
  if (name === 'goal') {
    const fields = rest.split(/\s+/).filter(Boolean);
    if (fields.length === 0) return POLICY_INSTANT;
    return (fields[0] === 'status' || fields[0] === 'stop') ? POLICY_INSTANT : POLICY_QUEUE;
  }
  if (QUEUE.has(name)) return POLICY_QUEUE;
  if (REJECT.has(name)) return POLICY_REJECT;
  return POLICY_INSTANT;
}
