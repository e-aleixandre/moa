import { test, expect } from "bun:test";
import { promptResolutionText, PromptResolutionNotice } from "./PromptResolutionNotice.jsx";

test("uses server-provided attribution and reason", () => {
  expect(promptResolutionText()).toBe("This request was resolved elsewhere.");
  expect(promptResolutionText({ kind: "permission", outcome: "approved" }))
    .toBe("This permission was approved in another client.");
  expect(promptResolutionText({ kind: "permission", outcome: "denied" }))
    .toBe("This permission was denied in another client.");
  expect(promptResolutionText({ kind: "ask", outcome: "answered" }))
    .toBe("This request was answered in another client.");
  expect(promptResolutionText({ reason: "cancelled" }))
    .toBe("This request was closed because the run was cancelled.");
  expect(promptResolutionText({ reason: "aborted" }))
    .toBe("This request was closed because the run was aborted.");
  expect(promptResolutionText({ origin: "tui" }))
    .toBe("This request was resolved in the terminal.");
});

test("does not render a server-projected local resolution", () => {
  expect(PromptResolutionNotice({ notice: { origin: "this_client" } })).toBeNull();
});
