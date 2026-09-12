// usage-door.test.js — the gauges group is the door to Usage, and a session
// that has not spent anything yet still needs that door.
//
// The bug: StatusStrip rendered the group on `hasCtx || hasSpend`, so a fresh
// session -- no context reading yet, nothing spent -- drew no gauges at all
// and the panel became unreachable from the line. The comment in the source
// already claimed the opposite ("a fresh session can still reach the panel")
// and `usageTrigger` was computed and then never used. Terra's review found
// it; this locks it down.
//
// Asserted against the SOURCE rather than by rendering, deliberately.
// StatusStrip imports components/index.js -- the whole barrel -- and pulling
// that into the test process is enough to change what CommandPalette's own
// hook runtime sees, which made three of its tests fail. That fragility is
// pre-existing and filed separately; this test does not need to trip it.
import { test, expect } from "bun:test";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const source = readFileSync(
  join(dirname(fileURLToPath(import.meta.url)), "../../layout/StatusStrip/StatusStrip.jsx"),
  "utf8",
);

test("the gauges render when there is a Usage panel to open, not only when there are numbers", () => {
  // The condition has to include the trigger. Without it the door disappears
  // exactly when the session is new -- the moment its own comment promises it
  // will still be there.
  expect(source).toContain("(hasCtx || hasSpend || usageTrigger)");
});

test("usageTrigger is derived from the handler, so no handler means no door", () => {
  // The other half of the rule the whole line follows: a datum that does not
  // exist is not drawn as a zero, and a door that opens nothing is not drawn.
  expect(source).toContain("const usageTrigger = !!onOpenUsage;");
});
