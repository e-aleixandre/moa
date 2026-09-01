import { setState } from "../data/store.js";
import { setTileSession } from "../data/tileTree.js";

// Frozen conversations the real screens wear. The chrome is production; only
// this data is fake. Each roster row is a full session so opening it shows
// tools, a permission, a live subagent — not an empty stream.

const SPECIMEN_ID = "catalog-session";
const now = Date.now();

const user = (id, text, agoMs) => ({
  msg_id: id,
  role: "user",
  timestamp: now - agoMs,
  content: [{ type: "text", text }],
});
const assistant = (text) => ({
  role: "assistant",
  timestamp: now - 30000,
  content: [{ type: "text", text }],
});
const tool = (id, name, args, status = "done", result = "ok", extra = {}) => ({
  _type: "tool_start",
  tool_call_id: id,
  tool_name: name,
  args,
  status,
  result,
  ...extra,
});

const FAST_DIFF = `--- a/pkg/serve/frontend/src/layout/StatusStrip/StatusStrip.jsx
+++ b/pkg/serve/frontend/src/layout/StatusStrip/StatusStrip.jsx
@@ -80,3 +80,6 @@
   <PermissionControl />
+  {fast && <span class="fast">fast</span>}
   <TokenFlow />
`;

const specimen = {
  id: SPECIMEN_ID,
  title: "Fast mode on the status line",
  state: "idle",
  model: "Claude Opus 4.8",
  provider: "anthropic",
  thinking: "medium",
  fast: true,
  fastSupported: true,
  fastNote: "2.5× faster · billed as separate usage credits",
  cwd: "/home/ealeixandre/dev/moa",
  updated: now,
  permissionMode: "yolo",
  contextPercent: 41,
  contextWindow: 200000,
  compactAt: 0,
  costUSD: 1.24,
  runTokensUp: 12800,
  runTokensDown: 3400,
  messages: [
    user("u1", "Put fast on the desktop status strip, after yolo.", 180000),
    assistant("I'll hang it on the strip, same word as on the phone."),
    tool("t-read", "read", { path: "pkg/serve/frontend/src/layout/StatusStrip/StatusStrip.jsx" }, "done", "412 lines"),
    tool("t-edit", "edit", { path: "pkg/serve/frontend/src/layout/StatusStrip/StatusStrip.jsx" }, "done", FAST_DIFF, { start_line: 80 }),
    {
      role: "assistant",
      timestamp: now - 60000,
      content: [{ type: "text", text: "Done. The word sits after the permission chip, same colour as on mobile." }],
    },
  ],
  subagents: {},
};

const deploy = {
  id: "deploy",
  title: "deploy pulse api",
  state: "permission",
  model: "Claude Sonnet 4.6",
  provider: "anthropic",
  thinking: "low",
  cwd: "/home/ealeixandre/dev/moa/pulse-api",
  updated: now - 40000,
  unseen: true,
  permissionMode: "ask",
  contextPercent: 22,
  contextWindow: 200000,
  costUSD: 0.18,
  runTokensUp: 4100,
  runTokensDown: 900,
  pendingPerm: {
    id: "perm-deploy",
    tool_name: "bash",
    args: { command: "kubectl apply -f deploy/pulse-api.yaml" },
    allow_pattern: "Bash(kubectl apply *)",
  },
  messages: [
    user("d-u1", "Ship the pulse-api chart to staging.", 90000),
    assistant("Checking the manifest, then I'll apply it."),
    tool("d-read", "read", { path: "deploy/pulse-api.yaml" }, "done", "88 lines"),
    tool("d-bash", "bash", { command: "kubectl apply -f deploy/pulse-api.yaml" }, "running", null, {
      startedAt: now - 12000,
    }),
  ],
  subagents: {},
};

const raceSub = {
  jobId: "changelog",
  title: "Audit the send/close race",
  task: "Find the send/close race in the websocket manager and report the failing test.",
  model: "terra",
  async: true,
  status: "running",
  originToolCallId: "call_sa1",
  startedAtMs: now - 72000,
  contextPercent: 44,
  thinking: "low",
  usage: { inputTokens: 14200, outputTokens: 4100, costUSD: 0.031, elapsedMs: 72000 },
  messages: [
    tool("sa-grep", "grep", { pattern: "send/close" }, "done", "4 matches"),
    tool("sa-read", "read", { path: "pkg/serve/manager.go" }, "done", "640 lines"),
    tool("sa-test", "bash", { command: "go test -race ./pkg/serve -run TestSendClose" }, "running", null, {
      startedAt: now - 18000,
    }),
  ],
};

const wsRace = {
  id: "ws-race",
  title: "ws race fix",
  state: "running",
  model: "GPT Sol",
  provider: "openai",
  thinking: "high",
  cwd: "/home/ealeixandre/dev/moa/main",
  updated: now - 80000,
  permissionMode: "yolo",
  briefProgress: "Running tests",
  contextPercent: 58,
  contextWindow: 400000,
  costUSD: 0.62,
  runTokensUp: 22100,
  runTokensDown: 4800,
  runStartedAtMs: now - 80000,
  dockOpen: true,
  messages: [
    user("w-u1", "There's a flaky send/close race under load. Find it.", 120000),
    assistant("Sending a child at the manager, and grepping the parent while it runs."),
    tool("call_sa1", "subagent", { task: raceSub.task, async: true }, "running"),
    tool("w-grep", "grep", { pattern: "close\\(\\)" }, "running", null, { startedAt: now - 9000 }),
  ],
  subagents: { changelog: raceSub },
};

const sqlite = {
  id: "sqlite",
  title: "migrate sqlite",
  state: "error",
  model: "Claude Opus 4.8",
  provider: "anthropic",
  thinking: "medium",
  cwd: "/home/ealeixandre/dev/moa/migrate",
  updated: now - 18 * 60000,
  unseen: true,
  error: "provider 429",
  permissionMode: "yolo",
  contextPercent: 71,
  contextWindow: 200000,
  costUSD: 2.04,
  runTokensUp: 48000,
  runTokensDown: 1200,
  messages: [
    user("s-u1", "Move the session store off the JSON files onto sqlite.", 40 * 60000),
    assistant("I'll sketch the schema first, then a dry-run migration."),
    tool("s-read", "read", { path: "pkg/session/store.go" }, "done", "310 lines"),
    {
      role: "assistant",
      timestamp: now - 18 * 60000,
      content: [{ type: "text", text: "Stopped — the provider returned 429 on the next request." }],
    },
  ],
  subagents: {},
};

const docsSub = {
  jobId: "docs",
  title: "Tighten the strip copy",
  task: "Rewrite the status-strip comments so they match what shipped.",
  model: "sonnet",
  async: false,
  status: "completed",
  usage: { inputTokens: 8200, outputTokens: 1100, costUSD: 0.02, elapsedMs: 41000 },
  messages: [
    tool("docs-read", "read", { path: "pkg/serve/frontend/src/layout/StatusStrip/StatusStrip.jsx" }, "done", "412 lines"),
    { role: "assistant", content: [{ type: "text", text: "Copy matches the shipped strip. Three comments rewritten." }] },
  ],
};

const frontend = {
  id: "frontend",
  title: "frontend polish",
  state: "idle",
  model: "Claude Sonnet 4.6",
  provider: "anthropic",
  thinking: "low",
  cwd: "/home/ealeixandre/dev/moa/frontend-polish",
  updated: now - 2 * 3600000,
  permissionMode: "yolo",
  contextPercent: 19,
  contextWindow: 200000,
  costUSD: 0.41,
  runTokensUp: 6400,
  runTokensDown: 2100,
  messages: [
    user("f-u1", "The sidebar rows still look like cards. Flatten them.", 3 * 3600000),
    assistant("Two lines, no peach bar. I'll send a child at the copy."),
    tool("subagent-docs", "subagent", { task: docsSub.task }, "done", "3 files · copy tightened"),
    {
      role: "assistant",
      timestamp: now - 2 * 3600000,
      content: [{ type: "text", text: "Rows are two lines now. Title and time on top, brief or path below." }],
    },
  ],
  subagents: { docs: docsSub },
};

const verifier = {
  id: "verifier",
  title: "verifier design notes",
  state: "saved",
  model: "Claude Haiku 4.5",
  provider: "anthropic",
  thinking: "off",
  cwd: "/home/ealeixandre/dev/moa/main",
  updated: now - 3 * 86400000,
  messages: [
    user("v-u1", "Park the verifier notes here so I can pick them up later.", 3 * 86400000),
    assistant("Saved. The STATE.md of a goal should not live in the repo."),
  ],
  subagents: {},
};

export const CATALOG_SESSIONS = {
  [SPECIMEN_ID]: specimen,
  deploy,
  "ws-race": wsRace,
  sqlite,
  frontend,
  verifier,
};

export function seedCatalogStore() {
  setState((s) => ({
    sessions: { ...CATALOG_SESSIONS },
    sessionsLoaded: true,
    activeSession: SPECIMEN_ID,
    usage: {
      available: true,
      five_hour: { utilization: 42, resets_at: new Date(Date.now() + 3 * 3600000).toISOString() },
      seven_day: { utilization: 61, resets_at: new Date(Date.now() + 4 * 86400000).toISOString() },
    },
    tileTree: setTileSession(s.tileTree, s.focusedTile, SPECIMEN_ID),
  }));
}
