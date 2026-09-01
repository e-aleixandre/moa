import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";

const head = readFileSync(new URL("./ChatHead.jsx", import.meta.url), "utf8");
const strip = readFileSync(new URL("../StatusStrip/StatusStrip.jsx", import.meta.url), "utf8");
const spine = readFileSync(new URL("../Spine/Spine.jsx", import.meta.url), "utf8");
const pane = readFileSync(new URL("../Pane/Pane.jsx", import.meta.url), "utf8");
const grid = readFileSync(new URL("../PaneGrid/PaneGrid.jsx", import.meta.url), "utf8");
const conv = readFileSync(new URL("../ConversationScreen/ConversationScreen.jsx", import.meta.url), "utf8");
const gridScreen = readFileSync(new URL("../PaneGridScreen/PaneGridScreen.jsx", import.meta.url), "utf8");
const shell = readFileSync(new URL("../DesktopShell/DesktopShell.jsx", import.meta.url), "utf8");

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
  expect(pane).not.toContain("Rewind");
  expect(grid).toContain("modelName=");
});

test("the sidebar lives once; close is wired there, not per view", () => {
  expect(conv).not.toContain("<Spine");
  expect(gridScreen).not.toContain("<Spine");
  expect(shell).toContain("onCloseSession");
  expect(shell).toContain("<Spine");
});

test("grid panes share the status strip and put activity above the composer", () => {
  const paneGrid = readFileSync(new URL("../PaneGrid/PaneGrid.jsx", import.meta.url), "utf8");
  expect(paneGrid).toContain("<StatusStrip");
  expect(paneGrid).toContain("<NowLine");
  expect(paneGrid).not.toMatch(/<StatusStrip[\s\S]*\btask=/);
  expect(paneGrid).toContain("compact");
});
