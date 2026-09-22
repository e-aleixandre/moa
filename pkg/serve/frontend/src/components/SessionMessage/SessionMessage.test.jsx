import { expect, mock, test } from "bun:test";

const css = await Bun.file(new URL("./SessionMessage.css", import.meta.url)).text();

// Same reason as UserWaypoint.test.jsx: these render the component as a plain
// function, so the hooks it uses are declared here rather than borrowed from
// another file's process-wide mock. useState is frozen at its initial value,
// which is the shut state — opening is exercised in the browser.
const realHooks = await import("preact/hooks");
mock.module("preact/hooks", () => ({
  ...realHooks,
  useState(initial) { return [typeof initial === "function" ? initial() : initial, () => {}]; },
  useEffect() {},
  useLayoutEffect() {},
  useRef(initial) { return { current: initial }; },
  useCallback(callback) { return callback; },
  useMemo(factory) { return factory(); },
}));

// The body is markdown, and DOMPurify needs a DOM this harness has no reason
// to build: what is under test is which parts render, not how they render.
// Spread the real module: mock.module replaces it for the WHOLE process, and
// a factory that lists only renderMarkdown deletes renderMarkdownWithCaret for
// every file loaded afterwards.
const realMarkdown = await import("../../data/util/markdown.js");
mock.module("../../data/util/markdown.js", () => ({
  ...realMarkdown,
  renderMarkdown: (text) => `<p>${text}</p>`,
}));

const { SessionMessage } = await import("./SessionMessage.jsx");

// The target is its own component now (SessionChip, shared with the report
// block), so a child whose type is a function is rendered in place: the head
// is still what is under test, whoever draws its parts.
const expand = (n) => (n && typeof n.type === "function" ? n.type(n.props) : n);

function descendants(node) {
  const out = [];
  const walk = (n) => {
    n = expand(n);
    if (!n || typeof n !== "object") return;
    if (Array.isArray(n)) return n.forEach(walk);
    out.push(n);
    walk(n.props?.children);
  };
  walk(node);
  return out;
}

function textContent(node) {
  let text = "";
  const walk = (n) => {
    n = expand(n);
    if (n == null || typeof n === "boolean") return;
    if (Array.isArray(n)) return n.forEach(walk);
    if (typeof n === "object") return walk(n.props?.children);
    text += ` ${n}`;
  };
  walk(node);
  return text;
}

function byClass(node, name) {
  return descendants(node).find((n) => String(n.props?.class || "").split(/\s+/).includes(name));
}

const LONG = `Trabajas en \`/home/x/dev/moa/main\`, rama \`main\`. NO mergear.\n\nEncargo: **las notificaciones en la app instalada**, que no llegan.\n\n${"detalle ".repeat(40)}`;

// What the owner likes about this block is its head. Folding must not cost him
// the verb, the session, the folder, the model or the thinking.
test("folded, the head survives whole and only the message waits", () => {
  const block = SessionMessage({
    action: "new",
    sessionId: "abc123456789",
    title: "Las notificaciones no funcionan",
    cwd: "/home/x/dev/moa/main",
    model: "terra",
    thinking: "medium",
    text: LONG,
  });
  const text = textContent(block);

  expect(text).toContain("started");
  expect(text).toContain("Las notificaciones no funcionan");
  expect(text).toContain("/home/x/dev/moa/main · terra · medium");
  expect(byClass(block, "smsg-disc").props["aria-expanded"]).toBe(false);
  expect(text).toContain("Encargo: las notificaciones en la app instalada, que no llegan.");
  expect(byClass(block, "smsg-body")).toBeUndefined();
});

test("a short message keeps no disclosure: there is nothing to hide", () => {
  const block = SessionMessage({ action: "send", sessionId: "abc", text: "Pushea la rama." });

  expect(byClass(block, "smsg-disc")).toBeUndefined();
  expect(byClass(block, "smsg-body")).toBeDefined();
});

// The answers to an ask travel with the message they answer.
// An `answer` is projected with NO text: only the answers and the question id
// (stream-model.js). Summarising that as "The message" named an outbound
// message that does not exist.
test("a long answer folds under what it actually holds", () => {
  const block = SessionMessage({
    action: "answer",
    sessionId: "abc",
    askId: "ask_1",
    text: "",
    answers: ["Sí, adelante. ".repeat(30), "Y lo segundo también"],
  });

  expect(byClass(block, "smsg-disc")).toBeDefined();
  expect(byClass(block, "smsg-answers")).toBeUndefined();
  expect(textContent(block)).toContain("2 answers");
  expect(textContent(block)).toContain("ask_1");
});

test("a short answer is not worth a tap", () => {
  const block = SessionMessage({ action: "answer", sessionId: "abc", text: "", answers: ["Sí"] });

  expect(byClass(block, "smsg-disc")).toBeUndefined();
  expect(byClass(block, "smsg-answers")).toBeDefined();
});

test("the fold borrows the transcript's disclosure grammar", () => {
  expect(css).toMatch(/\.smsg-disc\[aria-expanded="true"\] \.smsg-chev/);
  expect(css).toMatch(/prefers-reduced-motion[\s\S]*\.smsg-chev \{ transition: none; \}/);
  expect(css).toMatch(/min-height: var\(--control-touch\)/);
});
