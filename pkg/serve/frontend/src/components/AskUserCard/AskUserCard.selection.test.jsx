import { expect, mock, test } from "bun:test";

// Same leak as UserWaypoint.test.jsx: this file calls AskUserCard as a plain
// function outside any render, and the component uses hooks. It only ever
// worked because another file's process-wide mock.module("preact/hooks")
// happened to load first. It declares its own now.
// Spread the real hooks first. bun's mock.module replaces the module for the
// whole process and never restores it, so a factory that lists only some hooks
// deletes the rest for every file loaded afterwards -- that is the
// "Export named 'useMemo' not found" that appears only when these files run
// beside one another.
const realHooks = await import("preact/hooks");
mock.module("preact/hooks", () => ({
  ...realHooks,
  useState(initial) { return [typeof initial === "function" ? initial() : initial, () => {}]; },
  useEffect() {},
  useLayoutEffect() {},
  useRef(initial) { return { current: initial }; },
  useCallback(callback) { return callback; },
  useMemo(factory) { return factory(); },
  useContext(context) { return context?._defaultValue; },
  useReducer(reducer, initial) { return [initial, () => {}]; },
  useErrorBoundary() { return [undefined, () => {}]; },
  useId() { return "test-id"; },
  useDebugValue() {},
  useImperativeHandle() {},
}));

const { AskUserCard } = await import("./AskUserCard.jsx");

function descendants(node, result = []) {
  if (node == null || typeof node !== "object") return result;
  if (Array.isArray(node)) {
    node.forEach((child) => descendants(child, result));
    return result;
  }
  result.push(node);
  descendants(node.props?.children, result);
  return result;
}

function optionsFor(currentAnswer) {
  const tree = AskUserCard({
    question: "Continue?",
    options: [{ label: "Yes", recommended: true }, { label: "No" }],
    currentAnswer,
  });
  return descendants(tree).filter((node) => node.type === "button" && node.props?.class?.startsWith("ask-opt"));
}

test("options start unselected and exactly the current option is visibly and accessibly selected", () => {
  const initial = optionsFor("");
  expect(initial.map((option) => option.props.class)).toEqual(["ask-opt", "ask-opt"]);
  expect(initial.map((option) => option.props["aria-pressed"])).toEqual([false, false]);

  const selected = optionsFor("Yes");
  expect(selected.map((option) => option.props.class)).toEqual(["ask-opt chosen", "ask-opt"]);
  expect(selected.map((option) => option.props["aria-pressed"])).toEqual([true, false]);
  const selectedCheck = descendants(selected[0]).find((node) => node.props?.class === "ask-opt-check");
  const unselectedCheck = descendants(selected[1]).find((node) => node.props?.class === "ask-opt-check");
  expect(selectedCheck.props.children).toBeTruthy();
  expect(unselectedCheck.props.children).toBe(false);
});

test("free text does not leave a predefined option selected", () => {
  const options = optionsFor("Something else");
  expect(options.map((option) => option.props.class)).toEqual(["ask-opt", "ask-opt"]);
  expect(options.map((option) => option.props["aria-pressed"])).toEqual([false, false]);
});
