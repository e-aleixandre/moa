// McpPage.test.jsx — a remote server waiting for sign-in: how it reads in the
// list, and the Connect → paste → Finish flow, driven through the page's own
// handlers with fetch and window.open faked.
import { test, expect, mock, beforeEach, afterEach } from "bun:test";

// The page is walked as a plain vnode tree (no DOM), so its hooks are stubbed:
// `pick` decides what each useState returns, in call order across the render
// (McpPage: 0 data, 1 failed, 2 open; ServerBody: 3 busy, 4 confirming,
// 5 oauth), and every setter is recorded by that same index.
const realHooks = await import("preact/hooks");
let pick = () => undefined;
let calls = 0;
let setters = [];
mock.module("preact/hooks", () => ({
  ...realHooks,
  useState(initial) {
    const index = calls++;
    const chosen = pick(index, initial);
    const setter = mock(() => {});
    setters[index] = setter;
    return [chosen === undefined ? (typeof initial === "function" ? initial() : initial) : chosen, setter];
  },
  useEffect() {},
  useLayoutEffect() {},
  useRef(initial) { return { current: initial }; },
  useCallback(callback) { return callback; },
  useMemo(factory) { return factory(); },
}));

const { McpPage } = await import("./McpPage.jsx");
const flow = await import("./mcp-oauth-flow.js");
const { MCP_OAUTH_TIMEOUT_MS } = await import("../../data/api.js");

const OAUTH = 5;
const AUTHORIZE = "https://auth.example.com/authorize?client_id=moa&state=abc";
const CALLBACK = "http://127.0.0.1:1/callback?code=xyz&state=abc";

const serverIn = (overrides = {}) => ({
  name: "linear",
  state: "auth_required",
  auth_action: "connect",
  enabled: true,
  tool_count: 0,
  error: "sign-in required",
  disabled_scopes: [],
  ...overrides,
});

function expand(node, depth = 0) {
  if (node == null || typeof node !== "object" || depth > 30) return node;
  if (Array.isArray(node)) return node.map((child) => expand(child, depth));
  if (typeof node.type === "function") return expand(node.type(node.props), depth + 1);
  return { ...node, props: { ...node.props, children: expand(node.props?.children, depth + 1) } };
}

function all(node, match, out = []) {
  if (node == null || typeof node !== "object") return out;
  if (Array.isArray(node)) {
    for (const child of node) all(child, match, out);
    return out;
  }
  if (match(node)) out.push(node);
  all(node.props?.children, match, out);
  return out;
}

const hasClass = (cls) => (n) =>
  typeof n.props?.class === "string" && n.props.class.split(" ").includes(cls);

function text(node) {
  if (node == null || typeof node === "boolean") return "";
  if (typeof node !== "object") return String(node);
  if (Array.isArray(node)) return node.map(text).join("");
  return text(node.props?.children);
}

// Renders the live page with `server` expanded and the body's oauth state set.
function render(server, oauth = null) {
  calls = 0;
  setters = [];
  pick = (index) => {
    if (index === 0) return { servers: [server] };
    if (index === 1) return false;
    if (index === 2) return server.name;
    if (index === OAUTH) return oauth;
    return undefined;
  };
  return expand(McpPage({ sessionId: "s1", mcpTick: 0 }));
}

// Renders the catalogue's inline page: fixtures, no network, initial expansion.
function renderInline(servers) {
  calls = 0;
  setters = [];
  pick = () => undefined;
  return expand(McpPage({ servers, inline: true }));
}

const button = (tree, label) =>
  all(tree, (n) => n.type === "button" && text(n).trim() === label)[0];

let fetchCalls;
let fetchReply;
let opened;
let openReturns;
const realFetch = globalThis.fetch;
const realWindow = globalThis.window;

beforeEach(() => {
  fetchCalls = [];
  fetchReply = () => new Response(JSON.stringify({ servers: [] }), { status: 200 });
  globalThis.fetch = mock(async (path, opts) => {
    fetchCalls.push({ path, method: opts.method, body: opts.body });
    return fetchReply(path, opts);
  });
  opened = [];
  openReturns = () => ({ closed: false, opener: {}, location: { href: "about:blank" }, close() { this.closed = true; } });
  globalThis.window = {
    open: mock((...args) => {
      opened.push(args);
      return openReturns();
    }),
  };
});

afterEach(() => {
  globalThis.fetch = realFetch;
  globalThis.window = realWindow;
});

test("a server waiting for sign-in reads as needing you, not as failed", () => {
  const tree = render(serverIn());
  const state = all(tree, hasClass("zl-mcp-state"))[0];
  expect(text(state)).toBe("needs sign-in");
  expect(state.props.class).toContain("is-need");
  expect(state.props.class).not.toContain("is-bad");
  expect(all(tree, hasClass("zl-mcp-dot"))[0].props.class).not.toContain("is-bad");
  expect(all(tree, hasClass("zl-mcp-err"))).toHaveLength(0);
  expect(text(all(tree, hasClass("zl-mcp-verdict"))[0])).toBe("On everywhere, waiting for you to sign in.");
  expect(text(tree)).not.toContain("failed");
});

test("Connect or Reconnect replaces Restart, following auth_action", () => {
  let tree = render(serverIn());
  expect(button(tree, "Connect").props["aria-label"]).toBe("Connect linear");
  expect(button(tree, "Restart")).toBeUndefined();

  tree = render(serverIn({ auth_action: "reconnect" }));
  expect(button(tree, "Reconnect").props["aria-label"]).toBe("Reconnect linear");
  expect(button(tree, "Restart")).toBeUndefined();

  tree = render(serverIn({ state: "ready", auth_action: undefined, error: "" }));
  expect(button(tree, "Restart")).toBeDefined();
  expect(button(tree, "Connect")).toBeUndefined();
});

test("Connect opens a window in the click and sends it to the sign-in page", async () => {
  const handle = { closed: false, opener: {}, location: { href: "about:blank" } };
  openReturns = () => handle;
  fetchReply = () => new Response(JSON.stringify({ authorize_url: AUTHORIZE }), { status: 200 });

  const tree = render(serverIn());
  const done = button(tree, "Connect").props.onClick();
  // The window must be opened before the first await, inside the gesture.
  expect(opened).toEqual([["", "_blank"]]);
  await done;

  expect(fetchCalls[0]).toMatchObject({ path: "/api/sessions/s1/mcp/linear/oauth/start", method: "POST" });
  expect(handle.location.href).toBe(AUTHORIZE);
  expect(handle.opener).toBeNull();
  expect(setters[OAUTH]).toHaveBeenLastCalledWith({ url: AUTHORIZE, opened: true, pasted: "", error: "" });
});

test("a blocked window falls back to a link the user taps", async () => {
  openReturns = () => null;
  fetchReply = () => new Response(JSON.stringify({ authorize_url: AUTHORIZE }), { status: 200 });

  await button(render(serverIn()), "Connect").props.onClick();
  const state = setters[OAUTH].mock.calls.at(-1)[0];
  expect(state).toEqual({ url: AUTHORIZE, opened: false, pasted: "", error: "" });

  const tree = render(serverIn(), state);
  const link = all(tree, (n) => n.type === "a")[0];
  expect(link.props).toMatchObject({ href: AUTHORIZE, target: "_blank", rel: "noopener noreferrer" });
  expect(text(link)).toBe("Open sign-in page");
});

test("when the window opened there is no extra link, only the paste field", () => {
  const tree = render(serverIn(), { url: AUTHORIZE, opened: true, pasted: "", error: "" });
  expect(all(tree, (n) => n.type === "a")).toHaveLength(0);
  expect(text(all(tree, hasClass("zl-mcp-oauth-hint"))[0])).toBe(
    "Sign in, then paste the address of the page that doesn’t load."
  );
  // zl-input carries the 16px font floor, so iOS does not zoom on focus.
  const input = all(tree, (n) => n.type === "input")[0];
  expect(input.props.class).toBe("zl-input");
  expect(button(tree, "Finish").props.disabled).toBe(true);
});

test("a failed start closes the blank window and says why inline", async () => {
  const handle = { closed: false, opener: {}, location: { href: "about:blank" }, close: mock(() => {}) };
  openReturns = () => handle;
  fetchReply = () => new Response("could not start authorization: 503\n", { status: 502 });

  await button(render(serverIn()), "Connect").props.onClick();
  expect(handle.close).toHaveBeenCalled();
  expect(setters[OAUTH]).toHaveBeenLastCalledWith({
    url: "",
    opened: false,
    pasted: "",
    error: "could not start authorization: 503",
  });
});

test("Finish sends the pasted address and refreshes the page", async () => {
  fetchReply = (path) =>
    path.endsWith("/oauth/finish")
      ? new Response(JSON.stringify(serverIn({ state: "ready", auth_action: undefined })), { status: 200 })
      : new Response(JSON.stringify({ servers: [] }), { status: 200 });

  const tree = render(serverIn(), { url: AUTHORIZE, opened: true, pasted: `  ${CALLBACK} `, error: "" });
  expect(button(tree, "Finish").props.disabled).toBe(false);
  const form = all(tree, (n) => n.type === "form")[0];
  const preventDefault = mock(() => {});
  await form.props.onSubmit({ preventDefault });

  expect(preventDefault).toHaveBeenCalled();
  const finish = fetchCalls.find((c) => c.path === "/api/sessions/s1/mcp/linear/oauth/finish");
  expect(finish.method).toBe("POST");
  expect(JSON.parse(finish.body)).toEqual({ url: CALLBACK });
  expect(setters[OAUTH]).toHaveBeenLastCalledWith(null);
  expect(fetchCalls.at(-1)).toMatchObject({ path: "/api/sessions/s1/mcp", method: "GET" });
});

test("a finish that still needs sign-in asks to press the button again", async () => {
  fetchReply = (path) =>
    path.endsWith("/oauth/finish")
      ? new Response(JSON.stringify(serverIn({ auth_action: "reconnect" })), { status: 200 })
      : new Response(JSON.stringify({ servers: [] }), { status: 200 });

  const tree = render(serverIn(), { url: AUTHORIZE, opened: true, pasted: CALLBACK, error: "" });
  await all(tree, (n) => n.type === "form")[0].props.onSubmit({ preventDefault() {} });
  expect(setters[OAUTH]).toHaveBeenLastCalledWith({
    url: "",
    opened: false,
    pasted: "",
    error: "Sign-in didn’t work. Press Reconnect again.",
  });
});

test("a rejected paste shows the server's message inline and keeps the field", async () => {
  fetchReply = () =>
    new Response("That link doesn't match this sign-in. Start again.\n", { status: 400 });
  const state = { url: AUTHORIZE, opened: true, pasted: "https://wrong.example/", error: "" };

  const tree = render(serverIn(), state);
  await all(tree, (n) => n.type === "form")[0].props.onSubmit({ preventDefault() {} });

  const update = setters[OAUTH].mock.calls.at(-1)[0];
  expect(update(state)).toEqual({
    ...state,
    error: "That link doesn't match this sign-in. Start again.",
  });
  const shown = render(serverIn(), update(state));
  expect(text(all(shown, hasClass("zl-mcp-oauth-err"))[0])).toBe(
    "That link doesn't match this sign-in. Start again."
  );
  expect(all(shown, (n) => n.type === "input")).toHaveLength(1);
});

test("the catalogue opens a server waiting for sign-in and never goes to the network", async () => {
  const tree = renderInline([
    { name: "github", tools: 12, state: "ready" },
    serverIn(),
  ]);
  // Initial expansion picks the server that needs you.
  const head = all(tree, (n) => n.type === "button" && n.props["aria-expanded"] === true);
  expect(head).toHaveLength(1);
  expect(text(head[0])).toContain("linear");

  await button(tree, "Connect").props.onClick();
  expect(opened).toHaveLength(0);
  expect(fetchCalls).toHaveLength(0);
});

test("start and finish use the OAuth timeout, not the default one", async () => {
  const seen = [];
  const fakeApi = async (method, path, body, opts) => {
    seen.push({ method, path, body, opts });
    return path.endsWith("/start") ? { authorize_url: AUTHORIZE } : { state: "ready" };
  };
  await flow.startConnect(fakeApi, "s1", "a b", null);
  await flow.finishConnect(fakeApi, "s1", "a b", ` ${CALLBACK} `);
  expect(seen[0]).toMatchObject({ method: "POST", path: "/api/sessions/s1/mcp/a%20b/oauth/start" });
  expect(seen[1]).toMatchObject({ method: "POST", path: "/api/sessions/s1/mcp/a%20b/oauth/finish", body: { url: CALLBACK } });
  for (const call of seen) expect(call.opts.timeoutMs).toBe(MCP_OAUTH_TIMEOUT_MS);
});

test("routeAuthorizeWindow refuses a closed or missing window", () => {
  expect(flow.routeAuthorizeWindow(null, AUTHORIZE)).toBe(false);
  expect(flow.routeAuthorizeWindow({ closed: true, location: {} }, AUTHORIZE)).toBe(false);
});
