import { test, expect } from "bun:test";

// Imported through the SOURCE rather than the module, deliberately.
//
// PermissionCard itself is hooks-free, but CommandPalette's tests run over
// their own hook runtime installed with mock.module, which bun applies
// process-wide and never restores. Loading a component module into the same
// test process has already broken three of those tests (see
// tmp/redesign/fidelity/ESTADO.md). Asserting on the source keeps this test
// honest without papering over that fragility.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const dir = dirname(fileURLToPath(import.meta.url));
const source = readFileSync(join(dir, "PermissionCard.jsx"), "utf8");
const css = readFileSync(join(dir, "PermissionCard.css"), "utf8");
const prompt = readFileSync(join(dir, "PermissionPrompt.jsx"), "utf8");

test("allow and deny are distinct actions, allow first", () => {
  // A test that cannot tell approve from deny is not a permission test.
  // The primary button fires onAllow; Deny fires onDeny; they are not the
  // same call, and Allow is the one you hit first.
  const allowAt = source.indexOf("onClick={onAllow}");
  const denyAt = source.indexOf("onClick={onDeny}");
  expect(allowAt).toBeGreaterThan(-1);
  expect(denyAt).toBeGreaterThan(allowAt);
  expect(source).toMatch(/class=\{`zl-ask-btn is-primary/);
  expect(source).toMatch(/>\s*Deny\s*</);
});

test("always is a third action, not a relabel of allow, and destructive ops do not get it", () => {
  expect(source).toContain("onClick={onAlways}");
  expect(source).toMatch(/!destructive && alwaysLabel/);
  const alwaysAt = source.indexOf("onClick={onAlways}");
  const allowAt = source.indexOf("onClick={onAllow}");
  const denyAt = source.indexOf("onClick={onDeny}");
  expect(alwaysAt).toBeGreaterThan(allowAt);
  expect(denyAt).toBeGreaterThan(alwaysAt);
});

test("the command is the evidence, never clipped", () => {
  // The command sits alone in its own block (`.zl-ask-cmd-text`), with the
  // working directory above it. Ellipsis or line-clamp on that block would
  // let you approve a command other than the one you saw.
  expect(source).toMatch(/<code class="zl-ask-cmd-text zl-data">/);
  expect(source).toContain("CommandLine");
  expect(source).toMatch(/cwd && <div class="zl-ask-cwd zl-data">\{cwd\}<\/div>/);
  // Only the Always button's rule label may be shortened (its full text is the
  // title); nothing that shows the command or the cwd may be.
  const shown = css.match(/\.zl-ask-(?:cmd|cwd|cmd-text) \{[^}]*\}/g).join("");
  expect(shown).not.toMatch(/text-overflow:\s*ellipsis|line-clamp/);
  expect(css).not.toMatch(/-webkit-line-clamp/);
  expect(css).toMatch(/overflow-wrap:\s*anywhere/);
  expect(css).toMatch(/white-space:\s*pre-wrap/);
});

test("a failed resolve stays on the card as an error, not a toast", () => {
  expect(source).toMatch(/error && <div class="zl-ask-error">\{error\}<\/div>/);
});

test("destructive is a state of the card, not a restyle of deny", () => {
  expect(source).toMatch(/is-danger/);
  expect(source).toContain("Allow anyway");
  expect(css).toMatch(/\.zl-ask\.is-danger/);
});

test("the card is a group you can reach without a mouse", () => {
  expect(source).toContain('role="group"');
  expect(source).toContain('aria-label="Permission requested"');
  expect(css).toMatch(/\.zl-ask-btn:focus-visible/);
  expect(css).toMatch(/\.mconv-blocking \.zl-ask-btn[\s\S]*min-height:\s*44px/);
});

test("a bash permission prints the command, not a JSON wrapper", () => {
  // The user approves the string that will run. Extra keys (cwd, timeout)
  // must not replace that string with a JSON blob of the whole args object.
  expect(prompt).toContain("function permissionCommand");
  expect(prompt).toMatch(/typeof args\.command === "string"/);
  expect(prompt).not.toMatch(/Object\.keys\(args\)\.length === 1/);
  expect(prompt).toContain("command={permissionCommand(perm)}");
  expect(prompt).toContain("scope={permissionScope(perm)}");
});

test("a long command scrolls inside its block so the decision stays on screen", () => {
  expect(css).toMatch(/\.zl-ask \{[^}]*max-height:\s*60dvh/);
  expect(css).toMatch(/\.zl-ask-cmd \{[^}]*overflow-y:\s*auto/);
});

test("the Always button never grows with a long rule pattern", () => {
  expect(css).toMatch(/\.zl-ask-btn\.is-always b \{[^}]*text-overflow:\s*ellipsis/);
  // The whole rule is readable inside the scrolling block, wrapped.
  expect(source).toContain('class="zl-ask-rule-v zl-data"');
  expect(css).toMatch(/\.zl-ask-rule-v \{[^}]*overflow-wrap:\s*anywhere/);
  expect(css).toMatch(/\.zl-ask \{[^}]*overflow-y:\s*auto/);
});

test("the decision is pinned under a scrolling card and the short-screen layout keeps one row", () => {
  expect(css).toMatch(/\.zl-ask-acts \{[^}]*position:\s*sticky/);
  expect(css).toMatch(/@media \(max-height:\s*520px\)/);
  // Geometry (Allow/Always/Deny hit-tested at 390x400 and 390x844) is checked
  // in Chromium during QA: bun has no layout engine.
});
