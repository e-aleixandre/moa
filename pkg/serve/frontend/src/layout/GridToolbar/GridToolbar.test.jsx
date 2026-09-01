import { expect, test } from "bun:test";
import { GridToolbar } from "./GridToolbar.jsx";

function buttons(node, result = []) {
  if (!node || typeof node !== "object") return result;
  if (node.type === "button") result.push(node);
  const children = node.props?.children;
  for (const child of Array.isArray(children) ? children : [children]) buttons(child, result);
  return result;
}

test("split controls retain their visual copy but expose distinct labels", () => {
  const splitButtons = buttons(GridToolbar({})).filter((button) => button.props.class === "gt-btn");
  expect(splitButtons.map((button) => button.props["aria-label"])).toEqual(["Split right", "Split down"]);
  expect(splitButtons.map((button) => button.props.children.at(-1))).toEqual([" split", " split"]);
});
