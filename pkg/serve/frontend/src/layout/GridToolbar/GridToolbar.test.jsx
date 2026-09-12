import { expect, test } from "bun:test";
import { GridToolbar } from "./GridToolbar.jsx";

function buttons(node, result = []) {
  if (!node || typeof node !== "object") return result;
  if (node.type === "button") result.push(node);
  const children = node.props?.children;
  for (const child of Array.isArray(children) ? children : [children]) buttons(child, result);
  return result;
}

function textOf(node) {
  if (node == null || typeof node !== "object") return String(node ?? "");
  if (Array.isArray(node)) return node.map(textOf).join("");
  return textOf(node.props?.children);
}

test("the catalogue bar is Layout · N panes and the yellow needs-you count", () => {
  const tree = GridToolbar({ paneCount: 3, needsYouCount: 1 });
  expect(tree.props.class).toBe("zl-grid-bar");
  expect(textOf(tree)).toContain("Layout");
  expect(textOf(tree)).toContain("3");
  expect(textOf(tree)).toContain("panes");
  expect(textOf(tree)).toContain("needs you");
  expect(buttons(tree).length).toBe(0);
});

test("split controls expose distinct labels", () => {
  // The two splits used to share the visible word "split"; the labels are
  // what a screen reader uses to tell them apart. Inverting them (same
  // label twice) would make the second control unannounced.
  const splitButtons = buttons(GridToolbar({
    onSplitRight: () => {},
    onSplitDown: () => {},
  })).filter((button) => /Split/.test(button.props["aria-label"] || ""));
  expect(splitButtons.map((button) => button.props["aria-label"])).toEqual(["Split right", "Split down"]);
});
