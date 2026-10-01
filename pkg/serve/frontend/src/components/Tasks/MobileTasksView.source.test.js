import { expect, test } from "bun:test";
import { readFileSync } from "fs";
import { join } from "path";

const here = new URL(".", import.meta.url).pathname;
const read = (p) => readFileSync(join(here, p), "utf8");

// The native app draws the web view under the status bar, so a full-screen
// push must clear --shell-top itself. `.zi-head.is-sheet` is the head of a
// sheet that already sits below it: its 4px top padding put the Tasks head
// (‹, title, +) under the clock in the iOS app.
test("the Tasks screen head is a full-screen head, not a sheet head", () => {
  const head = read("MobileTasksView.jsx").match(/<div class="(zi-head[^"]*)">/);
  expect(head?.[1]).toBe("zi-head");
});

test("the phone head of a full-screen push clears the safe area", () => {
  const css = read("../InboxView/InboxView.css");
  expect(css).toMatch(/\.zi-inbox\.is-phone \.zi-head \{[^}]*padding-top: calc\(var\(--shell-top\) \+ \d+px\)/);
});
