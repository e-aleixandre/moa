import { test, expect } from "bun:test";
import { promptResolutionText, PromptResolutionNotice } from "./PromptResolutionNotice.jsx";

test("uses the same neutral wording for every resolution", () => {
  expect(promptResolutionText()).toBe("");
  expect(promptResolutionText({ id: "permission", kind: "permission" }))
    .toBe("This request is no longer pending.");
  expect(promptResolutionText({ id: "ask", kind: "ask" }))
    .toBe("This request is no longer pending.");
});

test("renders every resolved prompt", () => {
  expect(PromptResolutionNotice({ notice: { id: "permission", kind: "permission" } })).not.toBeNull();
});
