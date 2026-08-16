import { test, expect, mock } from "bun:test";

// The rows are rendered as plain vnode trees (no DOM), so the component's own
// hooks are stubbed: `expanded` is driven by the value this stub returns, which
// is how a test can assert both the collapsed and the opened live row.
let stateValue = null;
mock.module("preact/hooks", () => ({
  useState(initial) {
    const value = stateValue == null ? (typeof initial === "function" ? initial() : initial) : stateValue;
    return [value, () => {}];
  },
  useEffect() {},
  useRef(initial) { return { current: initial }; },
  useCallback(callback) { return callback; },
  useMemo(factory) { return factory(); },
}));

const { ActivityLedger, fullLabel } = await import("./ActivityLedger.jsx");

function descendants(node, nodes = []) {
  if (node == null || typeof node !== "object") return nodes;
  if (Array.isArray(node)) {
    for (const child of node) descendants(child, nodes);
    return nodes;
  }
  nodes.push(node);
  descendants(node.props?.children, nodes);
  return nodes;
}

function textContent(node) {
  if (node == null) return "";
  if (typeof node === "string") return node;
  if (Array.isArray(node)) return node.map(textContent).join("");
  if (typeof node !== "object") return String(node);
  return textContent(node.props?.children);
}

// The card renders row COMPONENTS, so a static vnode walk has to invoke them
// to reach the row markup the assertions are actually about.
function expandComponents(node, depth = 0) {
  if (node == null || typeof node !== "object" || depth > 8) return node;
  if (Array.isArray(node)) return node.map((child) => expandComponents(child, depth));
  if (typeof node.type === "function") return expandComponents(node.type(node.props), depth + 1);
  return { ...node, props: { ...node.props, children: expandComponents(node.props?.children, depth) } };
}

function render(rows) {
  return descendants(expandComponents(ActivityLedger({ rows })));
}

function rowByClass(nodes, className) {
  return nodes.find((node) => typeof node.props?.class === "string" && node.props.class.includes(className));
}

function labelCell(nodes) {
  return nodes.find((node) => node.props?.class === "txt");
}

const LONG = `find / -name '*.go' -type f | grep -v vendor | ${"x".repeat(120)} > out.txt`;

test("a bash row longer than the short label exposes the whole command as a tooltip", () => {
  stateValue = null;
  const short = `${LONG.slice(0, 100)}…`;
  const nodes = render([
    { tool: "bash", arg: { text: short }, out: "ok", status: "ok", id: "b1", command: LONG },
  ]);

  const label = labelCell(nodes);
  expect(textContent(label)).toContain(short);
  expect(label.props.title).toBe(LONG);
  expect(label.props.title.length).toBeGreaterThan(100);
});

test("a live bash row exposes the whole command even though its label is shortened", () => {
  stateValue = null;
  const nodes = render([
    { tool: "bash", arg: { text: `${LONG.slice(0, 100)}…` }, status: "ok", id: "b1", live: true, command: LONG },
  ]);

  expect(labelCell(nodes).props.title).toBe(LONG);
});

test("a live bash row with NO output at all can still be opened to read the command", () => {
  // The worst case: a command that writes to a file streams nothing, so there
  // is no live window to look at — the row itself must open.
  const row = { tool: "bash", arg: { text: "find / -na…" }, status: "ok", id: "b1", live: true, command: LONG };

  stateValue = false;
  const collapsed = render([row]);
  const collapsedRow = rowByClass(collapsed, "tg-row live");
  expect(collapsedRow.type).toBe("button");
  expect(collapsedRow.props["aria-expanded"]).toBe(false);
  expect(collapsed.some((node) => node.props?.class === "tg-detail")).toBe(false);

  stateValue = true;
  const opened = render([row]);
  expect(rowByClass(opened, "tg-row live").props["aria-expanded"]).toBe(true);
  const detail = opened.find((node) => node.props?.class === "tg-detail");
  expect(detail).toBeDefined();
  expect(textContent(detail)).toContain(LONG);
});

test("a live row without a command stays the inert div it is today", () => {
  stateValue = null;
  const nodes = render([
    { tool: "read", arg: { text: "pkg/serve/frontend/src/app.jsx" }, status: "ok", id: "r1", live: true },
  ]);

  const liveRow = rowByClass(nodes, "tg-row live");
  expect(liveRow.type).toBe("div");
  expect(liveRow.props["aria-expanded"]).toBeUndefined();
  expect(liveRow.props.role).toBe("status");
});

test("a CSS-clipped label (path, pattern, url) keeps its full value in the tooltip", () => {
  stateValue = null;
  const path = `/home/user/dev/moa/${"deep/".repeat(30)}file.jsx`;
  const nodes = render([
    { tool: "read", arg: { text: path }, out: "232 lines", status: "ok", id: "r1" },
  ]);

  expect(labelCell(nodes).props.title).toBe(path);
});

test("fullLabel prefers the untruncated command and omits an empty tooltip", () => {
  expect(fullLabel({ command: "long command" }, "long comm…")).toBe("long command");
  expect(fullLabel({}, "pattern")).toBe("pattern");
  expect(fullLabel({}, "")).toBeUndefined();
});
