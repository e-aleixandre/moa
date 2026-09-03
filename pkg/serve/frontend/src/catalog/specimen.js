import { setState } from "../data/store.js";
import { setTileSession } from "../data/tileTree.js";
import { skillForkLaunchRow } from "../data/ws/history.js";

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

// A forked skill, launched both ways, so the two entry points can be compared
// side by side. The model's own load_skill shows as an ordinary subagent row;
// the user's /<name> shows as the anchored launch row. Both fold their terminal
// card into the same `subagent-<jobId>` key, and both report back to the parent.
const reviewSub = {
  jobId: "sa-review",
  title: "Delivery review",
  task: "skill: delivery-review",
  model: "terra",
  async: true,
  status: "completed",
  accentIndex: 2,
  usage: { inputTokens: 31400, outputTokens: 2600, costUSD: 0.14, elapsedMs: 68000 },
  messages: [
    tool("rv-read", "read", { path: "pkg/serve/skills_http.go" }, "done", "240 lines"),
    { role: "assistant", content: [{ type: "text", text: "Reachable through the real entry point. One gap: the anchor is dropped when the agent is busy." }] },
  ],
};

const learnSub = {
  jobId: "sa-learn",
  title: "Systemic learning",
  task: "skill: systemic-learning",
  model: "fable",
  async: true,
  status: "running",
  accentIndex: 3,
  startedAtMs: now - 46000,
  contextPercent: 12,
  thinking: "low",
  usage: { inputTokens: 22800, outputTokens: 900, costUSD: 0.09, elapsedMs: 46000 },
  messages: [
    tool("ln-read", "read", { path: "/home/ealeixandre/.config/moa/sessions/…/transcript.md" }, "done", "3330 lines"),
  ],
};

const skillFork = {
  id: "skill-fork",
  title: "forked skills",
  state: "running",
  model: "Claude Opus 4.8",
  provider: "anthropic",
  thinking: "medium",
  cwd: "/home/ealeixandre/dev/moa/skill-fork-notify",
  updated: now - 40000,
  permissionMode: "yolo",
  contextPercent: 33,
  contextWindow: 200000,
  costUSD: 0.88,
  runTokensUp: 9100,
  runTokensDown: 2400,
  dockOpen: true,
  messages: [
    user("k-u1", "Check the fork lands before we ship it.", 300000),
    // (1) THE MODEL launched it itself with load_skill: an ordinary subagent
    // tool row, keyed by the job it spawned.
    assistant("Running the delivery review as a child so I keep this thread."),
    tool("subagent-sa-review", "subagent", { task: reviewSub.task }, "done", "verified · 1 gap found", {
      subagentJobId: "sa-review",
      accentIndex: 2,
      finishedAtMs: now - 120000,
    }),
    {
      role: "assistant",
      timestamp: now - 110000,
      content: [{ type: "text", text: "It came back with one gap: the anchor is dropped while the agent is busy. Fixed on the steer lane." }],
    },
    // (2) THE USER launched it with /systemic-learning. Built by the real
    // production projection (skillForkLaunchRow), not hand-written, so the lab
    // shows what the transcript actually renders — including if it regresses.
    skillForkLaunchRow({
      msg_id: "k-anchor",
      custom: { source: "skill_fork", subagent_job_id: "sa-learn", skill: "systemic-learning" },
    }),
  ],
  subagents: { "sa-review": reviewSub, "sa-learn": learnSub },
};

// The stretch a session reopened hours later could not explain: the notice, the
// work the model did because of it, and the compaction that followed. The
// notice is deliberately quiet — it is not an error or an alert, it is the
// reason the model paused the task to write things down.
const compaction = {
  id: "compaction",
  title: "long refactor",
  state: "idle",
  model: "Claude Opus 4.8",
  provider: "anthropic",
  thinking: "medium",
  cwd: "/home/ealeixandre/dev/moa/main",
  updated: now - 5 * 3600000,
  contextPercent: 24,
  contextWindow: 200000,
  costUSD: 3.42,
  messages: [
    user("c-u1", "Sigue con el refactor de los handlers, que quedaba a medias.", 7 * 3600000),
    {
      role: "assistant",
      timestamp: now - 7 * 3600000 + 20000,
      content: [{ type: "text", text: "Sigo por donde lo dejamos: separo los handlers de sesión antes de tocar el bus." }],
    },
    tool("c-edit", "edit", { path: "pkg/serve/session_handlers.go" }, "done", "12 lines changed"),
    // The notice: a quiet system line, not a user turn and not an alert.
    {
      _type: "system",
      _msg_id: "c-notice",
      timestamp: now - 6 * 3600000,
      text: "⚠ Context filling up — asked the agent to save unsaved work",
    },
    {
      role: "assistant",
      timestamp: now - 6 * 3600000 + 15000,
      content: [{ type: "text", text: "Antes de seguir dejo por escrito lo decidido, que si se resume la conversación se pierde." }],
    },
    tool("c-write", "write", { path: "tmp/ESTADO-refactor.md" }, "done", "Wrote 3120 bytes"),
    {
      _type: "compaction_marker",
      _msg_id: "c-compact",
      timestamp: Math.floor((now - 6 * 3600000 + 60000) / 1000),
      summary:
        "El usuario pidió terminar el refactor de handlers. Se separaron los handlers de sesión " +
        "de los de configuración, quedando pendiente el bus. Decisión: no tocar el contrato HTTP " +
        "existente; los clientes instalados dependen de él.",
      tokensBefore: 154000,
      readFiles: ["pkg/serve/session_handlers.go", "pkg/serve/manager.go", "pkg/bus/handlers.go"],
      modifiedFiles: ["pkg/serve/session_handlers.go", "tmp/ESTADO-refactor.md"],
    },
    // Work carries on afterwards: the transcript reads continuously across the
    // compaction instead of starting from nothing.
    {
      role: "assistant",
      timestamp: now - 5 * 3600000,
      content: [{ type: "text", text: "Retomo con el estado guardado: queda el bus, y el contrato HTTP no se toca." }],
    },
    tool("c-grep", "grep", { pattern: "RegisterHandlers", path: "pkg/bus" }, "done", "4 matches"),
  ],
  subagents: {},
};

// A response the provider served with a model other than the one requested.
// The badge is durable message provenance (requested_model vs model), so it
// survives a reload — this is the treatment to judge before reusing it for the
// compaction summarizer.
const redirected = {
  id: "redirected",
  title: "model redirect",
  state: "idle",
  model: "Claude Fable 5.1",
  provider: "anthropic",
  thinking: "high",
  cwd: "/home/ealeixandre/dev/moa/main",
  updated: now - 2 * 3600000,
  contextPercent: 18,
  contextWindow: 1000000,
  costUSD: 1.24,
  messages: [
    user("r-u1", "Diseña la migración del bus de eventos.", 2 * 3600000),
    // Real shape: a Fable request answered with Opus.
    {
      role: "assistant",
      timestamp: now - 2 * 3600000 + 30000,
      requested_model: "claude-fable-5-1",
      model: "claude-opus-4-8",
      content: [{ type: "text", text: "El bus actual acopla la publicación con la persistencia, así que la migración tiene que separar ambas antes de tocar los suscriptores." }],
    },
    tool("r-read", "read", { path: "pkg/bus/bridge.go" }, "done", "1240 lines"),
    // Taken from a real session: grok-4.6 served as grok-4.6-build.
    {
      role: "assistant",
      timestamp: now - 2 * 3600000 + 90000,
      requested_model: "grok-4.6",
      model: "grok-4.6-build",
      content: [{ type: "text", text: "Confirmado: publish() y persist() comparten el mismo lock, y ese es el nudo de la migración." }],
    },
    // An ordinary answer, for contrast: no badge when nothing was redirected.
    {
      role: "assistant",
      timestamp: now - 2 * 3600000 + 120000,
      requested_model: "claude-fable-5-1",
      model: "claude-fable-5-1",
      content: [{ type: "text", text: "Sin redirección, esta respuesta no lleva distintivo." }],
    },
  ],
  subagents: {},
};

// The compaction-model notice. The ordinary case says nothing: the compaction
// card alone is the whole story, whether the summary was written by the
// session's model or by the configured one. The line appears ONLY when the
// configured summarizer could not be used, because that is the case the
// transcript cannot otherwise explain.
const compactNotice = {
  id: "compact-notice",
  title: "summarizer fallback",
  state: "idle",
  model: "Claude Fable 5.1",
  provider: "anthropic",
  thinking: "medium",
  cwd: "/home/ealeixandre/dev/moa/main",
  updated: now - 60000,
  contextPercent: 21,
  contextWindow: 1000000,
  costUSD: 2.1,
  messages: [
    user("cn-u1", "Sigue con el refactor.", 600000),
    {
      role: "assistant",
      timestamp: now - 590000,
      content: [{ type: "text", text: "Sigo por donde lo dejamos." }],
    },

    // The ordinary compaction: card only, no line.
    {
      _type: "compaction_marker",
      _msg_id: "cn-c1",
      timestamp: Math.floor((now - 500000) / 1000),
      summary: "Resumen de la conversación previa.",
      tokensBefore: 262000,
      readFiles: ["pkg/bus/bridge.go"],
      modifiedFiles: [],
    },
    {
      role: "assistant",
      timestamp: now - 490000,
      content: [{ type: "text", text: "Compactación normal: arriba solo está la tarjeta, sin aviso." }],
    },

    // The fallback: the configured summarizer could not be reached.
    {
      _type: "compaction_marker",
      _msg_id: "cn-c2",
      timestamp: Math.floor((now - 300000) / 1000),
      summary: "Resumen de la conversación previa.",
      tokensBefore: 271000,
      readFiles: [],
      modifiedFiles: [],
    },
    { _type: "system", _msg_id: "cn-fb", timestamp: now - 300000, text: "\u2702 Summarized with Fable — no usable credential for Terra" },
    {
      role: "assistant",
      timestamp: now - 290000,
      content: [{ type: "text", text: "Aquí sí: el modelo configurado no se pudo usar, y queda dicho." }],
    },

    // For tone comparison: the notice already in production.
    {
      _type: "system",
      _msg_id: "cn-e",
      timestamp: now - 100000,
      text: "\u26a0 Context filling up — asked the agent to save unsaved work",
    },
  ],
  subagents: {},
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
  "skill-fork": skillFork,
  compaction,
  redirected,
  "compact-notice": compactNotice,
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
