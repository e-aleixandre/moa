import { test, expect } from "bun:test";
import { readFileSync } from "node:fs";

const page = readFileSync(new URL("./UsagePage.jsx", import.meta.url), "utf8");

test("a Gemini session's Usage page reads the quota on opening and says it is reading", () => {
  expect(page).toContain('loadUsage({ refresh: provider })');
  expect(page).toContain("(hasPlan || reading) &&");
  expect(page).toContain("reading…");
});

test("a Gemini session shows no prompt-cache row, and signed out says what to do", () => {
  expect(page).toContain("{!gemini && <CacheRows session={session} />}");
  expect(page).toContain("Sign in to Antigravity on the server");
});
