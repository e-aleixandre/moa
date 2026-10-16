// VoiceMinutesBlock.test.jsx — the record a call leaves: it names who took the
// call and shows every line of every item.
import { test, expect, mock, afterAll } from "bun:test";

const realHooks = { ...(await import("preact/hooks")) };
const realUseStore = { ...(await import("../../hooks/useStore.js")) };
mock.module("preact/hooks", () => ({
  ...realHooks,
  useState: (initial) => [initial, () => {}],
}));
const storeState = { owners: { list: [{ session_id: "s1", name: "QA owner" }] }, sessions: { s1: { id: "s1", title: "Some session" }, s2: { id: "s2", title: "Other work" } } };
mock.module("../../hooks/useStore.js", () => ({ useStore: (selector) => selector(storeState) }));

// mock.module outlives this file in one bun process: put the real modules back.
afterAll(() => {
  mock.module("preact/hooks", () => realHooks);
  mock.module("../../hooks/useStore.js", () => realUseStore);
});

const { VoiceMinutesBlock, VoiceMinutesForSession } = await import("./VoiceMinutesBlock.jsx");

function expand(node, depth = 0) {
  if (node == null || typeof node !== "object" || depth > 30) return node;
  if (Array.isArray(node)) return node.map((c) => expand(c, depth));
  if (typeof node.type === "function") return expand(node.type(node.props), depth + 1);
  return { ...node, props: { ...node.props, children: expand(node.props?.children, depth + 1) } };
}
function text(node) {
  if (node == null || typeof node === "boolean") return "";
  if (typeof node !== "object") return String(node);
  if (Array.isArray(node)) return node.map(text).join("|");
  return text(node.props?.children);
}

const TEXT = "<voice_minutes>\nVoice call ended (hangup) after 5s with grok-plan, billing plan_unverified.\n\nDecided:\n- (none)\n\n</voice_minutes>\n";
const MINUTES = { decided: ["D1\nD1-more"], passed: [{ delivery_id: "c/p", summary: "P1\nP1-more" }], open: [] };

test("the minutes show every line of the structured items", () => {
  const out = text(expand({ type: VoiceMinutesBlock, props: { text: TEXT, minutes: MINUTES, name: "QA owner" } }));
  expect(out).toContain("D1\nD1-more");
  expect(out).toContain("P1\nP1-more");
});

test("the minutes say who took the call: the owner, else the session's title", () => {
  const owner = text(expand({ type: VoiceMinutesForSession, props: { text: TEXT, minutes: MINUTES, sessionId: "s1" } }));
  expect(owner).toContain("Call with QA owner");
  expect(owner).toContain("Passed to QA owner");
  expect(owner).not.toContain("this session");
  const plain = text(expand({ type: VoiceMinutesForSession, props: { text: TEXT, minutes: MINUTES, sessionId: "s2" } }));
  expect(plain).toContain("Call with Other work");
});
