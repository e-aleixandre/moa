// ProvidersPage.test.jsx — what a Providers row draws in each step, walked as a
// vnode tree with the row's state chosen by the test (no DOM). R18: fields
// ≥16px and write-only, popup-blocked link that shares nothing, device rows
// read-only, instructions before opening.
import { test, expect, mock } from "bun:test";
import { readFileSync } from "node:fs";

const realHooks = await import("preact/hooks");
let rowState = null;
mock.module("preact/hooks", () => ({
  ...realHooks,
  useState(initial) {
    // The row's flow state is the one whose initial value is IDLE.
    const value = rowState && initial && typeof initial === "object" && initial.step === "idle" ? rowState : initial;
    return [typeof value === "function" ? value() : value, () => {}];
  },
  useEffect() {},
  useLayoutEffect() {},
  useRef(initial) { return { current: initial }; },
  useCallback(cb) { return cb; },
  useMemo(f) { return f(); },
}));

const { ProviderRow } = await import("./ProvidersPage.jsx");
const { IDLE } = await import("./provider-row-controller.js");
const { PROVIDER_ROWS } = await import("./providers-model.js");

function expand(node, depth = 0) {
  if (node == null || typeof node !== "object" || depth > 40) return node;
  if (Array.isArray(node)) return node.map((child) => expand(child, depth));
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

const def = (id) => PROVIDER_ROWS.find((r) => r.id === id);
const ROW = {
  anthropic: { id: "anthropic", source: "store", kind: "api_key", credential_generation: "g", state: "key_rejected", attention: true, actions: ["sign_in", "api_key"], oauth_enabled: true, api_key_enabled: true },
};

function render(id, { state = IDLE, row = ROW.anthropic, canAdmin = true } = {}) {
  rowState = state;
  const tree = expand({ type: ProviderRow, props: { def: def(id), row, canAdmin, loading: false, focused: false, onChanged() {} } });
  rowState = null;
  return tree;
}

const fields = (tree) => all(tree, (n) => n.type === "input" || n.type === "textarea");
const buttons = (tree) => all(tree, (n) => n.type === "button");

test("the API key field is write-only: password, empty, no autofill, no reveal or copy", () => {
  const tree = render("anthropic", { state: { ...IDLE, step: "key" } });
  const [input, ...rest] = fields(tree);
  expect(rest).toEqual([]);
  expect(input.type).toBe("input");
  expect(input.props).toMatchObject({
    type: "password", value: "", autocomplete: "off", autoCorrect: "off", autocapitalize: "off", spellcheck: false,
  });
  // Field's own input class: font-size is the --text-input floor (16px).
  expect(input.props.class).toBe("field-input");
  const labels = buttons(tree).map((b) => text(b).trim());
  expect(labels.some((l) => /show|reveal|copy|hide/i.test(l))).toBe(false);
  // It has a visible label tied to it.
  const label = all(tree, (n) => n.type === "label")[0];
  expect(label.props.for).toBe(input.props.id);
  expect(text(label)).toBe("Anthropic API key");
});

test("replacing a key shows what it changes before Save", () => {
  const tree = render("anthropic", { state: { ...IDLE, step: "key" } });
  expect(text(tree)).toContain("Applies to all sessions on the next request. Existing history and attachments may be sent using this account.");
  expect(buttons(tree).map((b) => text(b).trim())).toContain("Replace");
});

test("switching key → subscription names the billing change before opening", () => {
  const tree = render("anthropic", { state: { ...IDLE, step: "signin" } });
  expect(text(tree)).toContain("Anthropic will bill your subscription instead of API billing.");
});

test("OpenAI explains the localhost step BEFORE anything is opened", () => {
  const row = { ...ROW.anthropic, id: "openai", kind: undefined, state: "missing" };
  const tree = render("openai", { state: { ...IDLE, step: "signin" }, row });
  expect(text(tree)).toContain("Open OpenAI. After signing in, the localhost page won't load. Copy its full address from the address bar, come back here and paste it.");
  expect(fields(tree)).toEqual([]);
});

test("a blocked popup gets a plain link that shares nothing with moa", () => {
  const attempt = { attempt_id: "a", authorize_url: "https://claude.ai/oauth/authorize?state=s" };
  const tree = render("anthropic", { state: { ...IDLE, step: "paste", attempt, opened: false } });
  const link = all(tree, (n) => n.type === "a")[0];
  expect(link.props).toMatchObject({
    href: attempt.authorize_url, target: "_blank", rel: "noopener noreferrer", referrerPolicy: "no-referrer",
  });
  const [area] = fields(tree);
  expect(area.type).toBe("textarea");
  expect(area.props).toMatchObject({ value: "", autocomplete: "off", spellcheck: false, class: "field-input" });
  expect(text(tree)).toContain("Paste the code#state value or the callback address");
});

test("an opened window needs no link", () => {
  const tree = render("anthropic", { state: { ...IDLE, step: "paste", attempt: { attempt_id: "a", authorize_url: "https://x" }, opened: true } });
  expect(all(tree, (n) => n.type === "a")).toEqual([]);
});

test("the device code step: code, copy of the code only, safe link, warning, cancel", () => {
  const attempt = { attempt_id: "d", user_code: "WXYZ-1234", verification_uri: "https://accounts.x.ai/device", expires_at: "2026-10-03T21:00:00Z" };
  const row = { ...ROW.anthropic, id: "xai", kind: undefined, state: "missing" };
  const tree = render("xai", { state: { ...IDLE, step: "device", attempt, progress: "waiting" }, row });
  const t = text(tree);
  expect(t).toContain("WXYZ-1234");
  expect(t).toContain("Authorize only the code you just requested here.");
  expect(t).toContain("Waiting for authorization…");
  expect(t).toContain("Code expires at");
  const link = all(tree, (n) => n.type === "a")[0];
  expect(link.props).toMatchObject({ target: "_blank", rel: "noopener noreferrer", referrerPolicy: "no-referrer" });
  const labels = buttons(tree).map((b) => text(b).trim());
  expect(labels).toEqual(["Copy code", "Cancel"]);
  expect(fields(tree)).toEqual([]);
});

test("a device row is read-only and says who to ask", () => {
  const tree = render("anthropic", { canAdmin: false, row: { ...ROW.anthropic, actions: undefined, action: "ask_owner" } });
  expect(buttons(tree)).toEqual([]);
  expect(fields(tree)).toEqual([]);
  expect(text(tree)).toContain("Ask the owner to replace the Anthropic API key.");
});

test("an environment row has no actions", () => {
  const tree = render("anthropic", { row: { ...ROW.anthropic, source: "env", state: "ready", attention: false, actions: [] } });
  expect(buttons(tree)).toEqual([]);
  expect(text(tree)).toContain("Managed by environment");
});

test("a failed save offers Retry saving; an unreadable store offers nothing", () => {
  const failed = render("anthropic", { row: { ...ROW.anthropic, kind: undefined, state: "save_failed", actions: ["sign_in", "api_key", "retry_save"] } });
  expect(buttons(failed).map((b) => text(b).trim())[0]).toBe("Retry saving");
  expect(text(failed)).toContain("Could not save credentials.");
  const broken = render("anthropic", { row: { ...ROW.anthropic, kind: undefined, state: "store_unavailable", actions: [] } });
  expect(buttons(broken)).toEqual([]);
});

test("a row never draws account ids, key hints or tokens", () => {
  const row = { ...ROW.anthropic, account_id: "acct-SECRET", key_hint: "…WXYZ", access_token: "tok-SECRET" };
  const t = text(render("anthropic", { row })) + text(render("anthropic", { row, state: { ...IDLE, step: "key" } }));
  expect(t).not.toContain("acct-SECRET");
  expect(t).not.toContain("WXYZ");
  expect(t).not.toContain("tok-SECRET");
});

test("the provider sources write nothing to storage and log nothing", () => {
  for (const file of ["./ProvidersPage.jsx", "./provider-row-controller.js", "./providers-flow.js", "./providers-model.js", "../../data/providers.js", "../ProviderErrorAction/ProviderErrorAction.jsx"]) {
    // Code only: the comments explain why storage is avoided.
    const src = readFileSync(new URL(file, import.meta.url), "utf8")
      .split("\n").filter((line) => !/^\s*(\/\/|\*|\/\*)/.test(line)).join("\n");
    expect(src).not.toMatch(/localStorage|sessionStorage|indexedDB|caches\.|console\./);
  }
  // The status lives in its own module, not the persisted store.
  expect(readFileSync(new URL("../../data/providers.js", import.meta.url), "utf8")).not.toMatch(/from ['"]\.\/store\.js['"]/);
});

test("the fields are the Field primitive (16px floor) and the page CSS never shrinks them", () => {
  const css = readFileSync(new URL("./ProvidersPage.css", import.meta.url), "utf8");
  for (const block of css.split("}")) {
    if (/field-input|input|textarea/.test(block.split("{")[0] || "")) {
      const m = block.match(/font-size:\s*(\d+)px/);
      if (m) expect(Number(m[1])).toBeGreaterThanOrEqual(16);
    }
  }
  const tokens = readFileSync(new URL("../../tokens/tokens.css", import.meta.url), "utf8");
  expect(tokens).toMatch(/--zl-fs-title:\s*16px/);
  expect(tokens).toMatch(/--text-input:\s*var\(--zl-fs-title\)/);
});

// Preact assigns a lowercase `autocorrect` as a DOM property where one exists
// (Safari: a boolean, so "off" turns it on); `autoCorrect` goes out as the attribute.
test("no credential field leaves autocorrect on, key input or paste areas", () => {
  for (const [id, step] of [["anthropic", "key"], ["anthropic", "paste"], ["openai", "paste"]]) {
    const [field] = fields(render(id, { state: { ...IDLE, step } }));
    expect(field.props.autoCorrect).toBe("off");
    expect(field.props.autocorrect).toBeUndefined();
  }
});

test("the primary button follows the needed action", () => {
  const primary = (row) => buttons(render("anthropic", { row })).filter((b) => /is-primary/.test(b.props.class)).map((b) => text(b).trim());
  const base = ROW.anthropic;
  expect(primary({ ...base, state: "key_rejected", kind: "api_key" })).toEqual(["Replace API key"]);
  expect(primary({ ...base, state: "reconnect", kind: "oauth", actions: ["sign_in", "api_key"] })).toEqual(["Reconnect"]);
  expect(primary({ ...base, state: "missing", kind: undefined })).toEqual(["Sign in"]);
});
