import { expect, mock, test } from "bun:test";

// Rendered as plain vnode trees (no DOM): the hooks are stubbed and the row
// components are invoked by hand to reach their markup.
const realHooks = await import("preact/hooks");
mock.module("preact/hooks", () => ({
  ...realHooks,
  useState(initial) { return [typeof initial === "function" ? initial() : initial, () => {}]; },
  useEffect() {},
  useRef(initial) { return { current: initial }; },
  useCallback(callback) { return callback; },
  useMemo(factory) { return factory(); },
  useLayoutEffect() {},
}));

// The card must reach the SAME action the subagent view uses. Spread the real
// module: bun never restores a mocked module, and other files need the rest.
const promoted = [];
const realActions = await import("../../data/session-actions.js");
mock.module("../../data/session-actions.js", () => ({
  ...realActions,
  promoteSubagent(id, jobId) { promoted.push([id, jobId]); return Promise.resolve(); },
}));

const { docChildren } = await import("../../layout/Stream/ConversationStream.jsx");

function expand(node, depth = 0) {
  if (node == null || typeof node !== "object" || depth > 8) return node;
  if (Array.isArray(node)) return node.map((child) => expand(child, depth));
  if (typeof node.type === "function") return expand(node.type(node.props), depth + 1);
  return { ...node, props: { ...node.props, children: expand(node.props?.children, depth) } };
}

function descendants(node, nodes = []) {
  if (node == null || typeof node !== "object") return nodes;
  if (Array.isArray(node)) { for (const child of node) descendants(child, nodes); return nodes; }
  nodes.push(node);
  descendants(node.props?.children, nodes);
  return nodes;
}

function text(node) {
  if (node == null || typeof node === "boolean") return "";
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (Array.isArray(node)) return node.map(text).join("");
  return text(node.props?.children);
}

function promoteButtons(agents, sessionId = "s1") {
  const block = { type: "delegation", id: "d1", agents, settled: agents.every((a) => a.state !== "running") };
  const tree = expand(docChildren([block], () => {}, 1, sessionId, () => {}));
  return descendants(tree).filter((n) => n.type === "button" && text(n) === "to background");
}

const running = (id, promotable) => ({ id, name: `agent ${id}`, accent: "sky", state: "running", openable: true, promotable, bashJobs: [] });

test("a running sync subagent card offers to background and calls the existing promote action", () => {
  promoted.length = 0;
  const [button] = promoteButtons([running("j1", true)]);
  expect(button).toBeTruthy();
  let stopped = false;
  button.props.onClick({ stopPropagation() { stopped = true; } });
  expect(stopped).toBe(true);
  expect(promoted).toEqual([["s1", "j1"]]);
});

test("parallel sync subagents each carry their own button", () => {
  promoted.length = 0;
  const buttons = promoteButtons([running("j1", true), running("j2", true)]);
  expect(buttons).toHaveLength(2);
  buttons[1].props.onClick({ stopPropagation() {} });
  expect(promoted).toEqual([["s1", "j2"]]);
});

test("no button for a non-promotable (async or cancelling) or finished subagent", () => {
  expect(promoteButtons([running("j1", false)])).toHaveLength(0);
  const done = { id: "j2", name: "agent j2", accent: "sky", state: "done", result: "ok", openable: true, promotable: true, bashJobs: [] };
  const failed = { ...done, id: "j3", state: "failed", error: "boom" };
  expect(promoteButtons([done, failed])).toHaveLength(0);
});
