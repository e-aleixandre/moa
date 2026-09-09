import { test, expect, mock } from "bun:test";

// The view is walked as a plain vnode tree (no DOM), so its hooks are stubbed.
// The point of this file is the double-tap guard, and the stub is what makes it
// observable: `useState` never re-renders here, so `creating` stays false
// between the two taps -- which is precisely the real-device race being
// guarded against, where two touches land inside one render cycle.
const realHooks = await import("preact/hooks");
const refs = [];
// `caps` is filled by an effect that never runs under the stub, so the
// workspace root -- what the Create button needs as its target -- is injected
// through the useState call order instead.
let pick = () => undefined;
let calls = 0;
mock.module("preact/hooks", () => ({
  ...realHooks,
  useState(initial) {
    const chosen = pick(calls++, initial);
    if (chosen !== undefined) return [chosen, mock(() => {})];
    return [typeof initial === "function" ? initial() : initial, mock(() => {})];
  },
  useEffect() {},
  useLayoutEffect() {},
  useRef(initial) {
    const ref = { current: initial };
    refs.push(ref);
    return ref;
  },
  useCallback(callback) { return callback; },
  useMemo(factory) { return factory(); },
}));

const { NewSessionView } = await import("./NewSessionView.jsx");

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

function expand(node, depth = 0) {
  if (node == null || typeof node !== "object" || depth > 12) return node;
  if (Array.isArray(node)) return node.map((child) => expand(child, depth + 1));
  if (typeof node.type === "function") {
    const rendered = node.type(node.props ?? {});
    return expand(rendered, depth + 1);
  }
  if (node.props?.children != null) {
    return { ...node, props: { ...node.props, children: expand(node.props.children, depth + 1) } };
  }
  return node;
}

test("a second tap on Create cannot start a second session", () => {
  refs.length = 0;
  calls = 0;
  // index 1 is `caps` (see the useState order in NewSessionView)
  pick = (i) => (i === 1 ? { workspaceRoot: "/home/x/dev/moa/main" } : undefined);
  const onCreate = mock(() => {});
  const tree = expand(
    NewSessionView({
      projects: [{ path: "/home/x/dev/moa/main", label: "moa/main" }],
      onBack: () => {},
      onCreate,
    }),
  );

  const create = descendants(tree).find(
    (n) => typeof n.props?.children === "string" && n.props.children.startsWith("Create in"),
  );
  expect(create).toBeTruthy();

  // Two taps in the same cycle: `creating` is still false on the second one,
  // so only the synchronous ref can stop it.
  create.props.onClick?.();
  create.props.onClick?.();

  expect(onCreate).toHaveBeenCalledTimes(1);
});
