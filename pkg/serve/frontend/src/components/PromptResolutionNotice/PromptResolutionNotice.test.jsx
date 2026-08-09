import { test, expect } from "bun:test";
import { promptResolutionText } from "./PromptResolutionNotice.jsx";

test("uses an honest remote-resolution notice and includes a known outcome", () => {
  expect(promptResolutionText()).toBe("This request was resolved in another client.");
  expect(promptResolutionText({ kind: "permission", outcome: "approved" }))
    .toBe("This permission was approved in another client.");
  expect(promptResolutionText({ kind: "permission", outcome: "denied" }))
    .toBe("This permission was denied in another client.");
  expect(promptResolutionText({ kind: "ask", outcome: "answered" }))
    .toBe("This request was answered in another client.");
});
