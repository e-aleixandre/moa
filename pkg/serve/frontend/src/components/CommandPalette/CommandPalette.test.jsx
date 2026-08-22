import { test, expect, mock } from "bun:test";

// The palette is exercised as a function component over a minimal hook
// runtime: real useState/useEffect/useMemo semantics (so a keystroke really
// re-renders and effects really run), no DOM. That is enough to assert the
// create → model → create flow, which is about steps and state, not pixels.
let hooks = [];
let cursor = 0;
let effects = [];
let dirty = false;

function areEqual(a, b) {
  return Array.isArray(a) && Array.isArray(b) && a.length === b.length && a.every((v, i) => Object.is(v, b[i]));
}

mock.module("preact/hooks", () => ({
  useState(initial) {
    const cell = hooks[cursor] || (hooks[cursor] = { value: typeof initial === "function" ? initial() : initial });
    const at = cursor++;
    return [cell.value, (next) => {
      const value = typeof next === "function" ? next(hooks[at].value) : next;
      if (Object.is(value, hooks[at].value)) return;
      hooks[at].value = value;
      dirty = true;
    }];
  },
  useEffect(fn, deps) {
    const cell = hooks[cursor] || (hooks[cursor] = {});
    cursor++;
    if (deps && areEqual(cell.deps, deps)) return;
    cell.deps = deps;
    effects.push(fn);
  },
  useRef(initial) {
    const cell = hooks[cursor] || (hooks[cursor] = { value: { current: initial } });
    cursor++;
    return cell.value;
  },
  useMemo(factory, deps) {
    const cell = hooks[cursor] || (hooks[cursor] = {});
    cursor++;
    if (!deps || !areEqual(cell.deps, deps)) {
      cell.deps = deps;
      cell.value = factory();
    }
    return cell.value;
  },
  useCallback(callback, deps) {
    const cell = hooks[cursor] || (hooks[cursor] = {});
    cursor++;
    if (!deps || !areEqual(cell.deps, deps)) {
      cell.deps = deps;
      cell.value = callback;
    }
    return cell.value;
  },
}));

// The server default is deliberately NOT the first catalogued model: the row
// must show what a create would really use, not whatever happens to head the
// catalogue.
const MODELS = [
  { id: "claude-haiku-4-5", name: "Claude Haiku 4.5", provider: "anthropic", alias: "luna" },
  { id: "claude-opus-4-8", name: "Claude Opus 4.8", provider: "anthropic", alias: "opus", max_input: 1000000 },
  { id: "gpt-5-sol", name: "GPT Sol", provider: "openai", alias: "sol" },
];

globalThis.document = { activeElement: null };
globalThis.requestAnimationFrame = (fn) => { fn(); return 0; };
globalThis.fetch = (url) => {
  const body = url.startsWith("/api/capabilities")
    ? { homeDir: "/home/u", workspaceRoot: "/home/u/dev/moa", defaultModel: "anthropic/claude-opus-4-8" }
    : url.startsWith("/api/models") ? MODELS
      : url.startsWith("/api/model-preferences") ? { pinned_models: ["gpt-5-sol"] }
        : { entries: [] };
  return Promise.resolve({ ok: true, status: 200, text: () => Promise.resolve(JSON.stringify(body)), json: () => Promise.resolve(body) });
};

const created = [];
mock.module("../../data/session-actions.js", () => ({
  createSession: (opts) => { created.push(opts); return Promise.resolve("s1"); },
  resumeSession: () => Promise.resolve(),
}));

const { CommandPalette } = await import("./CommandPalette.jsx");

// ── tiny renderer ──────────────────────────────────────────────────────────
let tree = null;
let props = null;

function renderOnce() {
  cursor = 0;
  tree = CommandPalette(props);
  const queued = effects;
  effects = [];
  for (const fn of queued) fn();
}

async function flush(times = 8) {
  for (let i = 0; i < times; i++) {
    dirty = false;
    renderOnce();
    for (let t = 0; t < 8; t++) await Promise.resolve();
    if (!dirty && !effects.length) break;
  }
}

async function mount(overrides = {}) {
  hooks = [];
  effects = [];
  created.length = 0;
  props = { open: true, onClose: () => {}, context: "conversation", initialStep: "create", ...overrides };
  await flush();
}

function walk(node, out = []) {
  if (node == null || typeof node !== "object") return out;
  if (Array.isArray(node)) { for (const child of node) walk(child, out); return out; }
  out.push(node);
  walk(node.props?.children, out);
  return out;
}

function text(node) {
  if (node == null || typeof node === "boolean") return "";
  if (typeof node !== "object") return String(node);
  if (Array.isArray(node)) return node.map(text).join("");
  // Rows render leaf components (Highlight) — invoke them to reach their copy.
  if (typeof node.type === "function") return text(node.type(node.props));
  return text(node.props?.children);
}

const nodes = () => walk(tree);
const byClass = (cls) => nodes().filter((n) => typeof n.props?.class === "string" && n.props.class.split(" ").includes(cls));
const rowTexts = () => byClass("row").concat(byClass("sel")).filter((n, i, a) => a.indexOf(n) === i).map(text);
const groups = () => byClass("pal-group").map(text);

async function press(key, init = {}) {
  const handler = nodes().find((n) => n.props?.onKeyDown)?.props.onKeyDown;
  handler({ key, preventDefault() {}, ...init });
  await flush();
}

async function type(value) {
  nodes().find((n) => n.props?.class === "pal-input").props.onInput({ target: { value } });
  await flush();
}

async function click(node) {
  node.props.onClick({});
  await flush();
}

test("create opens on the default model and creates with it in one keystroke", async () => {
  await mount();

  const row = byClass("model-row")[0];
  expect(text(row)).toContain("Opus");
  expect(text(row)).toContain("Change");

  await click(byClass("btn-create")[0]);
  expect(created).toEqual([{ cwd: "/home/u/dev/moa", model: "anthropic/claude-opus-4-8" }]);
});

test("Change opens the model step inside the palette — pinned first, then providers", async () => {
  await mount();
  await click(byClass("model-change")[0]);

  expect(groups()).toEqual(["Pinned", "anthropic", "openai"]);
  expect(nodes().find((n) => n.props?.class === "pal-input").props.placeholder).toBe("Search models…");
  // No second selector chrome: the step is the palette's own list.
  expect(byClass("model-selector")).toHaveLength(0);
  // The cursor lands on the model already in use, so ⏎ changes nothing.
  expect(text(byClass("row").find((n) => n.props.class.includes("sel")))).toContain("Opus");
});

test("⌘M reaches the same step and typing filters it with the model matcher", async () => {
  await mount();
  await press("m", { metaKey: true });
  await type("luna");

  expect(groups()).toEqual(["Results · 1"]);
  expect(rowTexts().join(" ")).toContain("Haiku");
});

test("choosing a model returns to create, shows it, and creates with it", async () => {
  await mount();
  await press("m", { metaKey: true });
  await type("sol");
  await press("Enter");

  expect(text(byClass("model-row")[0])).toContain("Sol");
  expect(text(byClass("crumb-chip")[0])).toContain("New session");

  await click(byClass("btn-create")[0]);
  expect(created).toEqual([{ cwd: "/home/u/dev/moa", model: "openai/gpt-5-sol" }]);
});

test("Escape and Backspace back out of the model step without losing the create", async () => {
  await mount();
  await press("m", { metaKey: true });
  await press("Escape");
  expect(byClass("model-row")).toHaveLength(1);

  await press("m", { metaKey: true });
  await press("Backspace");
  expect(byClass("model-row")).toHaveLength(1);
  // The create is intact: it still creates on the server cwd with the default.
  await click(byClass("btn-create")[0]);
  expect(created).toEqual([{ cwd: "/home/u/dev/moa", model: "anthropic/claude-opus-4-8" }]);
});

test("the directory the create step was left on survives a trip to the model step", async () => {
  await mount();
  await type("/home/u/dev/other/");
  expect(text(byClass("btn-create")[0])).toContain("other");

  await press("m", { metaKey: true });
  await press("Enter");

  expect(text(byClass("btn-create")[0])).toContain("other");
  await click(byClass("btn-create")[0]);
  expect(created[0].cwd).toBe("/home/u/dev/other");
});

test("a phone never reaches the model step: it hands over to the drawer", async () => {
  const opened = [];
  mock.module("../../data/drawer.js", () => ({ openDrawer: (screen) => opened.push(screen) }));
  await mount({ context: "mobile" });

  expect(opened).toEqual(["new"]);
  expect(byClass("model-row")).toHaveLength(0);
});
