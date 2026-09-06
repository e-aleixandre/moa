// ModelPill.test.jsx — the pill's thinking meter contract: an explicit null
// position means "not known yet" and draws nothing, while an absent one keeps
// the legacy effort fallback.
import { test, expect } from "bun:test";
import { ModelPill } from "./ModelPill.jsx";
import { ThinkingMeter } from "../../primitives/ThinkingMeter/ThinkingMeter.jsx";

function meterOf(node) {
  if (node == null || typeof node !== "object") return undefined;
  if (Array.isArray(node)) {
    for (const child of node) {
      const found = meterOf(child);
      if (found) return found;
    }
    return undefined;
  }
  if (node.type === ThinkingMeter) return node;
  return meterOf(node.props?.children);
}

test("a null position draws no meter rather than inventing a bar", () => {
  expect(meterOf(ModelPill({ model: "Astra", level: "low", thinkingPosition: null }))).toBeUndefined();
  expect(meterOf(ModelPill({ model: "Astra", level: "low", thinkingPosition: null, readOnly: true }))).toBeUndefined();
});

test("an absent position keeps the legacy effort fallback", () => {
  expect(meterOf(ModelPill({ model: "Terra", level: "medium" })).props.level).toBe("medium");
  expect(meterOf(ModelPill({ model: "Astra", level: "max" })).props.level).toBe("xhigh");
});

test("a known position is what the meter draws", () => {
  const meter = meterOf(ModelPill({ model: "Astra", level: "low", thinkingPosition: "off" }));
  expect(meter.props.level).toBe("off");
  expect(meter.props.label).toBe("Thinking: low");
});
