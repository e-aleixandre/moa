// catalog-backend.js — the design lab's server.
//
// `npm run catalog` serves the real screens without the Go process. Those
// screens still fetch /api/* and open a session socket. This module is that
// process: fixtures for the reads the chrome needs, empty success for writes,
// and one init frame so the client does not mark the transcript stale.
//
// Specimens (store-shaped sessions) live in specimen.js. This file is only
// the wire: HTTP paths and the init payload handleWsInit already knows.

import { CATALOG_SESSIONS } from "./specimen.js";

export const CATALOG_MODELS = [
  { id: "claude-opus-4-8", name: "Claude Opus 4.8", provider: "anthropic", alias: "opus", max_input: 1000000 },
  { id: "claude-sonnet-4-6", name: "Claude Sonnet 4.6", provider: "anthropic", alias: "sonnet", max_input: 200000 },
  { id: "claude-haiku-4-5", name: "Claude Haiku 4.5", provider: "anthropic", alias: "luna", max_input: 200000 },
  { id: "gpt-5-sol", name: "GPT Sol", provider: "openai", alias: "sol", max_input: 400000 },
  { id: "gpt-5-terra", name: "GPT Terra", provider: "openai", alias: "terra", max_input: 400000 },
];

export const CATALOG_CAPS = {
  homeDir: "/home/ealeixandre",
  workspaceRoot: "/home/ealeixandre/dev/moa",
  defaultModel: "anthropic/claude-opus-4-8",
  goal_flags: [
    { name: "--max", placeholder: "N", desc: "max iterations (0 = unlimited)" },
    { name: "--stalled", placeholder: "N", desc: "stop after N iterations with no progress" },
    { name: "--timeout", placeholder: "2h", desc: "wall-clock deadline" },
    { name: "--budget", placeholder: "USD", desc: "cumulative USD ceiling" },
    { name: "--verifier", placeholder: "SPEC", desc: "model spec for the verifier" },
    { name: "--verify-timeout", placeholder: "5m", desc: "total verifier run timeout" },
    { name: "--verify-oneshot", desc: "use the tool-less one-shot verifier" },
    { name: "--compact", placeholder: "N", desc: "soft compaction threshold in tokens" },
    { name: "--cwd", placeholder: "DIR", desc: "execution and evaluation directory" },
  ],
};

const CATALOG_SKILLS = [
  { name: "review", description: "Review the current diff" },
  { name: "release-check", description: "Prepare a release decision" },
  { name: "visual-qa", description: "Exercise the visible workflow" },
  { name: "incident-review", description: "Learn from a material incident" },
  { name: "feedback", description: "Send project feedback" },
];

export const CATALOG_USAGE = {
  available: true,
  five_hour: { utilization: 42, resets_at: new Date(Date.now() + 3 * 3600000).toISOString() },
  seven_day: { utilization: 61, resets_at: new Date(Date.now() + 4 * 86400000).toISOString() },
};

const FS_ENTRIES = {
  "/home/ealeixandre": ["dev", "src"],
  "/home/ealeixandre/dev": ["moa", "pulse"],
  "/home/ealeixandre/dev/moa": ["main", "desktop-design", "fast-mode", "pulse-api"],
};

let created = 0;
let installed = false;

function pathnameOf(path) {
  try {
    return new URL(path, "http://lab.local").pathname;
  } catch {
    return String(path).split("?")[0];
  }
}

function queryOf(path) {
  try {
    return new URL(path, "http://lab.local").searchParams;
  } catch {
    return new URLSearchParams();
  }
}

function modelName(spec) {
  const found = CATALOG_MODELS.find((m) => `${m.provider}/${m.id}` === spec);
  return found ? found.name : spec;
}

function rosterOf(sessions) {
  return Object.values(sessions || {}).map((s) => ({
    id: s.id,
    title: s.title,
    state: s.state,
    cwd: s.cwd,
    model: s.model,
    provider: s.provider,
    thinking: s.thinking,
    permission_mode: s.permissionMode,
    context_percent: s.contextPercent,
    context_window: s.contextWindow,
    cost_usd: s.costUSD,
    updated: typeof s.updated === "number" ? new Date(s.updated).toISOString() : s.updated,
    unseen: !!s.unseen,
    error: s.error || undefined,
    brief_progress: s.briefProgress,
    brief_attempting: s.briefAttempting,
    fast: !!s.fast,
    fast_supported: !!s.fastSupported,
    fast_note: s.fastNote || "",
  }));
}

function subagentPayload(sessions, sessionId, jobId) {
  const sub = sessions?.[sessionId]?.subagents?.[jobId];
  if (!sub) return null;
  return {
    task: sub.task || "",
    model: sub.model || "",
    status: sub.status || "completed",
    async: !!sub.async,
    messages: sub.messages || [],
    input_tokens: sub.usage?.inputTokens || 0,
    output_tokens: sub.usage?.outputTokens || 0,
    cost_usd: sub.usage?.costUSD || 0,
  };
}

// catalogResponse is the pure dispatcher. Unknown /api reads return {} so a
// new chrome call does not 404 the lab; unknown writes succeed empty. Tests
// pass a session map; the running lab passes the store.
export function catalogResponse(method, path, body = null, sessions = CATALOG_SESSIONS) {
  const p = pathnameOf(path);
  const m = method.toUpperCase();

  if (m === "GET" && p === "/api/models") return CATALOG_MODELS;
  if (m === "GET" && p === "/api/capabilities") return CATALOG_CAPS;
  if (m === "GET" && p === "/api/usage") return CATALOG_USAGE;
  if (m === "GET" && p === "/api/model-preferences") return { pinned_models: ["claude-opus-4-8", "gpt-5-sol"] };
  if (m === "PATCH" && p === "/api/model-preferences") return { pinned_models: body?.pinned_models || [] };
  if (m === "GET" && p === "/api/compact-at") return { compact_at: 0, compact_at_min: 0 };
  if (m === "PATCH" && p === "/api/compact-at") return { compact_at: body?.compact_at || 0, compact_at_min: 0 };
  if (m === "GET" && p === "/api/compact-strategy") return { compact_strategy: "notify" };
  if (m === "PATCH" && p === "/api/compact-strategy") return { compact_strategy: body?.compact_strategy || "notify" };
  if (m === "GET" && p === "/api/subagent-models") return { allowed_models: [] };
  if (m === "GET" && p === "/api/sessions") return rosterOf(sessions);
  if (m === "GET" && p === "/api/fs/complete") {
    const dir = (queryOf(path).get("path") || "").replace(/\/+$/, "") || "/";
    return { entries: FS_ENTRIES[dir] || [] };
  }
  if (m === "POST" && p === "/api/sessions") {
    created += 1;
    const spec = body?.model || CATALOG_CAPS.defaultModel;
    return {
      id: `lab-${created}`,
      title: "New session",
      state: "idle",
      cwd: body?.cwd || CATALOG_CAPS.workspaceRoot,
      model: modelName(spec),
      provider: spec.split("/")[0] || "anthropic",
      thinking: "medium",
      permission_mode: "yolo",
      context_percent: 0,
    };
  }

  const sessionMatch = p.match(/^\/api\/sessions\/([^/]+)(?:\/(.*))?$/);
  if (sessionMatch) {
    const [, sessionId, rest = ""] = sessionMatch;
    if (m === "GET" && rest === "skills") {
      return { skills: CATALOG_SKILLS };
    }
    if (m === "GET" && rest === "mcp") return { servers: [] };
    if (m === "GET" && rest.startsWith("subagents/")) {
      return subagentPayload(sessions, sessionId, rest.slice("subagents/".length));
    }
    if (m === "PATCH" && rest === "config") {
      const out = {};
      if (body?.model) out.model = modelName(body.model);
      if (body?.thinking) out.thinking = body.thinking;
      if (body?.permission_mode) out.permission_mode = body.permission_mode;
      if (body?.compact_at != null) out.compact_at = body.compact_at;
      return out;
    }
    if (m === "PATCH" && rest === "fast") {
      return {
        fast: !!body?.fast,
        supported: true,
        note: "2.5× faster · billed as separate usage credits",
      };
    }
    if (m === "POST" && rest === "resume") {
      const s = sessions?.[sessionId];
      return s ? { ...rosterOf({ [sessionId]: { ...s, state: "idle" } })[0], state: "idle" } : { id: sessionId, state: "idle" };
    }
    if (m === "POST" || m === "PATCH" || m === "DELETE") return {};
  }

  if (p.startsWith("/api/") && (m === "POST" || m === "PATCH" || m === "DELETE")) return {};
  if (p.startsWith("/api/") && m === "GET") return {};
  return undefined;
}

function toRawMessages(messages) {
  const out = [];
  for (const msg of messages || []) {
    // Durable non-conversational rows travel on the wire the way the Go server
    // emits them, so the lab exercises the real normalizeHistory path instead
    // of a shortcut: the compaction notice rides as a user message carrying
    // custom.source, and the compaction marker as a session_event.
    //
    // The notice's copy is fixed by normalizeHistory, so only a row whose text
    // matches it can travel that way; any other system line would come back
    // rewritten. The rest ride as `goal`, the durable role that keeps its own
    // text and renders as the same system line.
    if (msg._type === "system" && msg._msg_id) {
      const isNotice = (msg.text || "").includes("Context filling up");
      out.push(isNotice ? {
        role: "user",
        msg_id: msg._msg_id,
        timestamp: msg.timestamp,
        custom: { source: "compaction_notice", internal: true },
        content: [{ type: "text", text: msg.text || "" }],
      } : {
        role: "goal",
        msg_id: msg._msg_id,
        timestamp: msg.timestamp,
        content: [{ type: "text", text: msg.text || "" }],
      });
      continue;
    }
    if (msg._type === "compaction_marker") {
      out.push({
        role: "session_event",
        msg_id: msg._msg_id,
        timestamp: msg.timestamp,
        custom: {
          type: "compaction_marker",
          summary: msg.summary || "",
          tokens_before: msg.tokensBefore || 0,
          read_files: msg.readFiles || [],
          modified_files: msg.modifiedFiles || [],
        },
        content: [{ type: "text", text: "compacted" }],
      });
      continue;
    }
    if (msg.role === "user" || (msg.role === "assistant" && !msg._type)) {
      out.push({
        role: msg.role,
        msg_id: msg.msg_id,
        timestamp: msg.timestamp,
        // Model provenance is durable on the wire: it is what tells the
        // transcript a response came back from a model other than the one
        // asked for, so the lab must carry it like the server does.
        requested_model: msg.requested_model,
        model: msg.model,
        content: msg.content,
        // The real server persists custom envelopes (event, subagent_parent…);
        // dropping them here would render a specimen differently from prod.
        ...(msg.custom ? { custom: msg.custom } : {}),
      });
      continue;
    }
    if (msg._type !== "tool_start") continue;
    out.push({
      role: "assistant",
      content: [{
        type: "tool_call",
        tool_call_id: msg.tool_call_id,
        tool_name: msg.tool_name,
        arguments: msg.args || {},
      }],
    });
    if (msg.status && msg.status !== "running" && msg.status !== "generating") {
      out.push({
        role: "tool_result",
        tool_call_id: msg.tool_call_id,
        is_error: msg.status === "error",
        content: [{ type: "text", text: String(msg.result ?? "") }],
      });
    }
  }
  return out;
}

export function sessionInitEvent(session) {
  const live = Object.values(session.subagents || {}).filter(
    (s) => s && (s.status === "running" || s.status === "cancelling"),
  );
  return {
    type: "init",
    seq: 1,
    data: {
      attention_namespace: "lab:1",
      server_instance: "lab",
      last_seq: 1,
      state: session.state,
      messages: toRawMessages(session.messages),
      subagents: live.map((s) => ({
        job_id: s.jobId,
        origin_tool_call_id: s.originToolCallId || "",
        task: s.task || "",
        model: s.model || "",
        status: s.status,
        async: !!s.async,
        messages: toRawMessages(s.messages),
        started_at_ms: s.startedAtMs || null,
        input_tokens: s.usage?.inputTokens || 0,
        output_tokens: s.usage?.outputTokens || 0,
        cost_usd: s.usage?.costUSD || 0,
      })),
      pending_permission: session.pendingPerm || null,
      permission_mode: session.permissionMode || "yolo",
      context_percent: session.contextPercent ?? -1,
      context_window: session.contextWindow || 0,
      cost_usd: session.costUSD || 0,
      fast: !!session.fast,
      fast_supported: !!session.fastSupported,
      fast_note: session.fastNote || "",
      run_tokens_up: session.runTokensUp || 0,
      run_tokens_down: session.runTokensDown || 0,
      run_started_at_ms: session.runStartedAtMs || null,
    },
  };
}

class CatalogSocket {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;

  constructor(url, sessionsOf) {
    this.url = String(url);
    this.readyState = CatalogSocket.CONNECTING;
    this.bufferedAmount = 0;
    this.#sessionsOf = sessionsOf;
    queueMicrotask(() => this.#open());
  }

  #sessionsOf;

  #open() {
    this.readyState = CatalogSocket.OPEN;
    this.onopen?.({ type: "open" });
    const id = this.url.match(/\/sessions\/([^/]+)\/ws/)?.[1];
    if (!id) return;
    const session = this.#sessionsOf()?.[id];
    if (!session) return;
    this.onmessage?.({ data: JSON.stringify(sessionInitEvent(session)) });
  }

  send() {}

  close() {
    this.readyState = CatalogSocket.CLOSED;
    this.onclose?.({ type: "close", code: 1000, wasClean: true });
  }

  addEventListener() {}
  removeEventListener() {}
}

// installCatalogBackend patches fetch and WebSocket for this page. Pass
// getSessions so a roster GET reads the live store (and does not wipe
// transcripts). Call once, from catalog-app, before the screens mount.
export function installCatalogBackend({ getSessions } = {}) {
  if (installed) return;
  installed = true;
  const sessionsOf = () => getSessions?.() || CATALOG_SESSIONS;

  globalThis.WebSocket = function CatalogSocketBound(url) {
    return new CatalogSocket(url, sessionsOf);
  };
  globalThis.WebSocket.CONNECTING = CatalogSocket.CONNECTING;
  globalThis.WebSocket.OPEN = CatalogSocket.OPEN;
  globalThis.WebSocket.CLOSING = CatalogSocket.CLOSING;
  globalThis.WebSocket.CLOSED = CatalogSocket.CLOSED;

  const orig = globalThis.fetch.bind(globalThis);
  globalThis.fetch = async (input, init = {}) => {
    const url = typeof input === "string" ? input : input.url;
    if (!String(url).includes("/api/")) return orig(input, init);
    const method = (init.method || "GET").toUpperCase();
    let parsed = null;
    if (init.body && typeof init.body === "string") {
      try { parsed = JSON.parse(init.body); } catch { parsed = null; }
    }
    const payload = catalogResponse(method, url, parsed, sessionsOf());
    if (payload === undefined) return orig(input, init);
    if (payload === null) return new Response(null, { status: 204 });
    return new Response(JSON.stringify(payload), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  };
}
