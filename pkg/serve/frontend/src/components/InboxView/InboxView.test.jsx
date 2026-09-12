import { test, expect, mock } from "bun:test";

// The sheet is walked as a plain vnode tree (no DOM), so its hooks are stubbed:
// `pick` decides what each useState returns, in call order, which is how a test
// can render the routing step and the model step of the same decision. Only the
// hooks this component uses are replaced — the rest of preact/hooks is kept, so
// anything else importing the module still gets a complete one.
const realHooks = await import("preact/hooks");
let pick = () => undefined;
let calls = 0;
const setters = [];
mock.module("preact/hooks", () => ({
  ...realHooks,
  useState(initial) {
    const index = calls++;
    const chosen = pick(index, initial);
    const setter = mock(() => {});
    setters.push(setter);
    return [chosen === undefined ? (typeof initial === "function" ? initial() : initial) : chosen, setter];
  },
  useEffect() {},
  useLayoutEffect() {},
  useRef(initial) { return { current: initial }; },
  useCallback(callback) { return callback; },
  useMemo(factory) { return factory(); },
}));

const SPECS = [
  { id: "openai/terra", catalogId: "terra", codename: "Terra", provider: "openai", accent: "green" },
  { id: "anthropic/opus", catalogId: "opus", codename: "Opus", provider: "anthropic", accent: "peach" },
];

const { InboxView } = await import("./InboxView.jsx");

const EVENT = {
  id: "ev_1",
  source: "sentry-tienda",
  project: "/home/x/dev/moa/main",
  title: "TypeError in OrderSummary",
  state: "new",
  pending_reason: "many_sessions",
  create_model: "openai/terra",
  create_thinking: "low",
  body: '{"level":"error"}',
};

const CARD = {
  event: EVENT,
  age: "6m",
  pending: true,
  sessions: [{ id: "s1", title: "ws race fix", state: "idle", when: "1m", path: "/home/x/dev/moa/main" }],
  project: "moa/main",
  projectLabel: "moa/main",
  projectName: "main",
  routedToTitle: "",
};

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

const mounted = [];
function expand(node, depth = 0) {
  if (node == null || typeof node !== "object" || depth > 10) return node;
  if (Array.isArray(node)) return node.map((child) => expand(child, depth));
  if (typeof node.type === "function") {
    mounted.push(node);
    return expand(node.type(node.props), depth + 1);
  }
  return { ...node, props: { ...node.props, children: expand(node.props?.children, depth) } };
}

function text(node) {
  if (node == null || node === false) return "";
  if (typeof node === "string") return node;
  if (Array.isArray(node)) return node.map(text).join("");
  if (typeof node !== "object") return String(node);
  return text(node.props?.children);
}

function classesOf(node) {
  return typeof node.props?.class === "string" ? node.props.class.split(" ") : [];
}

function byClass(nodes, className) {
  return nodes.filter((node) => classesOf(node).includes(className));
}

function byClasses(nodes, ...need) {
  return nodes.filter((node) => need.every((c) => classesOf(node).includes(c)));
}

// useState call order under the vnode walk:
//   0 selected · 1 step · 2 override · 3 models · 4 defaultModel
function render(states, props = {}) {
  calls = 0;
  setters.length = 0;
  mounted.length = 0;
  pick = (index) => states[index];
  return descendants(expand(InboxView({ cards: [CARD], ...props })));
}

const OPEN = { 0: EVENT.id, 3: SPECS, 4: "openai/terra" };
const MODEL_STEP = { ...OPEN, 1: "model" };

test("the routing step names the model its create action would use", () => {
  const nodes = render(OPEN);
  const [action] = byClasses(nodes, "zi-dest", "is-new");
  expect(text(action)).toContain("New session");
  expect(text(action)).toContain("Terra · low");
});

test("choosing a model returns to the routing step instead of creating the session", () => {
  const onNewSession = mock(() => {});
  const nodes = render(MODEL_STEP, { onNewSession });
  expect(nodes.length).toBeGreaterThan(0);
  const pickers = byClasses(nodes, "zi-dest", "is-pick");
  const opus = pickers.find((node) => text(node).includes("Opus"));
  expect(opus).toBeTruthy();

  opus.props.onClick();
  expect(onNewSession).not.toHaveBeenCalled();
  expect(setters[2].mock.calls).toHaveLength(1);
  expect(setters[1]).toHaveBeenCalledWith("route");
});

test("an overridden model is what the create action names and sends", () => {
  const onNewSession = mock(() => {});
  const nodes = render({ ...OPEN, 2: { model: "anthropic/opus", thinking: "high" } }, { onNewSession });
  const [action] = byClasses(nodes, "zi-dest", "is-new");
  expect(text(action)).toContain("Opus · high");

  action.props.onClick();
  expect(onNewSession).toHaveBeenCalledWith(EVENT.id, { model: "anthropic/opus", thinking: "high" });
});

test("the decision offers open sessions as destinations and sends the event to one", () => {
  const onSend = mock(() => {});
  const nodes = render(OPEN, { onSend });
  const dests = byClass(nodes, "zi-dest").filter((node) => !classesOf(node).includes("is-new") && !classesOf(node).includes("is-change") && !classesOf(node).includes("is-pick"));
  expect(dests.length).toBe(1);
  expect(text(dests[0])).toContain("ws race fix");
  dests[0].props.onClick();
  expect(onSend).toHaveBeenCalledWith(EVENT.id, "s1");
});

test("candidates of a project event drop the path the decision already states", () => {
  const nodes = render(OPEN);
  const dests = byClass(nodes, "zi-dest").filter((node) => !classesOf(node).includes("is-new") && !classesOf(node).includes("is-change"));
  expect(text(dests[0])).not.toContain("moa/main");

  const projectless = { ...CARD, event: { ...EVENT, project: "" } };
  calls = 0;
  mounted.length = 0;
  pick = (index) => OPEN[index];
  const spanning = descendants(expand(InboxView({ cards: [projectless] })));
  const spanningDests = byClass(spanning, "zi-dest").filter((node) => !classesOf(node).includes("is-new") && !classesOf(node).includes("is-change"));
  expect(text(spanningDests[0])).toContain("moa/main");
});

const SETTLED_CARD = {
  ...CARD,
  pending: false,
  sessions: [],
  routedToAvailable: false,
};

function settledCard(state) {
  return {
    ...SETTLED_CARD,
    event: { ...EVENT, state, routed_to: state === "routed" ? "missing-session" : "" },
  };
}

test("a routed event whose destination is gone opens a read-only detail", () => {
  const card = settledCard("routed");
  const closed = render({}, { cards: [card] });
  const [row] = byClass(closed, "zi-row");
  row.props.onClick();
  expect(setters[0]).toHaveBeenCalledWith(EVENT.id);

  const open = render({ 0: EVENT.id }, { cards: [card] });
  expect(text(open)).toContain("Destination unavailable");
  expect(text(open)).toContain("Arrived 6m ago");
  expect(text(open)).toContain(EVENT.body);
});

test("an ignored event opens an ignored read-only detail", () => {
  const card = settledCard("dismissed");
  const open = render({ 0: EVENT.id }, { cards: [card] });
  expect(text(open)).toContain("Ignored");
  expect(text(open)).toContain("This event was ignored");
});

test("a delivering event says delivering in its row and detail", () => {
  const card = settledCard("routing");
  const closed = render({}, { cards: [card] });
  expect(text(closed)).toContain("Delivering");
  expect(text(closed)).not.toContain("Ignored");

  const open = render({ 0: EVENT.id }, { cards: [card] });
  expect(text(open)).toContain("Delivering");
  expect(text(open)).toContain("Delivery is in progress");
});

test("waiting and settled share one list; there is no pending/all filter", () => {
  const nodes = render({}, { cards: [CARD, settledCard("dismissed")] });
  const body = text(nodes);
  expect(body).toContain("Waiting");
  expect(body).toContain("Settled");
  expect(body).toContain("Ignored");
  expect(body).toContain(EVENT.title);
  expect(body).not.toContain("Pending");
  expect(byClass(nodes, "zi-row")).toHaveLength(2);
});

test("a failed first load says so, and never claims the inbox is empty", () => {
  const nodes = render({}, {
    cards: [],
    health: { status: "error", error: "GET /api/events · 502: upstream is down" },
  });
  const body = text(nodes);
  expect(body).toContain("Can't reach the inbox");
  expect(body).toContain("GET /api/events · 502: upstream is down");
  expect(body).not.toContain("Nothing waiting.");
  expect(body).not.toContain("Nothing has arrived yet.");
});

test("the failed state offers a retry that calls back", () => {
  const onRetry = mock(() => {});
  const nodes = render({}, { cards: [], health: { status: "error", error: "x" }, onRetry });
  const retry = byClass(nodes, "zi-btn").find((node) => text(node) === "Retry");
  expect(retry).toBeTruthy();
  retry.props.onClick();
  expect(onRetry).toHaveBeenCalled();
});

test("a retry in flight says it is retrying instead of offering the same tap twice", () => {
  const nodes = render({}, { cards: [], health: { status: "error", error: "x", retrying: true }, onRetry: () => {} });
  const retry = byClass(nodes, "zi-btn").find((node) => text(node).includes("Retry"));
  expect(retry.props.disabled).toBe(true);
  expect(text(retry)).toBe("Retrying…");
});

test("a first load in flight shows ghosts, not an empty inbox", () => {
  const nodes = render({}, { cards: [], health: { status: "loading" } });
  expect(byClass(nodes, "zi-ghost")).toHaveLength(3);
  const body = text(nodes);
  expect(body).toContain("Loading events");
  expect(body).not.toContain("Nothing waiting.");
});

test("a stale inbox keeps its rows and says since when they stopped being current", () => {
  const nodes = render({}, {
    cards: [CARD],
    health: { status: "stale", error: "GET /api/events · 502", checkedAt: Date.now() - 4 * 60000 },
  });
  const body = text(nodes);
  expect(body).toContain("Not updating");
  expect(body).toContain("last checked 4m ago");
  expect(byClass(nodes, "zi-row")).toHaveLength(1);
  expect(body).toContain(EVENT.title);
});

test("a healthy empty inbox still says nothing has arrived", () => {
  const nodes = render({}, { cards: [], health: { status: "ready" } });
  expect(text(nodes)).toContain("Nothing has arrived yet.");
});
