import { afterEach, expect, mock, test } from "bun:test";

// Rendered as a plain function, so the hooks useStore relies on are declared
// here: the selector then reads the store as it is right now.
const realHooks = await import("preact/hooks");
mock.module("preact/hooks", () => ({
  ...realHooks,
  useState(initial) { return [typeof initial === "function" ? initial() : initial, () => {}]; },
  useEffect() {},
  useLayoutEffect() {},
  useRef(initial) { return { current: initial }; },
}));

const { setState, store } = await import("../../data/store.js");
const { SessionMention } = await import("./SessionMention.jsx");
const { SessionChip } = await import("./SessionChip.jsx");

const ID = "5d18914e4e62ac2206e1d77f";
const initialSessions = store.get().sessions;
afterEach(() => setState({ sessions: initialSessions }));

test("a mentioned session this client knows becomes its chip", () => {
  setState({ sessions: { [ID]: { id: ID, title: "Lenguaje de estado", state: "idle" } } });
  const onOpen = () => {};
  const out = SessionMention({ sessionId: ID, onOpen });
  expect(out.type).toBe(SessionChip);
  expect(out.props).toEqual({ sessionId: ID, onOpen });
  const chip = SessionChip(out.props);
  expect(chip.props.disabled).toBe(false);
});

test("a mentioned id this client does not know stays inline code", () => {
  setState({ sessions: {} });
  const out = SessionMention({ sessionId: ID, onOpen: () => {} });
  expect(out.type).toBe("code");
  expect(out.props.children).toBe(ID);
});
