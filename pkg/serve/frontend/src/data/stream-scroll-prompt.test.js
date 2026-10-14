import { test, expect } from "bun:test";
import { pendingPromptKey } from "./stream-scroll-policy.js";

test("a pending ask or secret request has a key, so the stream pins to it", () => {
  expect(pendingPromptKey({})).toBe("");
  expect(pendingPromptKey({ pendingAsk: { id: "a1" } })).toBe("a1");
  expect(pendingPromptKey({ pendingSecrets: [{ id: "s1" }, { id: "s2" }] })).toBe("s1");
  expect(pendingPromptKey({ pendingAsk: { id: "a1" }, pendingSecrets: [{ id: "s1" }] })).toBe("a1|s1");
});
