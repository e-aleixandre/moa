import { test, expect } from "bun:test";
import { existsSync, readFileSync } from "node:fs";

const frontend = new URL("../../", import.meta.url);

function source(path) {
  return readFileSync(new URL(path, frontend), "utf8");
}

test("MCP has one surface: the session panel page", () => {
  expect(existsSync(new URL("components/McpPanel/McpPanel.jsx", frontend))).toBe(false);
  expect(source("components/SessionPanel/SessionPanel.jsx")).toContain("<McpPage");
});

test("status-line MCP chips lead to the session panel page", () => {
  for (const host of [
    "layout/ConversationScreen/ConversationScreen.jsx",
    "layout/mobile/MobileStatusLine/MobileStatusLine.jsx",
  ]) {
    expect(source(host)).toContain('toggleSessionPanel(session.id, "mcp")');
    expect(source(host)).not.toContain("McpPanel");
  }

  const grid = source("layout/PaneGrid/PaneGrid.jsx");
  expect(grid).toContain('openSessionPanel(session.id, "mcp")');
  expect(grid).not.toContain("McpPanel");
});
