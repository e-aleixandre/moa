import { test, expect, mock, beforeEach, afterEach } from "bun:test";

// State lives in slots by call order, setters only write (the test re-renders).
// Child components are not expanded, so only the one under test owns slots.
const { hooks, runtime } = await import("./hook-runtime.js");
mock.module("preact/hooks", () => hooks);
let slots = [];
let idx = 0;
const useSlots = () => {
  runtime.useState = (initial) => {
    const i = idx++;
    if (!(i in slots)) slots[i] = typeof initial === "function" ? initial() : initial;
    return [slots[i], (v) => { slots[i] = typeof v === "function" ? v(slots[i]) : v; }];
  };
  runtime.useRef = (initial) => {
    const i = idx++;
    if (!(i in slots)) slots[i] = { current: initial };
    return slots[i];
  };
};

// Creation goes through fetch: record the POSTs, then fail them.
const created = [];
const realFetch = globalThis.fetch;
beforeEach(() => {
  useSlots();
  globalThis.fetch = (path, opts) => {
    if (opts?.method === "POST" && String(path).startsWith("/api/tasks")) created.push(JSON.parse(opts.body));
    return Promise.reject(new Error("offline"));
  };
});
afterEach(() => { globalThis.fetch = realFetch; });

const { TaskDetail } = await import("./TaskDetail.jsx");
const { SchedDetail, WhenEditor } = await import("./Scheduled.jsx");
const { SendLater } = await import("./SendLater.jsx");
const { store, setState, TASKS_INITIAL } = await import("../../data/store.js");
const { whenMountQuestion } = await import("../../data/schedule-model.js");

function flat(node, out = []) {
  if (node == null || typeof node !== "object") return out;
  if (Array.isArray(node)) { node.forEach((n) => flat(n, out)); return out; }
  out.push(node);
  flat(node.props?.children, out);
  return out;
}
function text(node) {
  if (node == null || node === false || node === true) return "";
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (Array.isArray(node)) return node.map(text).join("");
  return text(node.props?.children);
}

function mount(Comp, props) {
  slots = [];
  useSlots();
  const h = {
    get tree() { idx = 0; return Comp(props); },
    all: () => flat(h.tree),
    btn: (label) => h.all().find((n) => n.type === "button" && text(n).trim().startsWith(label)),
    byAria: (label) => h.all().find((n) => n.props?.["aria-label"] === label),
    editor: () => h.all().find((n) => n.type === WhenEditor),
    whenBtn: () => h.all().find((n) => n.type === "button" && String(n.props.class || "").includes("tk-prop-btn")),
  };
  // Hooks' slots survive between renders of the same harness.
  const keep = slots;
  return new Proxy(h, { get: (t, k) => { slots = keep; return t[k]; } });
}
const ctrlEnter = { key: "Enter", ctrlKey: true, metaKey: false, altKey: false, shiftKey: false, preventDefault() {} };
// preact/compat, when another test file loaded it, lower-cases onInput.
const typeTitle = (h, value) => { const { props } = h.byAria("Title"); (props.onInput || props.oninput)({ currentTarget: { value, style: {}, scrollHeight: 10 } }); };

const LIVE = { live: { id: "live", title: "Live one", state: "idle", cwd: "/x/moa/main" } };
function withStore(extra, fn) {
  const previous = store.get();
  setState({ tasks: { ...TASKS_INITIAL, ...extra }, sessions: LIVE });
  created.length = 0;
  try { return fn(); } finally { setState(previous); }
}
const ONCE = (h) => ({ kind: "once", at: Date.UTC(2026, 9, 1, h) });

test("a When that is started but unresolved never creates an ordinary task", () => withStore({}, () => {
  const h = mount(TaskDetail, { isNew: true, newDest: { place: "agent", sessionId: "live" } });
  typeTitle(h, "Fix it");
  const primary = () => h.btn("Assign and notify");
  expect(primary().props.disabled).toBe(false);
  h.btn("Not scheduled").props.onClick();
  // The user typed words: the server has not resolved them (pending or invalid).
  h.editor().props.onChange(null, "garbage at 99:90", null);
  expect(primary().props.disabled).toBe(true);
  primary().props.onClick();
  h.byAria("Title").props.onKeyDown(ctrlEnter);
  expect(created).toEqual([]);
  // Closing the popover does not forget the intent.
  h.btn("Pick a time").props.onClick();
  expect(primary().props.disabled).toBe(true);
  h.byAria("Title").props.onKeyDown(ctrlEnter);
  expect(created).toEqual([]);
  // Removing the When on purpose returns to ordinary creation.
  h.btn("Remove When").props.onClick();
  expect(primary().props.disabled).toBe(false);
  h.byAria("Title").props.onKeyDown(ctrlEnter);
  expect(created).toHaveLength(1);
  expect(created[0].when).toBeUndefined();
}));

test("an open When with nothing typed still creates an ordinary task", () => withStore({}, () => {
  const h = mount(TaskDetail, { isNew: true, newDest: { place: "you" } });
  typeTitle(h, "Plain");
  h.btn("Not scheduled").props.onClick();
  h.byAria("Title").props.onKeyDown(ctrlEnter);
  expect(created).toHaveLength(1);
}));

test("Send later stays blocked while its When is unresolved", () => {
  const cleared = [];
  const h = mount(SendLater, { sessionId: "live", text: "do it", phone: true, onClose() {}, onScheduled: (t) => cleared.push(t) });
  h.editor().props.onChange(null, "soonish", null);
  expect(h.btn("Schedule").props.disabled).toBe(true);
  created.length = 0;
  h.all().find((n) => n.props?.onKeyDown && String(n.props.class || "").includes("sch-later-body")).props.onKeyDown(ctrlEnter);
  expect(created).toEqual([]);
});

test("a new scheduled task is not created while its When is unresolved", () => withStore({}, () => {
  const h = mount(SchedDetail, { isNew: true, init: { draft: { title: "T", when: ONCE(9), target: { kind: "session", id: "live" } }, pop: "when", whenText: "tomorrow 9" } });
  h.editor().props.onChange(null, "tomorrow at 99", null);
  expect(h.btn("Schedule").props.disabled).toBe(true);
  h.byAria("Title").props.onKeyDown?.(ctrlEnter);
  expect(created).toEqual([]);
}));

test("Discard closes When, so a late parse cannot bring Save back", () => {
  const task = {
    id: 5, title: "Daily", place: "you", status: "pending", revision: 2, tz: "Europe/Madrid",
    when: { kind: "repeat", rule: { freq: "daily", h: 2, mi: 30 } }, target: { kind: "session", id: "live" },
    delivery: { mode: "steer" }, schedule_state: "scheduled", next: Date.UTC(2026, 9, 1, 0, 30), created_at: 1,
  };
  withStore({ details: { 5: task } }, () => {
    const h = mount(SchedDetail, { taskId: 5 });
    h.whenBtn().props.onClick();
    expect(h.editor()).toBeDefined();
    h.editor().props.onChange({ kind: "repeat", rule: { freq: "daily", h: 10, mi: 0 } }, "daily 10", Date.UTC(2026, 9, 1, 8));
    h.btn("Discard").props.onClick();
    expect(h.editor()).toBeUndefined();
    expect(h.btn("Save")).toBeUndefined();
  });
});

test("reopening When shows the current text, not the first one", () => withStore({}, () => {
  const h = mount(SchedDetail, { isNew: true, init: { draft: { title: "T", when: ONCE(9), target: { kind: "session", id: "live" } }, pop: "when", whenText: "9am" } });
  expect(h.editor().props.text).toBe("9am");
  h.editor().props.onChange(ONCE(10), "10am", ONCE(10).at);
  const whenBtn = { props: { onClick: () => h.whenBtn().props.onClick() } };
  whenBtn.props.onClick(); // close
  expect(h.editor()).toBeUndefined();
  whenBtn.props.onClick(); // reopen
  expect(h.editor().props.text).toBe("10am");
}));

test("the editor asks only for text that has no resolved value yet", () => {
  expect(whenMountQuestion("9am", ONCE(9))).toBe(null);
  expect(whenMountQuestion("9am", null)).toBe("9am");
  expect(whenMountQuestion("", null)).toBe(null);
});
