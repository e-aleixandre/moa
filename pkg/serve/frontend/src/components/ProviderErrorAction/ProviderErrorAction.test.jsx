// ProviderErrorAction.test.jsx — the conversation tail's action for a run that
// ended on a provider credential: driven by the structured detail, owner vs
// device, and never a replay.
import { test, expect, mock, beforeEach } from "bun:test";
import { readFileSync } from "node:fs";

const realHooks = await import("preact/hooks");
let canAdmin = true;
mock.module("preact/hooks", () => ({
  ...realHooks,
  useState(initial) {
    if (initial && typeof initial === "object" && "attentionCount" in initial) return [{ ...initial, canAdmin }, () => {}];
    return [initial, () => {}];
  },
  useEffect() {},
  useLayoutEffect() {},
  useRef(initial) { return { current: initial }; },
}));

const { ProviderErrorAction, focusComposerNear } = await import("./ProviderErrorAction.jsx");
const providers = await import("../../data/providers.js");
const { normalizeErrorDetail } = await import("../../data/provider-error.js");

function expand(node, depth = 0) {
  if (node == null || typeof node !== "object" || depth > 30) return node;
  if (Array.isArray(node)) return node.map((c) => expand(c, depth));
  if (typeof node.type === "function") return expand(node.type(node.props), depth + 1);
  return { ...node, props: { ...node.props, children: expand(node.props?.children, depth + 1) } };
}
function all(node, match, out = []) {
  if (node == null || typeof node !== "object") return out;
  if (Array.isArray(node)) { node.forEach((c) => all(c, match, out)); return out; }
  if (match(node)) out.push(node);
  all(node.props?.children, match, out);
  return out;
}
function text(node) {
  if (node == null || typeof node === "boolean") return "";
  if (typeof node !== "object") return String(node);
  if (Array.isArray(node)) return node.map(text).join("");
  return text(node.props?.children);
}

const detail = (cls, action, provider = "anthropic") => normalizeErrorDetail({ provider, source: "store", credential_generation: "g", class: cls, action });
const session = (d, over = {}) => ({ id: "s1", state: "error", error: "anything at all", errorDetail: d, ...over });
const render = (s) => expand({ type: ProviderErrorAction, props: { session: s } });
const buttonOf = (tree) => all(tree, (n) => n.type === "button")[0];

beforeEach(() => { canAdmin = true; providers.resetProviderStatusForTest(); });

test("reconnect opens Settings → Providers focused on that provider, for this session", () => {
  const opened = [];
  providers.subscribeProviderSettings((r) => opened.push(r));
  const tree = render(session(detail("reconnect", "reconnect", "openai")));
  const button = buttonOf(tree);
  expect(text(button).trim()).toBe("Reconnect OpenAI");
  button.props.onClick({ currentTarget: null });
  expect(opened).toHaveLength(1);
  expect(opened[0]).toMatchObject({ provider: "openai", returnSessionId: "s1" });
});

test("a rejected key offers Replace API key", () => {
  expect(text(buttonOf(render(session(detail("key_rejected", "replace_key")))))).toContain("Replace API key");
});

test("a failed save offers Retry saving (through Providers)", () => {
  expect(text(buttonOf(render(session(detail("persistence_failed", "retry_save")))))).toContain("Retry saving");
});

test("a device is told who to ask and gets no button", () => {
  canAdmin = false;
  const tree = render(session(detail("reconnect", "reconnect")));
  expect(all(tree, (n) => n.type === "button")).toEqual([]);
  expect(text(tree)).toContain("Ask the owner to reconnect Anthropic.");
});

test("credentials changed only focuses the composer — nothing is sent", () => {
  const sent = [];
  const realFetch = globalThis.fetch;
  globalThis.fetch = (...a) => { sent.push(a); return Promise.resolve(new Response("", { status: 204 })); };
  const tree = render(session(detail("credentials_changed", "send_again")));
  expect(text(tree)).toContain("Credentials changed. Send again.");
  let focused = 0;
  const composer = { focus: () => { focused++; } };
  const pane = { querySelector: (sel) => (sel === "textarea.zl-ta" ? composer : null), parentElement: null };
  const card = { querySelector: () => null, parentElement: pane };
  buttonOf(tree).props.onClick({ currentTarget: card });
  expect(focused).toBe(1);
  expect(sent).toEqual([]);
  globalThis.fetch = realFetch;
});

test("an environment credential points at the server, with no button", () => {
  const tree = render(session(detail("reconnect", "manage_environment")));
  expect(text(tree)).toContain("Update the environment variable on the server.");
  expect(all(tree, (n) => n.type === "button")).toEqual([]);
});

test("nothing without a structured detail, whatever the prose says", () => {
  expect(render(session(null, { error: "anthropic API key was rejected: replace the key" }))).toBeNull();
  expect(render(session(detail("reconnect", "reconnect"), { state: "idle" }))).toBeNull();
  expect(render(session(detail("quota", "wait")))).toBeNull();
});

test("focusComposerNear finds the pane's own composer", () => {
  expect(focusComposerNear(null)).toBe(false);
});

test("the stream mounts it once for desktop, grid and mobile, from the detail", () => {
  const stream = readFileSync(new URL("../../layout/Stream/ConversationStream.jsx", import.meta.url), "utf8");
  expect(stream.match(/<ProviderErrorAction session=\{session\} \/>/g)).toHaveLength(1);
  const mobile = readFileSync(new URL("../../layout/mobile/MobileConversationScreen/MobileStream.jsx", import.meta.url), "utf8");
  expect(mobile).toContain("ConversationStream");
  const src = readFileSync(new URL("./ProviderErrorAction.jsx", import.meta.url), "utf8");
  expect(src).not.toMatch(/session\.error\b(?!Detail)/);
});

const withStatus = (rows) => providers.applyProviderStatus({ can_admin: true, attention_count: 0, providers: rows });
const generated = (cls, action, gen) => normalizeErrorDetail({ provider: "anthropic", source: "store", credential_generation: gen, class: cls, action });

test("a newer credential turns the stale card into Send again, focus only", () => {
  withStatus([{ id: "anthropic", state: "ready", credential_generation: "g2" }]);
  for (const [cls, action] of [["reconnect", "reconnect"], ["key_rejected", "replace_key"], ["persistence_failed", "retry_save"], ["missing", "connect"]]) {
    const tree = render(session(generated(cls, action, "g1")));
    expect(text(tree)).toContain("Anthropic updated. Send again.");
    expect(text(buttonOf(tree)).trim()).toBe("Send again");
    const opened = [];
    providers.subscribeProviderSettings((r) => opened.push(r));
    let focused = 0;
    const pane = { querySelector: () => ({ focus: () => { focused++; } }), parentElement: null };
    buttonOf(tree).props.onClick({ currentTarget: { querySelector: () => null, parentElement: pane } });
    expect(focused).toBe(1);
    expect(opened).toEqual([]);
  }
});

test("the card stays when the generation matches, the status is not loaded, or the provider still needs attention", () => {
  const d = generated("reconnect", "reconnect", "g1");
  expect(text(render(session(d)))).toContain("needs you to sign in again");
  withStatus([{ id: "anthropic", state: "ready", credential_generation: "g1" }]);
  expect(text(render(session(d)))).toContain("needs you to sign in again");
  withStatus([{ id: "anthropic", state: "reconnect", attention: true, credential_generation: "g2" }]);
  expect(text(render(session(d)))).toContain("needs you to sign in again");
});
