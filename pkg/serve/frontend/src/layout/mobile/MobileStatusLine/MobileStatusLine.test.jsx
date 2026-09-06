// MobileStatusLine.test.jsx — the thinking meter as the phone actually paints
// it: nothing while the shared model catalog is still unknown, Astra's "low"
// as the zero position once it is ready.
import { test, expect, mock, beforeEach } from "bun:test";

mock.module("preact/hooks", () => ({
  useState(initial) { return [typeof initial === "function" ? initial() : initial, () => {}]; },
  useEffect() {},
  useLayoutEffect() {},
  useRef(initial) { return { current: initial }; },
  useCallback(callback) { return callback; },
  useMemo(factory) { return factory(); },
  useContext(context) { return context?._defaultValue; },
  useReducer(reducer, initial) { return [initial, () => {}]; },
  useErrorBoundary() { return [undefined, () => {}]; },
  useId() { return "test-id"; },
  useDebugValue() {},
  useImperativeHandle() {},
}));

const { MobileStatusLine } = await import("./MobileStatusLine.jsx");
const { setState } = await import("../../../data/store.js");
const { MODEL_CATALOG_IDLE, loadModelCatalog } = await import("../../../data/model-catalog.js");

const ASTRA = {
  id: "gpt-6-astra", name: "GPT-6 Astra", provider: "openai", alias: "astra",
  reasoning_efforts: ["low", "medium", "high", "xhigh", "max"],
};

const SESSION = {
  id: "s1", model: "GPT-6 Astra", provider: "openai", thinking: "low",
  state: "idle", permissionMode: "yolo", contextPercent: 20, subagents: {},
};

// Walks the vnode tree, invoking components so the assertions see the markup
// the phone paints rather than the top-level element.
function expand(node, depth = 0) {
  if (node == null || typeof node !== "object" || depth > 12) return node;
  if (Array.isArray(node)) return node.map((child) => expand(child, depth));
  if (typeof node.type === "function") return expand(node.type(node.props), depth + 1);
  return { ...node, props: { ...node.props, children: expand(node.props?.children, depth + 1) } };
}

function findByClass(node, className) {
  if (node == null || typeof node !== "object") return undefined;
  if (Array.isArray(node)) {
    for (const child of node) {
      const found = findByClass(child, className);
      if (found) return found;
    }
    return undefined;
  }
  const cls = node.props?.class;
  if (typeof cls === "string" && cls.split(" ").includes(className)) return node;
  return findByClass(node.props?.children, className);
}

function renderMeter() {
  const pill = findByClass(expand(MobileStatusLine({ session: SESSION, usage: null })), "model-pill");
  expect(pill).toBeDefined();
  return findByClass(pill, "thinking-meter");
}

beforeEach(() => {
  setState({ modelCatalog: MODEL_CATALOG_IDLE, sessions: { s1: SESSION } });
});

test("the status line draws no meter until the shared model catalog answers", () => {
  // The bug this replaces: with no catalog the line drew Astra's "low" as one
  // filled bar, contradicting the selector's zero.
  expect(renderMeter()).toBeUndefined();
});

test("once the catalog is ready Astra low paints the zero position", async () => {
  globalThis.fetch = () => Promise.resolve(new Response(JSON.stringify([ASTRA]), { status: 200 }));
  await loadModelCatalog();

  const meter = renderMeter();
  expect(meter).toBeDefined();
  expect(meter.props["aria-label"]).toBe("Thinking: low");
  expect(meter.props.children.filter((bar) => bar.props.class === "on")).toHaveLength(0);
});

test("an ordinary model keeps its own level once the catalog is ready", async () => {
  globalThis.fetch = () => Promise.resolve(new Response(JSON.stringify([
    { id: "gpt-5.6-terra", name: "GPT-5.6 Terra", provider: "openai", alias: "terra" },
  ]), { status: 200 }));
  await loadModelCatalog();
  SESSION.model = "GPT-5.6 Terra";
  SESSION.thinking = "medium";

  try {
    const meter = renderMeter();
    expect(meter.props.children.filter((bar) => bar.props.class === "on")).toHaveLength(2);
  } finally {
    SESSION.model = "GPT-6 Astra";
    SESSION.thinking = "low";
  }
});
