import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";

const head = readFileSync(new URL("./ChatHead.jsx", import.meta.url), "utf8");
const strip = readFileSync(new URL("../StatusStrip/StatusStrip.jsx", import.meta.url), "utf8");
const spine = readFileSync(new URL("../Spine/Spine.jsx", import.meta.url), "utf8");
const pane = readFileSync(new URL("../Pane/Pane.jsx", import.meta.url), "utf8");
const grid = readFileSync(new URL("../PaneGrid/PaneGrid.jsx", import.meta.url), "utf8");

test("the conversation header is a title, not a toolbar of session controls", () => {
  expect(head).not.toContain("ModelPill");
  expect(head).not.toContain("Bell");
  expect(head).not.toContain("MoreHorizontal");
  expect(head).not.toContain("onRewind");
  expect(head).not.toContain("head-rewind");
  expect(head).not.toContain("StateDot");
  expect(head).toContain("grid");
});

test("the model lives on the status strip next to permission", () => {
  expect(strip).toContain("ModelPill");
  expect(strip).toContain("modelName");
});

test("the spine does not grow a second notifications door next to settings", () => {
  expect(spine).not.toContain("Bell");
  expect(spine).not.toContain("onNotifications");
});

test("grid panes put the model on the status strip, not the pane header", () => {
  expect(pane).not.toContain("p-model");
  expect(grid).toContain("modelName=");
});
