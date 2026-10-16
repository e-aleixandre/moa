import { test, expect, afterEach, beforeEach } from "bun:test";
import {
  incomingRequest, heldRequests, wantsToTalkIds, incomingTitle, heldTitle, callBlockModel, requestIdFromSearch,
} from "./call-requests-model.js";
import { ownerRows, ownerLine } from "./owners-model.js";
import { projectStream } from "./stream-model.js";

const ring = { id: "r1", session_id: "s1", owner_id: "o1", owner_name: "Infra", reason: "Deploy is blocked", level: "ring", state: "ringing" };
const ask = { ...ring, id: "r2", owner_id: "o2", owner_name: "Web", level: "ask_first" };
const held = { ...ring, id: "r3", owner_id: "o3", owner_name: "Docs", state: "held" };

test("the incoming call is the oldest ringing one; held and accepted ones do not ring", () => {
  expect(incomingRequest([held, ask, ring])).toBe(ask);
  expect(incomingRequest([held, { ...ring, state: "accepted" }])).toBeNull();
  expect(heldRequests([held, ring])).toEqual([held]);
});

test("Ring says is calling, Ask first says wants to talk", () => {
  expect(incomingTitle(ring)).toBe("Infra is calling");
  expect(incomingTitle(ask)).toBe("Web wants to talk");
  expect(heldTitle(held)).toBe("Docs wanted to talk");
});

test("the owner row says wants to talk in amber while a request lives", () => {
  const owners = [{ id: "o1", name: "Infra", session_id: "s1" }, { id: "o9", name: "Other" }];
  const rows = ownerRows(owners, {}, Date.now(), wantsToTalkIds([ring, held]));
  expect(rows[0].wantsToTalk).toBe(true);
  expect(rows[1].wantsToTalk).toBe(false);
  expect(ownerLine(rows[0]).lead).toEqual({ tone: "yellow", text: "wants to talk" });
  expect(ownerLine(rows[1]).lead).toBeNull();
  expect(wantsToTalkIds([{ ...ring, state: "accepted" }]).size).toBe(0);
});

test("the deep link carries the request id", () => {
  expect(requestIdFromSearch("?call_request=abc&x=1")).toBe("abc");
  expect(requestIdFromSearch("")).toBe("");
});

test("request_call result: answered, or missed with a readable why and the reason", () => {
  expect(callBlockModel({ call_request: { outcome: "answered", reason: "Deploy" } })).toEqual({ answered: true, title: "Call · answered", reason: "Deploy" });
  expect(callBlockModel({ call_request: { outcome: "written", why: "missed", reason: "Deploy" } })).toEqual({ answered: false, title: "Missed call · no answer", reason: "Deploy" });
  expect(callBlockModel({ call_request: { outcome: "written", why: "outside_hours", reason: "x" } }).title).toBe("Missed call · outside your allowed hours");
  expect(callBlockModel({})).toBeNull();
});

// ── Actions ───────────────────────────────────────────────────────────────

const realFetch = globalThis.fetch;
const realLocation = Object.getOwnPropertyDescriptor(globalThis, "location");
let posts;
beforeEach(() => {
  posts = [];
  globalThis.location = { protocol: "https:", host: "moa.test" };
});
afterEach(async () => {
  globalThis.fetch = realFetch;
  const { __setVoiceCallDepsForTests } = await import("./voice-call-session.js");
  __setVoiceCallDepsForTests(null);
  if (realLocation) Object.defineProperty(globalThis, "location", realLocation); else delete globalThis.location;
});

async function wire() {
  const { __setVoiceCallDepsForTests } = await import("./voice-call-session.js");
  class WS {
    constructor() { this.readyState = 0; setTimeout(() => { this.readyState = 1; this.onopen?.(); }, 0); }
    send() {} close() { this.readyState = 3; this.onclose?.(); }
  }
  const audio = { start: async () => {}, play() {}, interrupt() {}, stop() {}, setMuted() {} };
  const fetchImpl = async (url, init) => {
    posts.push({ url, method: init?.method, body: init?.body ? JSON.parse(init.body) : null });
    if (url.endsWith("/hangup") || url.endsWith("/write") || url.endsWith("/accept")) return new Response(null, { status: 204 });
    return new Response(JSON.stringify({ call_id: "c1", provider: "chatgpt-plan", billing: { kind: "plan_unverified" }, ws_url: "/api/voice/calls/c1/ws" }), { status: 201 });
  };
  globalThis.fetch = fetchImpl;
  __setVoiceCallDepsForTests({ supported: true, WebSocket: WS, createAudio: () => audio, fetch: fetchImpl });
}

test("Answer opens the call with the request id and no provider", async () => {
  await wire();
  const { answerCallRequest } = await import("./call-requests.js");
  expect(answerCallRequest(ring)).toBe(true);
  await new Promise((r) => setTimeout(r, 5));
  expect(posts[0]).toEqual({ url: "/api/voice/calls", method: "POST", body: { session_id: "s1", call_request_id: "r1" } });
});

test("Write it instead posts write for that request", async () => {
  await wire();
  const { writeCallRequest } = await import("./call-requests.js");
  await writeCallRequest("r1");
  expect(posts).toEqual([{ url: "/api/voice/requests/r1/write", method: "POST", body: null }]);
});

test("Call on a held request accepts it, hangs up the current call, then calls with its id", async () => {
  await wire();
  const { startVoiceCall } = await import("./voice-call-session.js");
  const { callHeldRequest } = await import("./call-requests.js");
  expect(startVoiceCall("other")).toBe(true);
  await new Promise((r) => setTimeout(r, 5));
  posts.length = 0;
  expect(await callHeldRequest(held)).toBe(true);
  await new Promise((r) => setTimeout(r, 5));
  expect(posts.map((p) => `${p.method} ${p.url}`)).toEqual([
    "POST /api/voice/requests/r3/accept",
    "POST /api/voice/calls/c1/hangup",
    "POST /api/voice/calls",
  ]);
  expect(posts[2].body).toEqual({ session_id: "s1", call_request_id: "r3" });
});

test("a held request that is gone (409) starts nothing and leaves the call alone", async () => {
  await wire();
  globalThis.fetch = async (url, init) => { posts.push({ url }); return new Response("{}", { status: 409 }); };
  const { callHeldRequest } = await import("./call-requests.js");
  expect(await callHeldRequest(held)).toBe(false);
  expect(posts.map((p) => p.url)).toEqual(["/api/voice/requests/r3/accept"]);
});

test("a request whose plan fails says its question waits in writing and offers no payment", async () => {
  const { VoiceCallController } = await import("./voice-call.js");
  const { callStatus, paidAction, retryAction } = await import("./voice-call-view.js");
  const fetchImpl = async () => new Response(JSON.stringify({ error: "plan_unavailable", reason: "unavailable" }), { status: 503 });
  const c = new VoiceCallController({ sessionId: "s1", fetch: fetchImpl, createAudio: () => ({ prepare() {}, stop() {} }) });
  await c.start({ callRequestId: "r1" });
  const s = c.state();
  expect(s.phase).toBe("failed");
  expect(callStatus(s, "Infra")).toBe("The call could not connect. Infra's question is waiting in writing.");
  expect(paidAction(s)).toBeNull();
  expect(retryAction(s)).toBeNull();
});

test("a request_call history result becomes one compact ledger row", () => {
  const msgs = [
    { role: "user", content: [{ type: "text", text: "go" }], timestamp: 1 },
    { _type: "tool_start", tool_call_id: "t1", tool_name: "request_call", args: { reason: "Deploy is blocked" }, status: "done", result: "No call", callRequest: { id: "r1", outcome: "written", why: "missed", reason: "Deploy is blocked" } },
  ];
  const blocks = projectStream({ messages: msgs });
  const rows = JSON.stringify(blocks);
  expect(rows).toContain("Missed call · no answer");
  expect(rows).toContain("Deploy is blocked");
  const row = blocks.flatMap((b) => b.blocks || []).flatMap((b) => b.rows || []).find((r) => String(r.toolLabel).startsWith("Missed call"));
  expect(row.status).toBe("warn");
  const answered = projectStream({ messages: [{ ...msgs[1], callRequest: { id: "r2", outcome: "answered" } }] });
  expect(answered.flatMap((b) => b.blocks || []).flatMap((b) => b.rows || [])[0].status).toBe("ok");
});

test("the sidebar roster repaints when a request to talk arrives or ends", async () => {
  const { store } = await import("./store.js");
  const { selectDesktopChrome, __resetDesktopChromeForTests } = await import("../layout/Sidebar/sessions.js");
  const base = store.get();
  const withRequests = (list) => ({ ...base, owners: { ...base.owners, list: [{ id: "o1", name: "Infra", session_id: "s1" }] }, callRequests: { list, loaded: true } });
  __resetDesktopChromeForTests();
  const quiet = selectDesktopChrome(withRequests([]));
  const ringing = selectDesktopChrome(withRequests([ring]));
  expect(ringing).not.toBe(quiet);
  expect(ringing.owners[0].wantsToTalk).toBe(true);
  expect(selectDesktopChrome(withRequests([])).owners[0].wantsToTalk).toBe(false);
  __resetDesktopChromeForTests();
});

test("the call's own name says whose question waits when the surface is not told", async () => {
  const { callStatus } = await import("./voice-call-view.js");
  const failed = { phase: "failed", error: "plan_unavailable", callRequestId: "r1", name: "Infra" };
  expect(callStatus(failed)).toBe("The call could not connect. Infra's question is waiting in writing.");
});
