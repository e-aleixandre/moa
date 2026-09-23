import { test, expect } from "bun:test";
import { PreviewErrorBanner, PreviewRecoveryNotice, PreviewURLSetup } from "./PreviewSetup.jsx";

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
// The card's title is a prop of the card, not a node of its own.
const titleOf = (node) => find(node, (n) => typeof n.props?.title === "string")?.props.title;
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
  expect(titleOf(tree)).toBe("Open your app");
  expect(byLabel(tree, "Preview URL").props.value).toBe("");
  expect(find(tree, (n) => n.props?.disabled === true)).toBeTruthy();
});

// On a phone, typing "localhost:5173" is the expensive part: an address this
// browser already previewed is one tap, and that tap loads it.
test("an address used before is offered and opens with one tap", () => {
  const picked = [];
  const tree = PreviewURLSetup({
    value: "",
    onInput: () => {},
    onCommit: () => {},
    onCancel: () => {},
    recent: ["http://localhost:5173", "http://localhost:3000/"],
    onPick: (url) => picked.push(url),
  });
  const chip = find(tree, (n) => textOf(n) === "localhost:3000" && n.props?.onClick);
  expect(chip).toBeTruthy();
  chip.props.onClick();
  expect(picked).toEqual(["http://localhost:3000/"]);
});

test("the first screen offers nothing when there is nothing to offer", () => {
  const tree = PreviewURLSetup({ value: "", onInput: () => {}, onCommit: () => {}, onCancel: () => {} });
  expect(byClass(tree, "live-preview-recent")).toBeNull();
  expect(byClass(tree, "live-preview-setup-back")).toBeNull();
});

test("a preview error offers a retry without asking for a proxy address", () => {
  let opened = 0;
  let changed = 0;
  const tree = PreviewErrorBanner({ message: "Make https://dev.test:7351 reachable from this device, then try again.", onRetry: () => { opened += 1; }, onChangeURL: () => { changed += 1; } });
  expect(textOf(tree)).toContain("reachable from this device");
  const action = byClass(tree, "live-preview-proxy-error-action");
  expect(textOf(action)).toBe("Try again");
  action.props.onClick();
  expect(opened).toBe(1);
  const change = byClass(tree, "live-preview-setup-back");
  expect(textOf(change)).toBe("Change the app URL");
  change.props.onClick();
  expect(changed).toBe(1);
});

test("a disconnected bridge offers an explicit return to the configured app", () => {
  let returned = 0;
  const tree = PreviewRecoveryNotice({ message: "This page is no longer connected to Moa.", onReturn: () => { returned += 1; } });
  expect(textOf(tree)).toContain("This page is no longer connected to Moa.");
  const action = byClass(tree, "live-preview-recovery-action");
  expect(textOf(action)).toBe("Return to app");
  action.props.onClick();
  expect(returned).toBe(1);
});
