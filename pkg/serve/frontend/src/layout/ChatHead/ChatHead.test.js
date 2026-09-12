import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";

const head = readFileSync(new URL("./ChatHead.jsx", import.meta.url), "utf8");
const strip = readFileSync(new URL("../StatusStrip/StatusStrip.jsx", import.meta.url), "utf8");
const sidebar = readFileSync(new URL("../Sidebar/Sidebar.jsx", import.meta.url), "utf8");
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
  expect(head).toContain("onGridToggle");
});

test("the desktop head is the catalogue's crumb, defined once", () => {
  // METODO §4: the lab imports the shipped head and does not draw its own
  // crumb. A private copy here would be the exact drift this move ends.
  const lab = readFileSync(new URL("../../catalog/zones-lab.jsx", import.meta.url), "utf8");
  expect(lab).toMatch(/import \{ ChatHead \} from ["'].*ChatHead\/ChatHead\.jsx["']/);
  expect(lab).not.toMatch(/<div class="zl-desk-head">/);
  expect(lab).not.toContain("function HeadActions");

  expect(head).toContain('class="zl-desk-head"');
  expect(head).toContain('class="zl-crumb"');
  expect(head).toContain('class="zl-crumb-title"');
  expect(head).toContain("zl-crumb-path");
  expect(head).toContain("zl-desk-act");
  expect(head).not.toContain("chat-head");
  expect(head).not.toContain('class="crumb-title"');
  expect(head).not.toContain("grid-toggle");
});

test("the crumb is a door to the session dossier, with ARIA that says so", () => {
  // Inverting this (always painting a span, or dropping aria-expanded) would
  // bring back a title that does not tell a screen reader it opens anything.
  expect(head).toContain('aria-haspopup={onTitleClick ? "dialog" : undefined}');
  expect(head).toContain("aria-expanded={onTitleClick ? panelOpen : undefined}");
  expect(conv).toContain("panelOpen={panel.open}");
  expect(conv).toContain("toggleSessionPanel(session.id)");
});

test("preview and grid stay wired, and preview keeps the focus return hook", () => {
  expect(head).toContain('data-preview-trigger="true"');
  expect(head).toContain("aria-label=\"Live preview\"");
  expect(head).toContain("aria-label=\"Back to the grid\"");
  expect(head).toContain("onGridToggle &&");
  expect(head).toContain("onPreviewToggle &&");
});

test("the model lives on the status strip next to permission", () => {
  // ModelPill is gone: the status line now carries the catalogue's own markup,
  // where the model is a .zl-st-model button beside .zl-st-perm rather than a
  // separate production component. What this test defends is where the control
  // lives -- on the strip, next to permission -- not the name of the class it
  // used to be made of.
  expect(strip).toContain("zl-st-model");
  expect(strip).toContain("zl-st-perm");
  expect(strip).toContain("modelName");
});

test("the sidebar does not grow a second notifications door next to settings", () => {
  expect(sidebar).not.toContain("Bell");
  expect(sidebar).not.toContain("onNotifications");
});

test("grid panes put the model on the status strip, not the pane header", () => {
  expect(pane).not.toContain("p-model");
  expect(pane).not.toContain("Rewind");
  expect(grid).toContain("modelName=");
});

test("the sidebar lives once; close is wired there, not per view", () => {
  expect(conv).not.toContain("<Sidebar");
  expect(gridScreen).not.toContain("<Sidebar");
  expect(shell).toContain("onCloseSession");
  expect(shell).toContain("<Sidebar");
});

test("one sidebar serves both densities: the phone chassis mounts it, not a copy", () => {
  // The drawer is the phone's FRAME (veil, slide, focus trap, the create
  // screen). If it ever grows its own list again — its own head, its own field,
  // its own group labels — this fails.
  const drawer = readFileSync(
    new URL("../mobile/SessionDrawer/SessionDrawer.jsx", import.meta.url), "utf8",
  );
  expect(drawer).toContain("<Sidebar");
  expect(drawer).toContain('density="phone"');
  expect(drawer).not.toContain("sdrawer-group");
  expect(drawer).not.toContain("sessionSearchMatch");
  expect(drawer).not.toContain("partitionByAttention");
});

test("grid panes share the status strip and put activity above the composer", () => {
  const paneGrid = readFileSync(new URL("../PaneGrid/PaneGrid.jsx", import.meta.url), "utf8");
  expect(paneGrid).toContain("<StatusStrip");
  expect(paneGrid).toContain("<LiveBar");
  expect(paneGrid).not.toMatch(/<StatusStrip[\s\S]*\btask=/);
  expect(paneGrid).toContain("compact");
});
