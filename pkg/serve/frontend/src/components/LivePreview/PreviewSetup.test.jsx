import { test, expect } from "bun:test";
import { PreviewAddressSetup, PreviewErrorBanner, PreviewURLSetup } from "./PreviewSetup.jsx";

const find = (node, predicate) => {
  if (!node || typeof node !== "object") return null;
  if (Array.isArray(node)) {
    for (const child of node) {
      const hit = find(child, predicate);
      if (hit) return hit;
    }
    return null;
  }
  if (predicate(node)) return node;
  return find(node.props?.children, predicate);
};
const byClass = (node, cls) => find(node, (n) => typeof n.props?.class === "string" && n.props.class.split(" ").includes(cls));
const byLabel = (node, label) => find(node, (n) => n.props?.["aria-label"] === label);
const handler = (node, name) => node.props?.[name] || node.props?.[name.toLowerCase()];
const textOf = (node) => {
  if (node == null || node === false) return "";
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (Array.isArray(node)) return node.map(textOf).join("");
  return textOf(node.props?.children);
};

// First run: the app URL, and nothing else. The proxy is not started until
// there is something to point it at.
test("the first screen asks for the app URL and cannot be submitted empty", () => {
  const tree = PreviewURLSetup({ value: "", onInput: () => {}, onCommit: () => {}, onCancel: () => {} });
  expect(textOf(byClass(tree, "live-preview-setup-title"))).toBe("Enter your app URL");
  expect(byLabel(tree, "Preview URL").props.value).toBe("");
  expect(find(tree, (n) => n.props?.disabled === true)).toBeTruthy();
});

// The address the browser uses to reach the proxy is proposed, editable, and
// confirmed with a button — Moa cannot derive it, so it asks once.
test("the address screen shows the suggestion, takes edits and confirms with a button", () => {
  const edits = [];
  let committed = 0;
  const tree = PreviewAddressSetup({
    value: "https://dev.taild072ac.ts.net:7402",
    onInput: (v) => edits.push(v),
    onCommit: () => { committed += 1; },
    onBack: () => {},
  });
  expect(textOf(byClass(tree, "live-preview-setup-title"))).toBe("Confirm the preview address");

  const field = byLabel(tree, "Preview proxy address");
  expect(field.props.value).toBe("https://dev.taild072ac.ts.net:7402");
  handler(field, "onInput")({ currentTarget: { value: "https://dev.taild072ac.ts.net:9000" } });
  expect(edits).toEqual(["https://dev.taild072ac.ts.net:9000"]);

  const start = find(tree, (n) => textOf(n) === "Start" && n.props?.onClick);
  start.props.onClick();
  handler(field, "onKeyDown")({ key: "Enter" });
  expect(committed).toBe(2);
});

// An address that cannot be bound is reported where it is corrected, not as a
// banner over an app that is not being shown.
test("a rejected address is reported next to the field", () => {
  const tree = PreviewAddressSetup({
    value: "https://dev.test:7402",
    onInput: () => {},
    onCommit: () => {},
    onBack: () => {},
    error: "port 7402 is not available for the preview proxy",
  });
  expect(textOf(byClass(tree, "live-preview-setup-error"))).toContain("port 7402 is not available");
});

test("the address screen can go back to the app URL", () => {
  let back = 0;
  const tree = PreviewAddressSetup({ value: "https://dev.test:7402", onInput: () => {}, onCommit: () => {}, onBack: () => { back += 1; }, });
  byClass(tree, "live-preview-setup-back").props.onClick();
  expect(back).toBe(1);
});

// An open socket is not a usable preview: when the frame cannot load through
// the public origin, the fix is one tap from the message.
test("a preview error offers the way to change the address", () => {
  let opened = 0;
  const tree = PreviewErrorBanner({ message: "The preview proxy could not be started.", onChangeAddress: () => { opened += 1; } });
  expect(textOf(tree)).toContain("The preview proxy could not be started.");
  const action = byClass(tree, "live-preview-proxy-error-action");
  expect(textOf(action)).toBe("Change the preview address");
  action.props.onClick();
  expect(opened).toBe(1);
});
