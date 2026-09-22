import { afterEach, expect, mock, test } from "bun:test";

// The selector is exercised as a function component over a minimal hook
// runtime (real useState/useEffect/useMemo/useRef semantics, no DOM), the
// same harness CommandPalette.test.jsx uses. The pinning gesture is about what
// a tap DOES and what goes over the wire, not about pixels.
let hooks = [];
let cursor = 0;
let effects = [];
let dirty = false;

function areEqual(a, b) {
  return Array.isArray(a) && Array.isArray(b) && a.length === b.length && a.every((v, i) => Object.is(v, b[i]));
}

// Spread the real hooks first: bun's mock.module replaces the module for the
// whole process and never restores it.
const realHooks = await import("preact/hooks");
mock.module("preact/hooks", () => ({
  ...realHooks,
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
}));

const toasts = [];
const realNotifications = await import("../../data/notifications.js");
mock.module("../../data/notifications.js", () => ({
  ...realNotifications,
  addToast: (toast) => { toasts.push(toast); return toasts.length; },
}));

const { Fragment } = await import("preact");
const { ModelSelector, PickerPopover } = await import("./ModelSelector.jsx");

const MODELS = [
  { id: "anthropic/claude-opus-4-8", catalogId: "claude-opus-4-8", name: "Claude Opus 4.8", codename: "Opus", provider: "anthropic", sub: "4.8" },
  { id: "anthropic/claude-haiku-4-5", catalogId: "claude-haiku-4-5", name: "Claude Haiku 4.5", codename: "Haiku", provider: "anthropic", sub: "4.5" },
  { id: "openai/gpt-5-sol", catalogId: "gpt-5-sol", name: "GPT Sol", codename: "Sol", provider: "openai", sub: "5.5" },
];

// ── wire ──────────────────────────────────────────────────────────────────
const realFetch = globalThis.fetch;
let requests = [];
let serverPins = [];
let failPatch = false;

function respond(status, body) {
  const text = JSON.stringify(body);
  return Promise.resolve({ ok: status < 400, status, text: () => Promise.resolve(text), json: () => Promise.resolve(body) });
}

function installServer(pins) {
  serverPins = [...pins];
  requests = [];
  failPatch = false;
  globalThis.fetch = (url, opts = {}) => {
    const body = opts.body ? JSON.parse(opts.body) : null;
    requests.push({ method: opts.method, url, body });
    if (url === "/api/model-preferences" && opts.method === "PATCH") {
      if (failPatch) return respond(500, "failed to save model preferences");
      serverPins = body.pinned
        ? (serverPins.includes(body.model_id) ? serverPins : [...serverPins, body.model_id])
        : serverPins.filter((id) => id !== body.model_id);
      return respond(200, { pinned_models: serverPins });
    }
    if (url === "/api/model-preferences") return respond(200, { pinned_models: serverPins });
    return respond(200, {});
  };
}

afterEach(() => { globalThis.fetch = realFetch; toasts.length = 0; });

// ── tiny renderer ─────────────────────────────────────────────────────────
let tree = null;
let component = null;
let props = null;

function renderOnce() {
  cursor = 0;
  tree = component(props);
  const queued = effects;
  effects = [];
  for (const fn of queued) fn();
}

async function flush(times = 12) {
  for (let i = 0; i < times; i++) {
    dirty = false;
    renderOnce();
    for (let t = 0; t < 12; t++) await Promise.resolve();
    if (!dirty && !effects.length) break;
  }
}

async function mount(Component, overrides = {}) {
  hooks = [];
  effects = [];
  component = Component;
  props = overrides;
  await flush();
}

// Expands the hook-free children (chips, icons, fragments) so the tree reads
// as what the user sees.
function walk(node, out = []) {
  if (node == null || typeof node !== "object") return out;
  if (Array.isArray(node)) { for (const child of node) walk(child, out); return out; }
  out.push(node);
  if (typeof node.type === "function" && node.type !== Fragment) {
    walk(node.type(node.props), out);
    return out;
  }
  walk(node.props?.children, out);
  return out;
}

function textOf(node) {
  if (node == null || typeof node === "boolean") return "";
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (Array.isArray(node)) return node.map(textOf).join("");
  if (typeof node.type === "function" && node.type !== Fragment) return textOf(node.type(node.props));
  return textOf(node.props?.children);
}

const nodes = () => walk(tree);
const byClass = (cls) => nodes().filter((n) => String(n.props?.class || "").split(" ").includes(cls));
const chips = () => byClass("zl-mchip");
const chip = (name) => chips().find((n) => textOf(n).includes(name));
const modeButton = () => byClass("zl-group-act")[0];
const hints = () => byClass("zl-pin-hint").map(textOf);

async function click(node) {
  node.props.onClick();
  await flush();
}

async function mountSelector(pins, overrides = {}) {
  installServer(pins);
  const picked = [];
  await mount(ModelSelector, {
    models: MODELS,
    selected: "anthropic/claude-opus-4-8",
    sessionModel: "anthropic/claude-opus-4-8",
    onSelect: (id) => picked.push(id),
    ...overrides,
  });
  return picked;
}

test("outside the mode a chip picks; inside it the same chip pins instead", async () => {
  const picked = await mountSelector(["claude-opus-4-8", "gpt-5-sol"]);
  expect(modeButton().props["aria-label"]).toBe("Edit pinned models");
  expect(textOf(modeButton())).toBe("Edit");

  await click(chip("Sol"));
  expect(picked).toEqual(["openai/gpt-5-sol"]);
  expect(requests.filter((r) => r.method === "PATCH")).toHaveLength(0);

  await click(modeButton());
  expect(textOf(modeButton())).toBe("Done");
  expect(modeButton().props["aria-label"]).toBe("Done pinning models");
  expect(hints()).toEqual(["Tap a model to pin or unpin it."]);
  expect(chip("Sol").props["aria-pressed"]).toBe(true);
  expect(chip("Sol").props["aria-label"]).toBe("Pin Sol");

  await click(chip("Sol"));
  expect(picked).toEqual(["openai/gpt-5-sol"]);
  expect(requests.filter((r) => r.method === "PATCH")).toHaveLength(1);

  await click(modeButton());
  expect(textOf(modeButton())).toBe("Edit");
  await click(chip("Opus"));
  expect(picked).toEqual(["openai/gpt-5-sol", "anthropic/claude-opus-4-8"]);
});

test("each tap sends its own PATCH with the catalog id and the new pin state", async () => {
  await mountSelector(["claude-opus-4-8", "gpt-5-sol"]);
  await click(modeButton());

  await click(chip("Sol"));
  // Unpinned, yet still in the grid: the chip stays under the finger.
  expect(chip("Sol").props["aria-pressed"]).toBe(false);
  expect(chip("Sol").props.class).not.toContain("is-pinned");

  // A model that is not pinned yet is reached through its provider's page.
  props = { ...props, view: "anthropic", setView: () => {} };
  await flush();
  expect(hints()).toEqual(["Tap a model to pin or unpin it."]);
  expect(chip("Haiku").props["aria-pressed"]).toBe(false);
  await click(chip("Haiku"));
  expect(chip("Haiku").props["aria-pressed"]).toBe(true);

  expect(requests.filter((r) => r.method === "PATCH").map((r) => r.body)).toEqual([
    { model_id: "gpt-5-sol", pinned: false },
    { model_id: "claude-haiku-4-5", pinned: true },
  ]);
  expect(serverPins).toEqual(["claude-opus-4-8", "claude-haiku-4-5"]);
});

test("a failed PATCH puts the pin back and says so", async () => {
  await mountSelector(["claude-opus-4-8"]);
  await click(modeButton());
  failPatch = true;

  chip("Opus").props.onClick();
  // Optimistic: the chip flips before the server answers.
  renderOnce();
  expect(chip("Opus").props["aria-pressed"]).toBe(false);

  await flush();
  expect(chip("Opus").props["aria-pressed"]).toBe(true);
  expect(toasts).toHaveLength(1);
  expect(toasts[0]).toMatchObject({ title: "Could not unpin Opus", type: "error" });
});

test("two failed taps leave what the server has, not a half-applied guess", async () => {
  // The snapshot taken before the second tap already holds the first tap's
  // optimistic guess, so restoring it would draw a pin the server refused.
  await mountSelector([]);
  await click(modeButton());
  props = { ...props, view: "anthropic", setView: () => {} };
  await flush();
  failPatch = true;

  chip("Opus").props.onClick();
  chip("Haiku").props.onClick();
  renderOnce();
  expect(chip("Opus").props["aria-pressed"]).toBe(true);
  expect(chip("Haiku").props["aria-pressed"]).toBe(true);

  await flush();
  // The re-read is a second round trip: the failed PATCH resolves first.
  await flush();
  expect(serverPins).toEqual([]);
  expect(chip("Opus").props["aria-pressed"]).toBe(false);
  expect(chip("Haiku").props["aria-pressed"]).toBe(false);
  expect(toasts).toHaveLength(2);
});

test("an empty Pinned group says what to do, in and out of the mode", async () => {
  await mountSelector([]);
  expect(chips()).toHaveLength(0);
  expect(hints()).toEqual(["Tap Edit to keep your go-to models here."]);
  await click(modeButton());
  expect(hints()).toEqual(["Open All models and tap one to pin it."]);
});

test("the controlled catalogue never calls the API", async () => {
  const changes = [];
  await mountSelector(["gpt-5-sol"], { pinnedIDs: ["gpt-5-sol"], onPinnedChange: (ids) => changes.push(ids) });
  await click(modeButton());
  props = { ...props, view: "anthropic", setView: () => {} };
  await flush();
  await click(chip("Opus"));
  expect(changes).toEqual([["gpt-5-sol", "claude-opus-4-8"]]);
  expect(requests).toHaveLength(0);

  // Without a handler the lab keeps its own copy, so it can still be played with.
  await mountSelector(["gpt-5-sol"], { pinnedIDs: ["gpt-5-sol"] });
  await click(modeButton());
  await click(chip("Sol"));
  expect(chip("Sol").props["aria-pressed"]).toBe(false);
  expect(textOf(byClass("zl-group-n")[0])).toBe("0");
  expect(requests).toHaveLength(0);
});

test("turning on model-only leaves chips choosing, never pinning", async () => {
  const picked = await mountSelector(["claude-opus-4-8"]);
  await click(modeButton());
  props = { ...props, modelOnly: true, view: "anthropic", setView: () => {} };
  await flush();

  expect(chip("Opus").props["aria-label"]).toBeUndefined();
  await click(chip("Opus"));
  expect(picked).toEqual(["anthropic/claude-opus-4-8"]);
  expect(requests.filter((r) => r.method === "PATCH")).toHaveLength(0);
});

// Closing the host unmounts the selector, so a normal reopen starts fresh.
// The narrow case is a reopen DURING the exit, when usePresence keeps the host
// mounted: the host keys its content on each opening so the mode still dies.
test("the mode does not survive closing the picker", async () => {
  await mountSelector(["claude-opus-4-8"]);
  await click(modeButton());
  expect(textOf(modeButton())).toBe("Done");

  await mountSelector(["claude-opus-4-8"]);
  expect(textOf(modeButton())).toBe("Edit");

  const hadWindow = "window" in globalThis;
  const realWindow = globalThis.window;
  if (!hadWindow) globalThis.window = { addEventListener() {}, removeEventListener() {} };
  try {
    const content = () => walk(tree).find((n) => n.type === Fragment && n.key != null);
    await mount(PickerPopover, { kind: "model", models: MODELS, onClose() {}, leaving: false, children: () => null });
    const first = content().key;
    props = { ...props, leaving: true };
    await flush();
    expect(content().key).toBe(first);
    props = { ...props, leaving: false };
    await flush();
    expect(content().key).not.toBe(first);
  } finally {
    if (hadWindow) globalThis.window = realWindow;
    else delete globalThis.window;
  }
});
