import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";

const view = readFileSync(new URL("./SubagentView.jsx", import.meta.url), "utf8");

// These assertions read the SOURCE rather than a render, which is a weak kind
// of test: it cannot see what the screen does, only what the file says. They
// are kept in that form because what they defend is a composition rule -- what
// this screen is allowed to MOUNT -- and because the repo has no renderer in
// its test deps. Treat a failure here as "the rule was crossed", then go look.

test("a subagent screen does not grow a second set of turn controls", () => {
  // The parent's status line owns the controls for the next turn. A subagent
  // is an errand you are reading, not a session you are configuring, so the
  // model pill, the permission control and the context ring have no business
  // here -- they would offer to change settings that belong to the parent.
  expect(view).not.toContain("<StatusStrip");
  expect(view).not.toContain("<ModelPill");
  expect(view).not.toContain("<PermissionControl");
});

test("the model is provenance in the head, not a control", () => {
  // Which agent ran the errand still matters -- it just answers "who did
  // this", asked after "what was it". It rides the head's sub-line as text.
  expect(view).toContain("view.model");
  expect(view).toMatch(/sub=\{<SubHead/);
});

test("a finished errand leads with its report, not with the record", () => {
  // The result used to sit in a banner UNDER the whole transcript: the one
  // thing you opened the screen for was the last thing you reached.
  const report = view.indexOf("SubagentReport");
  const log = view.indexOf('label="Work log"');
  expect(report).toBeGreaterThan(-1);
  expect(log).toBeGreaterThan(report);
  // And the record is folded away behind a disclosure rather than open.
  expect(view).toMatch(/<Disclosure[\s\S]*label="Work log"/);
});

test("finished and cancelled are neutral, never green", () => {
  // Green means running in this system. A green tick on something that has
  // stopped teaches the eye that green is decoration.
  expect(view).toMatch(/outcome === "cancelled".*tone="neutral"/s);
  expect(view).toMatch(/word="Completed"/);
  expect(view).not.toMatch(/tone="(ok|success|done|green)"/);
});
