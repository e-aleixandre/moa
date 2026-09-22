import { test, expect, beforeEach } from "bun:test";
import { store, setState, updateSession } from "../data/store.js";
import { subscribeSelected } from "./useStore.js";

// The hook itself is a subscribe + Object.is. These tests pin that contract
// against the live store, which is what DesktopShell / ConversationScreen /
// panes rely on: a sibling session's stream must not look like a change.

beforeEach(() => {
  setState({ sessions: {}, tileTree: null, activeSession: null, view: null });
});

test('a session-object selector ignores a sibling stream patch', () => {
  setState({
    sessions: {
      a: { id: "a", streamingText: null },
      b: { id: "b", streamingText: null },
    },
  });
  const selectA = (s) => s.sessions.a;
  let current = selectA(store.get());
  let n = 0;
  const unsub = store.subscribe(() => {
    const next = selectA(store.get());
    if (Object.is(current, next)) return;
    current = next;
    n++;
  });
  updateSession("b", { streamingText: "hello" });
  expect(n).toBe(0);
  expect(store.get().sessions.a).toBe(current);
  updateSession("a", { streamingText: "hello" });
  expect(n).toBe(1);
  unsub();
});

test('a primitive selector ignores unrelated keys', () => {
  setState({ view: "grid", isMobile: false });
  const selectView = (s) => s.view;
  let current = selectView(store.get());
  let n = 0;
  const unsub = store.subscribe(() => {
    const next = selectView(store.get());
    if (Object.is(current, next)) return;
    current = next;
    n++;
  });
  setState({ isMobile: true });
  expect(n).toBe(0);
  setState({ view: "conversation" });
  expect(n).toBe(1);
  unsub();
});

// The render reads the value, the effect subscribes later. A change landing in
// between (the first roster on a cold start) must not wait for the next one.
test('a change between the render and the subscription is not lost', () => {
  setState({ sessionsLoaded: false });
  const selectorRef = { current: (s) => s.sessionsLoaded };
  const valueRef = { current: selectorRef.current(store.get()) }; // the render
  setState({ sessionsLoaded: true }); // lands before the effect runs
  let renders = 0;
  const unsub = subscribeSelected(selectorRef, valueRef, () => renders++);
  expect(renders).toBe(1);
  expect(valueRef.current).toBe(true);
  setState({ view: "grid" });
  expect(renders).toBe(1);
  unsub();
});
