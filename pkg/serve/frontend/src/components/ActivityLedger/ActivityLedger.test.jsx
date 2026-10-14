import { test, expect, mock } from "bun:test";

// The rows are rendered as plain vnode trees (no DOM), so the component's own
// hooks are stubbed: `expanded` is driven by the value this stub returns, which
// is how a test can assert both the collapsed and the opened live row.
let stateValue = null;
// Spread the real hooks first. bun's mock.module replaces the module for the
// whole process and never restores it, so a factory listing only some hooks
// deletes the rest for every file loaded afterwards -- the
// "Export named 'useMemo' not found" that only appears when these files run
// together.
const realHooks = await import("preact/hooks");
mock.module("preact/hooks", () => ({
  ...realHooks,
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
  return nodes.find((node) => node.props?.class === "zl-lg-txt");
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
  const collapsedRow = rowByClass(collapsed, "zl-lg-row is-live");
  expect(collapsedRow.type).toBe("button");
  expect(collapsedRow.props["aria-expanded"]).toBe(false);
  expect(rowByClass(collapsed, "zl-lg-detail")).toBeUndefined();

  stateValue = true;
  const opened = render([row]);
  expect(rowByClass(opened, "zl-lg-row is-live").props["aria-expanded"]).toBe(true);
  const detail = rowByClass(opened, "zl-lg-detail");
  expect(detail).toBeDefined();
  expect(textContent(detail)).toContain(LONG);
});

test("a live row without a command stays the inert div it is today", () => {
  stateValue = null;
  const nodes = render([
    { tool: "read", arg: { text: "pkg/serve/frontend/src/app.jsx" }, status: "ok", id: "r1", live: true },
  ]);

  const liveRow = rowByClass(nodes, "zl-lg-row is-live");
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

const DIFF_A = "@@ -1,2 +1,4 @@\n   1  keep\n   2 -old1\n   2 +new1\n   3 +new2\n   4 +new3";
const PATCH_ROW = {
  tool: "apply_patch", arg: { text: "3 files" }, out: "+5 −1", status: "ok", id: "p1",
  patchFiles: [
    { op: "M", path: "src/a.go", add: 3, del: 1, diff: DIFF_A },
    { op: "A", path: "src/new.go", add: 2, del: 0, diff: "@@ -0,0 +1,2 @@\n   1 +package x\n   2 +" },
    { op: "D", path: "src/old.go", add: 0, del: 0 },
  ],
};

test("a closed apply_patch row lists each file with its letter and counts, with no diff mounted", () => {
  stateValue = null;
  const nodes = render([PATCH_ROW]);
  const files = nodes.filter((n) => typeof n.props?.class === "string" && n.props.class.split(" ").includes("zl-pf-file"));
  expect(files.map((f) => textContent(f).replace(/false/g, ""))).toEqual(["Msrc/a.go+3−1", "Asrc/new.go+2", "Dsrc/old.godeleted"]);
  expect(nodes.some((n) => typeof n.props?.class === "string" && n.props.class.includes("diff"))).toBe(false);
});

test("an open apply_patch row shows the recorded diff of every file that has one", () => {
  stateValue = true;
  const nodes = render([PATCH_ROW]);
  const diffs = nodes.filter((n) => typeof n.props?.class === "string" && n.props.class.split(" ").includes("diff"));
  expect(diffs).toHaveLength(2);
  expect(textContent(diffs[0])).toContain("old1");
  expect(textContent(diffs[1])).toContain("package x");
  stateValue = null;
});

test("an old result without recorded diffs stays a plain text row", () => {
  stateValue = null;
  const nodes = render([{ tool: "apply_patch", arg: { text: "x" }, status: "ok", id: "p2", body: "M a.go\nA b.go" }]);
  expect(nodes.some((n) => typeof n.props?.class === "string" && n.props.class.includes("zl-pf"))).toBe(false);
});

test("files listed without their diffs (a persisted child) are openable and mount no rows closed", () => {
  stateValue = null;
  const row = { ...PATCH_ROW, patchDetailUrl: "/x", patchFiles: [{ op: "M", path: "a.go", add: 1, del: 1, has_diff: true }, { op: "D", path: "b.go", add: 0, del: 2, has_diff: true }] };
  const nodes = render([row]);
  expect(nodes.find((n) => n.type === "button" && n.props?.class?.includes("zl-lg-row")).props["aria-expanded"]).toBe(false);
  const files = nodes.filter((n) => typeof n.props?.class === "string" && n.props.class.split(" ").includes("zl-pf-file"));
  expect(files.every((f) => f.type === "button")).toBe(true);
  expect(nodes.some((n) => typeof n.props?.class === "string" && n.props.class.includes("diff"))).toBe(false);
});

test("a patch listed past its cap says how many more files there are", () => {
  stateValue = null;
  const nodes = render([{ ...PATCH_ROW, patchMore: { files: 1, add: 1, del: 2 } }]);
  expect(textContent(nodes.find((n) => n.props?.class?.includes?.("zl-pf-cut")))).toBe("+1 more files");
});
