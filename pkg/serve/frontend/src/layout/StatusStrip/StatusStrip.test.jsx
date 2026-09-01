import { expect, test } from "bun:test";
import { tokenFlowVariant } from "./status-strip-view-model.js";

test("compact status strips omit the token unit through TokenFlow's compact variant", () => {
  expect(tokenFlowVariant(true)).toBe("compact");
});

test("full status strips retain TokenFlow's strip variant", () => {
  expect(tokenFlowVariant(false)).toBe("strip");
});
