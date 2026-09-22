import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";

const css = readFileSync(new URL("./SubagentView.css", import.meta.url), "utf8");

test("the live subagent body gives Stream the only vertical scroller", () => {
  const body = css.match(/\.subagent-view \.sa-body\s*\{([^}]*)\}/)?.[1] || "";

  expect(body).toContain("display: flex");
  expect(body).toContain("overflow: hidden");
  expect(body).not.toContain("overflow-y: auto");
});
