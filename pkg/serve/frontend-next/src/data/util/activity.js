// activity.js — derives the activity-indicator label shown while the agent
// works. Three coarse phases (thinking / working / waiting) plus the special
// compacting and auto-verify states. In the "working" phase, when a tool is in
// flight, the label synthesizes a mid-level *intent* ("Running tests", "Editing
// code") from the running tool and its args — an honest, glanceable summary that
// is neither the task title nor the raw tool call (both of which the redesign
// rejects for this line). Between tools it falls back to a steady "Working";
// the live elapsed timer is what signals progress.

// TOOL_ACTIONS maps a tool name to its present-continuous intent phrase. bash is
// handled separately (its command classifies into an intent). Phrases are
// Title-case, no trailing punctuation, and short (≤ ~16 chars) so they never
// crowd the permission chip + spend on a narrow line.
const TOOL_ACTIONS = {
  read: 'Reading files',
  ls: 'Reading files',
  grep: 'Searching the code',
  find: 'Searching the code',
  write: 'Writing a file',
  edit: 'Editing code',
  multiedit: 'Editing code',
  apply_patch: 'Editing code',
  fetch_content: 'Fetching a page',
  web_search: 'Searching the web',
  send_file: 'Sending a file',
  subagent: 'Running a subagent',
};

// LIVE_VERBS maps a tool name to the present-continuous verb shown on the
// "running tool" row (RUNNING-TOOL-SPEC-FABLE.md §2 / TOOLCALLS-ALT-SPEC-FABLE.md
// direction B — Tail). Distinct from TOOL_ACTIONS above: that one names a mid-
// level *intent* for the coarse activity indicator ("Editing code"), this one is
// the verb paired with the tool's concrete object (toolPath) on the live row
// ("Editing pkg/serve/ws.go"). Closed table — unmapped/MCP tools fall back to
// "Calling".
const LIVE_VERBS = {
  read: 'Reading',
  ls: 'Reading',
  bash: 'Running',
  grep: 'Searching',
  find: 'Searching',
  web_search: 'Searching',
  edit: 'Editing',
  multiedit: 'Editing',
  apply_patch: 'Editing',
  write: 'Writing',
  fetch_content: 'Fetching',
  subagent: 'Delegating',
  send_file: 'Sending',
};

// liveVerb returns the present-continuous verb for a tool name, from
// LIVE_VERBS, or 'Calling' for anything not in the closed table (unknown/MCP
// tools).
export function liveVerb(name) {
  const n = (name || '').toLowerCase();
  return LIVE_VERBS[n] || 'Calling';
}

// BASH_INTENTS classifies a bash command (first line, lowercased) into an
// intent. First match wins, top to bottom, so order matters: specific verbs
// before the broad "any git" / "inspect" / catch-all buckets.
const BASH_INTENTS = [
  [/\b(go test|pytest|jest|vitest|cargo test|rspec|mocha|ctest)\b/, 'Running tests'],
  [/\b(npm|yarn|pnpm|bun)\s+(run\s+)?test\b/, 'Running tests'],
  [/\b(go build|make|cargo build|tsc|esbuild|webpack|vite build)\b/, 'Building'],
  [/\b(npm|yarn|pnpm|bun)\s+run\s+build\b/, 'Building'],
  [/\b(go vet|golangci-lint|eslint|ruff|clippy|prettier|gofmt|black|flake8)\b/, 'Linting'],
  [/\b(go mod|pip install|cargo add|brew install|apt(-get)?\s+install)\b/, 'Installing deps'],
  [/\b(npm|yarn|pnpm|bun)\s+(i|ci|add|install)\b/, 'Installing deps'],
  [/\bgit\s+commit\b/, 'Committing'],
  [/\bgit\s+push\b/, 'Pushing'],
  [/\bgit\b/, 'Running git'],
  [/\b(go run|npm start|node|python3?|deno run)\b/, 'Running the app'],
  [/\b(rg|grep|find|ls|cat|head|tail)\b/, 'Inspecting files'],
];

// tryParseArgs coerces a tool's args (which may be a JSON string or an object)
// into an object; returns null when it can't.
function tryParseArgs(args) {
  if (!args) return null;
  if (typeof args === 'object') return args;
  if (typeof args === 'string') {
    try {
      const parsed = JSON.parse(args);
      return parsed && typeof parsed === 'object' ? parsed : null;
    } catch {
      return null;
    }
  }
  return null;
}

// inFlightTool returns the tool message currently running in a session (the last
// 'tool_start' with status 'running'), or null. This is what activityAction
// synthesizes from — no new state, just what the store already holds.
export function inFlightTool(session) {
  const messages = session && session.messages;
  if (!Array.isArray(messages)) return null;
  for (let i = messages.length - 1; i >= 0; i--) {
    const m = messages[i];
    if (m && m._type === 'tool_start' && m.status === 'running') return m;
  }
  return null;
}

// activityAction synthesizes the mid-level intent phrase for the tool a session
// is currently running, or null when nothing is in flight. Zero LLM calls: it
// reads the running tool name + args and maps them through TOOL_ACTIONS /
// BASH_INTENTS. Honest by construction — it names the *category* of work, never
// the raw command.
export function activityAction(session) {
  const tool = inFlightTool(session);
  if (!tool) return null;
  const name = (tool.tool_name || '').toLowerCase();
  if (!name) return null;
  if (name === 'bash') {
    const args = tryParseArgs(tool.args);
    const command = args && typeof args.command === 'string' ? args.command : '';
    const firstLine = command.split('\n', 1)[0].toLowerCase();
    for (const [re, phrase] of BASH_INTENTS) {
      if (re.test(firstLine)) return phrase;
    }
    return 'Running a command';
  }
  return TOOL_ACTIONS[name] || 'Working';
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

// activityLabel builds the human text for the indicator given a coarse phase. In
// the working phase it returns a steady "Working"; callers that have the session
// (MobileComposer, StatusStrip) prefer activityAction(session) to name the tool
// in flight and fall back to this "Working" only between tools.
export function activityLabel(phase) {
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
      return 'Working';
    default:
      return null;
  }
}

// activityText resolves the full activity phrase for a session, following the
// SPEC order: special phases keep their fixed copy; the working phase names the
// in-flight tool via activityAction and falls back to "Working" between tools;
// idle (null phase) returns null so the segment hides. This is the single
// source both the mobile status line and the desktop status strip consume, so
// they never diverge. It never returns the task title or a raw tool call.
export function activityText(session) {
  const phase = activityPhase(session);
  if (phase === null) return null;
  if (phase === 'working') return activityAction(session) || 'Working';
  return activityLabel(phase);
}
