import { describe, expect, it } from "bun:test";
import { readFileSync } from "node:fs";

const css = readFileSync(new URL("./StatusStrip.css", import.meta.url), "utf8");
const jsx = readFileSync(new URL("./StatusStrip.jsx", import.meta.url), "utf8");

// The ring is p1: a measured 320px dock with a 12/20 checklist and one MCP
// alarm pushed it 9px out, because the chip's width grows with its count.
describe("the tasks chip yields before the context ring is cut", () => {
  it("tells CSS how long its count is", () => {
    expect(jsx).toContain("data-wide=");
  });

  it("yields to one alarm at the floor when it is wide or the owner's face is there", () => {
    const floor = css.slice(css.indexOf("@container (max-width: 320px)"));
    expect(floor).toContain('.zl-st-tasks:is([data-owner], [data-wide]):not([data-alarms="0"]) { display: none; }');
  });

  it("yields to one alarm beside the face when wide on a 390 phone", () => {
    expect(css).toContain('.zl-st-tasks[data-wide][data-owner]:not([data-alarms="0"]) { display: none; }');
  });
});
