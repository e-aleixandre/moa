import { test, expect, beforeEach, afterEach } from "bun:test";
import { SecretActionView } from "./SecretActionCard.jsx";
import { SecretActionPrompt } from "./SecretActionPrompt.jsx";
import { autofocusAllowed, createSecretCard } from "../../data/secret-card.js";

// No DOM and no hook runtime: the card's behaviour is a controller
// (data/secret-card.js) and its drawing is a pure view, so the test drives the
// controller through the same handlers the view wires up and reads what the
// view draws next. (Mocking preact/hooks cannot work here: bun applies such a
// mock process-wide and the last file to install one wins.)

function expand(node, depth = 0) {
  if (node == null || typeof node !== "object" || depth > 40) return node;
  if (Array.isArray(node)) return node.map((c) => expand(c, depth));
  if (typeof node.type === "function") return expand(node.type(node.props), depth + 1);
  return { ...node, props: { ...node.props, children: expand(node.props?.children, depth + 1) } };
}
function walk(node, out = []) {
  if (node == null || typeof node !== "object") return out;
  if (Array.isArray(node)) { node.forEach((c) => walk(c, out)); return out; }
  out.push(node);
  walk(node.props?.children, out);
  return out;
}
const text = (node) => {
  if (node == null || typeof node === "boolean") return "";
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (Array.isArray(node)) return node.map(text).join("");
  return text(node.props?.children);
};
const settle = async () => { for (let n = 0; n < 8; n++) await new Promise((r) => setTimeout(r, 0)); };

function mount(props, { init = true } = {}) {
  const request = props.request;
  const firstRef = { current: null };
  const bridge = props.bridge ?? (globalThis.window?.moaSavedSecrets || null);
  const card = createSecretCard({ request, send: props.send, bridge, onChange() {} });
  const api = {
    card, firstRef,
    tree: () => expand(SecretActionView({ ...props, bridge, card, firstRef })),
    all: () => walk(api.tree()),
    form: () => api.all().find((n) => n.type === "form"),
    inputs: () => api.all().filter((n) => n.type === "input"),
    input: (name) => api.inputs().find((n) => n.props.name === name),
    buttons: () => api.all().filter((n) => n.type === "button" || n.type === "submit"),
    button: (label) => api.buttons().find((b) => text(b).trim().replace("⏎", "").trim() === label),
    primary: () => api.buttons().find((b) => b.props.type === "submit"),
    switch: () => api.buttons().find((b) => b.props.role === "switch"),
    text: () => text(api.tree()),
    // preact/compat (loaded by other test files) lowercases onInput.
    type(name, value) {
      const props = api.input(name).props;
      (props.onInput || props.oninput)({ currentTarget: { value } });
    },
    async press(label) {
      const b = api.button(label);
      expect(b).toBeDefined();
      // A submit button is a click on the form, as in a browser.
      if (b.props.type === "submit") { if (!b.props.disabled) await api.submit(); return; }
      b.props.onClick?.({});
      await settle();
    },
    async submit() { api.form().props.onSubmit({ preventDefault() {} }); await settle(); },
  };
  api.ready = init ? card.init().then(settle) : Promise.resolve();
  return api;
}

const COMMAND = {
  id: "sec_1", requester: "main", kind: "command",
  command: "ssh -tt me@100.93.175.62 security unlock-keychain", cwd: "~/dev/proj", sshHost: "100.93.175.62",
  actionFingerprint: "fp-1", submit: false, origin: "",
  fields: [{ index: 0, label: "Keychain password", kind: "password", alias: "agents-keychain", element: "", realLabel: "", realType: "" }],
};
const BROWSER = {
  id: "sec_2", requester: "subagent · job7", kind: "browser", origin: "https://idmsa.apple.com", submit: true,
  actionFingerprint: "fp-2", command: "", cwd: "", sshHost: "",
  fields: [
    { index: 0, label: "Username", kind: "username", alias: "", element: "e21", realLabel: "Email or Phone Number", realType: "text" },
    { index: 1, label: "Password", kind: "password", alias: "appstore", element: "e27", realLabel: "Password", realType: "password" },
  ],
};
const session = { id: "s1", subagents: { job7: { title: "sonnet" } } };

let sent;
let failWith;
const send = async (id, body) => {
  sent.push({ id, body: JSON.parse(JSON.stringify(body)) });
  if (failWith) throw failWith;
};

const globals = { window: globalThis.window };
beforeEach(() => {
  sent = [];
  failWith = null;
  globalThis.window = { matchMedia: () => ({ matches: false }) };
});
afterEach(() => {
  if (globals.window === undefined) delete globalThis.window;
  else globalThis.window = globals.window;
});

// A fake native bridge that records every call.
function bridge({ statuses = {}, values = {}, useError = null } = {}) {
  const calls = [];
  const b = {
    calls,
    status: async (args) => { calls.push(["status", args]); return statuses[args.alias] || { saved: false, sameAction: false }; },
    use: async (args) => {
      calls.push(["use", args]);
      if (useError) throw useError;
      return { value: values[args.alias] };
    },
    touch: async (args) => { calls.push(["touch", args]); return {}; },
    save: async (args) => { calls.push(["save", args]); return { ok: true }; },
  };
  globalThis.window.moaSavedSecrets = b;
  return b;
}

// ── the two variants ─────────────────────────────────────────────────────────

test("a command card shows where it runs and the exact command, with one password field", () => {
  const c = mount({ session, request: COMMAND, send });
  const t = c.text();
  expect(t).toContain("Password for this command");
  expect(t).toContain("~/dev/proj");
  expect(t).toContain(COMMAND.command);
  expect(t).toContain("runs on 100.93.175.62 via SSH");
  expect(t).toContain("Sent only to this command, not to the chat");
  expect(c.button("Run")).toBeDefined();
  expect(c.button("Deny")).toBeDefined();
  const pw = c.input("password");
  expect(pw.props.type).toBe("password");
  expect(pw.props.autocomplete).toBe("current-password");
  expect(c.form().props.autocomplete).toBe("on");
  // Not shown on the web: nothing about saving.
  expect(t).not.toContain("Save on this iPhone");
  expect(t).not.toContain("agents-keychain");
});

test("a command without ssh has no host chip", () => {
  const c = mount({ session, request: { ...COMMAND, sshHost: "" }, send });
  expect(c.text()).not.toContain("via SSH");
});

test("a browser card shows the origin, the field mapping and the Enter press", () => {
  const c = mount({ session, request: BROWSER, send });
  const t = c.text();
  expect(t).toContain("Sign in on this page");
  expect(t).toContain("https://idmsa.apple.com");
  expect(t).toContain("→ field “Email or Phone Number” (text)");
  expect(t).toContain("→ field “Password” (password)");
  expect(t).toContain("Then");
  expect(t).toContain("presses Enter");
  expect(t).toContain("Typed by the browser tool. Later page snapshots can show it");
  expect(c.button("Fill in")).toBeDefined();
  expect(t).toContain("subagent · sonnet");
  expect(c.input("username").props.autocomplete).toBe("username");
  expect(c.input("username").props.type).toBe("text");
  expect(c.input("password").props.autocomplete).toBe("current-password");
});

test("a browser card that does not press Enter does not say so", () => {
  const c = mount({ session, request: { ...BROWSER, submit: false }, send });
  expect(c.text()).not.toContain("presses Enter");
});

test("a text field opts out of autofill", () => {
  const request = { ...COMMAND, fields: [{ index: 0, label: "Code", kind: "text", alias: "" }] };
  const c = mount({ session, request, send });
  expect(c.input("field-0").props.autocomplete).toBe("off");
  expect(c.input("field-0").props.type).toBe("text");
});

test("the main agent has no requester label", () => {
  expect(mount({ session, request: COMMAND, send }).text()).not.toContain("subagent");
});

test("several pending requests show 1 of N and the oldest first", () => {
  const tree = SecretActionPrompt({ session: { id: "s1", pendingSecrets: [COMMAND, BROWSER] } });
  expect(tree.props.request.id).toBe("sec_1");
  expect(tree.props.total).toBe(2);
  const c = mount({ session, request: COMMAND, position: 1, total: 2, send });
  expect(c.text()).toContain("1 of 2");
  expect(mount({ session, request: COMMAND, send }).text()).not.toContain("1 of");
  expect(SecretActionPrompt({ session: { id: "s1", pendingSecrets: [] } })).toBeNull();
});

test("font stays at the field floor and the mobile height is 48px", async () => {
  const { readFileSync } = await import("node:fs");
  const css = readFileSync(new URL("./SecretActionCard.css", import.meta.url), "utf8");
  expect(css).toMatch(/\.zl-sx \.zl-sx-input \{[^}]*min-height: 48px/);
  expect(css).not.toMatch(/font-size:\s*1[0-5]px/);
});

// ── typing and sending ───────────────────────────────────────────────────────

test("Run stays disabled until every field has a value", () => {
  const c = mount({ session, request: BROWSER, send });
  const primary = () => c.buttons().find((b) => b.props.type === "submit");
  expect(primary().props.disabled).toBe(true);
  c.type("username", "me@example.com");
  expect(primary().props.disabled).toBe(true);
  c.type("password", "pw");
  expect(primary().props.disabled).toBe(false);
});

test("an empty submit sends nothing", async () => {
  const c = mount({ session, request: COMMAND, send });
  await c.submit();
  expect(sent).toEqual([]);
});

test("allow POSTs exactly {action, values:[{index,value}]} and clears the inputs", async () => {
  const c = mount({ session, request: BROWSER, send });
  c.type("username", "me@example.com");
  c.type("password", "hunter2-not-real");
  await c.submit();
  expect(sent).toEqual([{
    id: "sec_2",
    body: { action: "allow", values: [{ index: 0, value: "me@example.com" }, { index: 1, value: "hunter2-not-real" }] },
  }]);
  expect(c.input("username").props.value).toBe("");
  expect(c.input("password").props.value).toBe("");
});

test("Enter in a field submits (the form's submit)", async () => {
  const c = mount({ session, request: COMMAND, send });
  c.type("password", "pw");
  await c.submit();
  expect(sent).toHaveLength(1);
});

test("the values are cleared even when the server refuses, and the error stays on the card", async () => {
  failWith = Object.assign(new Error('400: {"echo":"hunter2"}'), { status: 400 });
  const errors = console.error;
  console.error = () => {};
  try {
    const c = mount({ session, request: COMMAND, send });
    c.type("password", "hunter2");
    await c.submit();
    expect(c.input("password").props.value).toBe("");
    expect(c.text()).toContain("The server refused these values");
    expect(c.text()).not.toContain("hunter2");
    // Still usable: type again and send again.
    failWith = null;
    c.type("password", "second");
    await c.submit();
    expect(sent.at(-1).body.values[0].value).toBe("second");
  } finally {
    console.error = errors;
  }
});

test("a second press while the request is in flight is ignored", async () => {
  let release;
  const slow = (id, body) => { sent.push({ id, body }); return new Promise((r) => { release = r; }); };
  const c = mount({ session, request: COMMAND, send: slow });
  c.type("password", "pw");
  c.form().props.onSubmit({ preventDefault() {} });
  c.form().props.onSubmit({ preventDefault() {} });
  await settle();
  expect(sent).toHaveLength(1);
  release();
});

test("Deny POSTs deny and sends no value", async () => {
  const c = mount({ session, request: COMMAND, send });
  c.type("password", "typed-then-denied");
  await c.press("Deny");
  expect(sent).toEqual([{ id: "sec_1", body: { action: "deny" } }]);
  expect(c.input("password").props.value).toBe("");
});

test("Show reveals one field and Hide covers it again", () => {
  const c = mount({ session, request: BROWSER, send });
  const toggle = () => c.buttons().find((b) => b.props["aria-label"] === "Show Password" || b.props["aria-label"] === "Hide Password");
  expect(c.input("password").props.type).toBe("password");
  toggle().props.onClick();
  expect(c.input("password").props.type).toBe("text");
  expect(toggle().props["aria-label"]).toBe("Hide Password");
  toggle().props.onClick();
  expect(c.input("password").props.type).toBe("password");
  // The username is plain text: it has no Show.
  expect(c.buttons().some((b) => b.props["aria-label"] === "Show Username")).toBe(false);
});

test("the first empty field gets the focus ref, and only on desktop", () => {
  const c = mount({ session, request: BROWSER, send });
  const refs = c.inputs().map((n) => n.ref);
  expect(refs[0]).toBe(c.firstRef);
  expect(refs[1]).toBeUndefined();
  // Once it is typed in, the next empty field is the target.
  c.type("username", "me");
  expect(c.inputs().map((n) => n.ref)).toEqual([undefined, c.firstRef]);

  expect(autofocusAllowed({ matchMedia: () => ({ matches: false }) })).toBe(true);
  expect(autofocusAllowed({ matchMedia: () => ({ matches: true }) })).toBe(false);
  expect(autofocusAllowed({})).toBe(true);
});

test("inputs are written so the font never drops below 16px (shared Field)", () => {
  const c = mount({ session, request: COMMAND, send });
  const wrapper = c.all().find((n) => String(n.props?.class || "").includes("field "));
  expect(wrapper).toBeDefined();
  expect(c.input("password").props.class).toBe("field-input");
});

// ── Save on this iPhone (native only) ───────────────────────────────────────

test("saved for the same action: one tap, Face ID for the value, then touch", async () => {
  const b = bridge({ statuses: { "agents-keychain": { saved: true, sameAction: true, savedAt: 1 } }, values: { "agents-keychain": "from-keychain" } });
  const c = mount({ session, request: COMMAND, send });
  await c.ready;
  expect(b.calls[0]).toEqual(["status", { alias: "agents-keychain", actionFingerprint: "fp-1" }]);
  expect(c.text()).toContain("agents-keychain");
  expect(c.text()).toContain("Saved on this iPhone · same command as last use");
  expect(c.inputs()).toHaveLength(0);
  expect(c.button("Allow with Face ID")).toBeDefined();
  expect(c.button("Run")).toBeUndefined();

  await c.press("Allow with Face ID");
  expect(sent).toEqual([{ id: "sec_1", body: { action: "allow", values: [{ index: 0, value: "from-keychain" }] } }]);
  expect(b.calls.filter(([m]) => m === "use")).toHaveLength(1);
  expect(b.calls.at(-1)).toEqual(["touch", { alias: "agents-keychain", actionFingerprint: "fp-1" }]);
  // use() happened before the POST, touch after it.
  expect(b.calls.findIndex(([m]) => m === "use")).toBeLessThan(b.calls.findIndex(([m]) => m === "touch"));
});

test("Type another swaps the saved row for the input and the plain primary", async () => {
  bridge({ statuses: { "agents-keychain": { saved: true, sameAction: true } } });
  const c = mount({ session, request: COMMAND, send });
  await c.ready;
  await c.press("Type another");
  expect(c.input("password")).toBeDefined();
  expect(c.button("Run")).toBeDefined();
  expect(c.button("Allow with Face ID")).toBeUndefined();
});

test("a failed or cancelled Face ID keeps the card, shows the input and sends nothing", async () => {
  const b = bridge({
    statuses: { "agents-keychain": { saved: true, sameAction: true } },
    useError: Object.assign(new Error("cancelled"), { code: "cancelled" }),
  });
  const c = mount({ session, request: COMMAND, send });
  await c.ready;
  await c.press("Allow with Face ID");
  expect(sent).toEqual([]);
  expect(b.calls.some(([m]) => m === "touch")).toBe(false);
  expect(c.text()).toContain("Face ID was cancelled.");
  expect(c.input("password")).toBeDefined();
  expect(c.button("Run")).toBeDefined();
  // The owner can still type it.
  c.type("password", "typed");
  await c.submit();
  expect(sent).toHaveLength(1);
  expect(sent[0].body.values[0].value).toBe("typed");
});

test("saved for a different action: a warning and a fill button, never one tap", async () => {
  const b = bridge({
    statuses: { "agents-keychain": { saved: true, sameAction: false } },
    values: { "agents-keychain": "from-keychain" },
  });
  const c = mount({ session, request: COMMAND, send });
  await c.ready;
  expect(c.text()).toContain("Different from the saved one");
  expect(c.input("password")).toBeDefined();
  expect(c.button("Allow with Face ID")).toBeUndefined();
  // The primary is the plain one and is not pressable yet.
  const primary = c.buttons().find((x) => x.props.type === "submit");
  expect(primary.props.disabled).toBe(true);
  await c.submit();
  expect(sent).toEqual([]);
  expect(b.calls.some(([m]) => m === "use")).toBe(false);

  await c.press("Fill saved (Face ID)");
  expect(b.calls.filter(([m]) => m === "use")).toHaveLength(1);
  expect(sent).toEqual([]);
  expect(c.input("password").props.value).toBe("from-keychain");
  expect(c.text()).not.toContain("Different from the saved one");

  // The owner then presses Run.
  await c.submit();
  expect(sent).toEqual([{ id: "sec_1", body: { action: "allow", values: [{ index: 0, value: "from-keychain" }] } }]);
  expect(b.calls.at(-1)).toEqual(["touch", { alias: "agents-keychain", actionFingerprint: "fp-1" }]);
});

test("editing a value filled from the keychain does not touch it afterwards", async () => {
  const b = bridge({
    statuses: { "agents-keychain": { saved: true, sameAction: false } },
    values: { "agents-keychain": "from-keychain" },
  });
  const c = mount({ session, request: COMMAND, send });
  await c.ready;
  await c.press("Fill saved (Face ID)");
  c.type("password", "something else");
  await c.submit();
  expect(b.calls.some(([m]) => m === "touch")).toBe(false);
});

test("Save on this iPhone is off by default and saves only when switched on", async () => {
  const b = bridge();
  const c = mount({ session, request: COMMAND, send });
  await c.ready;
  const sw = () => c.buttons().find((x) => x.props.role === "switch");
  expect(sw().props["aria-checked"]).toBe(false);
  c.type("password", "pw1");
  await c.submit();
  expect(b.calls.some(([m]) => m === "save")).toBe(false);

  const b2 = bridge();
  const c2 = mount({ session, request: COMMAND, send });
  await c2.ready;
  c2.buttons().find((x) => x.props.role === "switch").props.onClick();
  c2.type("password", "pw2");
  await c2.submit();
  const save = b2.calls.find(([m]) => m === "save")[1];
  expect(save).toEqual({
    alias: "agents-keychain", actionFingerprint: "fp-1", label: "Keychain password",
    actionSummary: COMMAND.command, value: "pw2",
  });
});

test("Save only offers fields that have an alias, and summarises a browser by its origin", async () => {
  const b = bridge();
  const c = mount({ session, request: BROWSER, send });
  await c.ready;
  c.buttons().find((x) => x.props.role === "switch").props.onClick();
  c.type("username", "me");
  c.type("password", "pw");
  await c.submit();
  const saves = b.calls.filter(([m]) => m === "save").map(([, a]) => a);
  expect(saves).toHaveLength(1);
  expect(saves[0]).toMatchObject({ alias: "appstore", actionSummary: "https://idmsa.apple.com", value: "pw" });

  const noAlias = { ...COMMAND, fields: [{ index: 0, label: "Code", kind: "text", alias: "" }] };
  bridge();
  const c3 = mount({ session, request: noAlias, send });
  await c3.ready;
  expect(c3.buttons().some((x) => x.props.role === "switch")).toBe(false);
});

test("a save that fails after the answer was accepted does not surface", async () => {
  const b = bridge();
  b.save = async () => { throw Object.assign(new Error("x"), { code: "biometry_unavailable" }); };
  const c = mount({ session, request: COMMAND, send });
  await c.ready;
  c.buttons().find((x) => x.props.role === "switch").props.onClick();
  c.type("password", "pw");
  await c.submit();
  expect(sent).toHaveLength(1);
  expect(c.text()).not.toContain("biometry");
});
