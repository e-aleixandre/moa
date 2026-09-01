import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";

const view = readFileSync(new URL("./SubagentView.jsx", import.meta.url), "utf8");

test("the subagent header is a breadcrumb, not a second model toolbar", () => {
  expect(view).not.toMatch(/sa-head[\s\S]*ModelPill/);
  expect(view).toContain("RunModeChip");
});

test("the subagent strip is the same StatusStrip, with the child's model on it", () => {
  expect(view).toContain("<StatusStrip");
  expect(view).toContain("modelName={view.model}");
  expect(view).not.toMatch(/<StatusStrip[\s\S]*\btask=/);
});
