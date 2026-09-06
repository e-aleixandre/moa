import { test, expect, mock } from "bun:test";

// The sheet is walked as a plain vnode tree (no DOM), so its hooks are stubbed:
// `pick` decides what each useState returns, in call order, which is how a test
// can render the routing step and the model step of the same sheet. Only the
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

// The models the sheet would fetch. useEventCreateModels' effect never runs
// under the stub, so the specs are injected through its useState defaults.
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

// expand renders component vnodes in place so the assertions reach real
// markup; the components themselves are collected first, so a test can also
// assert on the props a shipped primitive (ModelSelector) was given.
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

function byClass(nodes, className) {
  return nodes.filter((node) => typeof node.props?.class === "string" && node.props.class.split(" ").includes(className));
}

// render — the inbox with its routing sheet open on the one pending card.
// `states` maps a useState index to the value it should return; everything else
// falls through to the component's own initial value.
function render(states, props = {}) {
  calls = 0;
  setters.length = 0;
  mounted.length = 0;
  pick = (index) => states[index];
  return descendants(expand(InboxView({ cards: [CARD], ...props })));
}

// The useState call order under the vnode walk:
//   0 filter · 1 routing · 2 models · 3 defaultModel · 4 step · 5 override
const OPEN = { 1: EVENT.id, 2: SPECS, 3: "openai/terra" };
const MODEL_STEP = { ...OPEN, 4: "model" };

test("the routing sheet names the model its create action would use", () => {
  const nodes = render(OPEN);
  const [action] = byClass(nodes, "inbox-sheet-item");
  expect(text(action)).toBe("New session · Terra low");
});

// The bug this replaces: ModelSelector's onSelect called onCreate, so picking a
// model created the session and closed the sheet in the same tap. Choosing is a
// step, not a decision — like the palette's model step, it only returns.
test("choosing a model returns to the routing step instead of creating the session", () => {
  const onNewSession = mock(() => {});
  const nodes = render(MODEL_STEP, { onNewSession });
  expect(nodes.length).toBeGreaterThan(0);
  const selector = mounted.find((node) => node.type.name === "ModelSelector");
  expect(selector).toBeTruthy();

  selector.props.onSelect("anthropic/opus");
  expect(onNewSession).not.toHaveBeenCalled();
  // useState call order in the sheet: filter, routing, models, defaultModel,
  // step, override. The pick writes the override and returns to "route" —
  // nothing is sent to the server.
  expect(setters[5].mock.calls).toHaveLength(1);
  expect(setters[4]).toHaveBeenCalledWith("route");
});

// The choice is local to the open sheet: it changes what the create action
// SAYS and what it would send, and nothing else.
test("an overridden model is what the create action names and sends", () => {
  const onNewSession = mock(() => {});
  const nodes = render({ ...OPEN, 5: { model: "anthropic/opus", thinking: "high" } }, { onNewSession });
  const [action] = byClass(nodes, "inbox-sheet-item");
  expect(text(action)).toBe("New session · Opus high");

  action.props.onClick();
  expect(onNewSession).toHaveBeenCalledWith(EVENT.id, { model: "anthropic/opus", thinking: "high" });
});

// The candidates are the shipped SessionRow, not a shape invented for the
// sheet — a session has to look like a session wherever it is offered.
test("the sheet offers open sessions as session rows and sends the event to one", () => {
  const onSend = mock(() => {});
  const nodes = render(OPEN, { onSend });
  const rows = mounted.filter((node) => node.type.name === "SessionRow");
  expect(rows.length).toBe(1);
  const hit = nodes.find((node) => node.props?.class === "session-row-hit");
  hit.props.onClick();
  expect(onSend).toHaveBeenCalledWith(EVENT.id, "s1");
});

// Every candidate of a project event lives in that project, so repeating the
// path under each row would say the same thing N times.
test("candidates of a project event drop the path the sheet already states", () => {
  const nodes = render(OPEN);
  expect(byClass(nodes, "path")).toHaveLength(0);

  const projectless = { ...CARD, event: { ...EVENT, project: "" } };
  calls = 0;
  mounted.length = 0;
  pick = (index) => OPEN[index];
  const spanning = descendants(expand(InboxView({ cards: [projectless] })));
  expect(byClass(spanning, "path")).toHaveLength(1);
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
  const closed = render({ 0: "all" }, { cards: [card] });
  const [row] = byClass(closed, "inbox-row");
  row.props.onClick();
  expect(setters[1]).toHaveBeenCalledWith(EVENT.id);

  const open = render({ 0: "all", 1: EVENT.id }, { cards: [card] });
  expect(text(open)).toContain("Destination unavailable");
  expect(text(open)).toContain("Arrived 6m ago");
  expect(text(open)).toContain(EVENT.body);
});

test("an ignored event opens an ignored read-only detail", () => {
  const card = settledCard("dismissed");
  const open = render({ 0: "all", 1: EVENT.id }, { cards: [card] });
  expect(text(open)).toContain("Ignored");
  expect(text(open)).toContain("This event was ignored");
});

test("a delivering event says delivering in its row and detail", () => {
  const card = settledCard("routing");
  const closed = render({ 0: "all" }, { cards: [card] });
  expect(text(closed)).toContain("delivering");
  expect(text(closed)).not.toContain("ignored");

  const open = render({ 0: "all", 1: EVENT.id }, { cards: [card] });
  expect(text(open)).toContain("Delivering event");
  expect(text(open)).toContain("Delivery is in progress");
});
