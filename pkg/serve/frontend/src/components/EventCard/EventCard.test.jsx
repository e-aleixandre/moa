/* wake-on-event — EventCard actions. */
import { expect, test } from "bun:test";
import { EventCard } from "./EventCard.jsx";

function descendants(node, nodes = []) {
  if (node == null || typeof node === "string") return nodes;
  nodes.push(node);
  const children = node.props?.children;
  for (const child of Array.isArray(children) ? children : [children]) descendants(child, nodes);
  return nodes;
}

function byClass(node, className) {
  return descendants(node).find((child) => child.props?.class === className);
}

function textContent(node) {
  if (node == null || node === false) return "";
  if (typeof node === "string" || typeof node === "number") return String(node);
  const children = node.props?.children;
  return (Array.isArray(children) ? children : [children]).map(textContent).join("");
}

const pending = {
  id: "ev_1",
  source: "agentmail",
  project: "/home/u/dev/moa/main",
  title: "Re: design",
  body: "looks good",
  suggested: "sess-1",
};

test("the primary action sends the event to the suggested session", () => {
  const calls = [];
  const card = EventCard({
    event: pending,
    suggestedTitle: "wake-on-event design",
    onRoute: (id, sessionId) => calls.push([id, sessionId]),
  });
  const go = byClass(card, "evcard-go");

  expect(textContent(go)).toBe("Send to wake-on-event design");
  go.props.onClick();
  expect(calls).toEqual([["ev_1", "sess-1"]]);
});

test("without a suggestion the primary action creates a session", () => {
  const created = [];
  const card = EventCard({
    event: { ...pending, suggested: "" },
    onNew: (id) => created.push(id),
  });
  const go = byClass(card, "evcard-go");

  expect(textContent(go)).toContain("New session");
  expect(textContent(go)).toContain("No session open in this project");
  // With no session to send to there is no second "new session" button: the
  // primary action already is it.
  expect(byClass(card, "evcard-new")).toBeUndefined();
  go.props.onClick();
  expect(created).toEqual(["ev_1"]);
});

test("new session and dismiss are separate terminal actions", () => {
  const calls = [];
  const card = EventCard({
    event: pending,
    suggestedTitle: "wake-on-event design",
    onNew: (id) => calls.push(["new", id]),
    onDismiss: (id) => calls.push(["dismiss", id]),
  });

  byClass(card, "evcard-new").props.onClick();
  byClass(card, "evcard-dismiss").props.onClick();
  expect(calls).toEqual([["new", "ev_1"], ["dismiss", "ev_1"]]);
});

test("every action is disabled while one is in flight", () => {
  const card = EventCard({ event: pending, suggestedTitle: "s", busy: true });
  for (const cls of ["evcard-go", "evcard-new", "evcard-dismiss"]) {
    expect(byClass(card, cls).props.disabled).toBe(true);
  }
});

test("the card shows the source and a shortened project path", () => {
  const card = EventCard({ event: pending, suggestedTitle: "s" });
  expect(textContent(byClass(card, "evcard-source"))).toBe("agentmail");
  expect(textContent(byClass(card, "evcard-proj"))).toBe("…/moa/main");
  expect(textContent(byClass(card, "evcard-title"))).toBe("Re: design");
});
