import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";

const view = readFileSync(new URL("./SubagentView.jsx", import.meta.url), "utf8");
const sheet = readFileSync(new URL("./SubagentView.css", import.meta.url), "utf8");
const mobile = readFileSync(new URL("../mobile/MobileConversationScreen/MobileSubagentView.jsx", import.meta.url), "utf8");
const composerCss = readFileSync(new URL("../Composer/Composer.css", import.meta.url), "utf8");
const streamCss = readFileSync(new URL("../Stream/Stream.css", import.meta.url), "utf8");

// These assertions read the SOURCE rather than a render, which is a weak kind
// of test: it cannot see what the screen does, only what the file says. They
// are kept in that form because what they defend is a composition rule -- what
// this screen is allowed to MOUNT -- and because the repo has no renderer in
// its test deps. Treat a failure here as "the rule was crossed", then go look.

test("a subagent screen does not grow a second set of turn controls", () => {
  // A subagent is an errand you are reading, not a session you configure, so
  // it must not grow the parent's controls. Its read-only StatusStrip carries
  // only the child's own model, thinking, and context.
  expect(view).not.toContain("<ModelPill");
  expect(view).not.toContain("<PermissionControl");
  expect(view).toMatch(/<StatusStrip[\s\S]*showPermission=\{false\}/);
});

test("the model is configuration in the status strip and provenance in the report foot", () => {
  // The live line answers only what is happening. The model and thinking use
  // the same below-composer status-strip slot as the parent; when the run has
  // ended, that configuration settles into the report foot beside its figures.
  expect(view).toContain("view.model");
  expect(view).toMatch(/<SubagentStatusStrip view=\{view\} \/>/);
  expect(mobile).toMatch(/<SubagentStatusStrip view=\{view\} compact \/>/);
  expect(view).toMatch(/<SubIdent view=\{view\} \/>[\s\S]*marks\.map/);
  const liveBar = view.slice(view.indexOf("export function SubagentLiveBar"), view.indexOf("// SubagentLive —"));
  expect(liveBar).not.toContain("<SubIdent");
});

test("a live head stays quiet; a terminal head keeps its outcome", () => {
  expect(view).toMatch(/state=\{view\.terminal \? <SubState view=\{view\} \/> : null\}/);
  expect(mobile).toMatch(/state=\{view\.terminal \? <SubState view=\{view\} \/> : null\}/);
  expect(view).not.toMatch(/word="Running"/);
});

test("child context uses the status-strip reading live and freezes in the report foot", () => {
  // The view model supplies the child's real percentage (or -1 when unknown),
  // not the parent's. CtxRing is the exact main-conversation representation.
  expect(view).toMatch(/ctxPercent=\{view\.contextPercent >= 0 \? view\.contextPercent : undefined\}/);
  expect(view).toMatch(/<CtxRing pct=\{view\.contextPercent\} \/>/);
  expect(view).toMatch(/<SubagentContext view=\{view\} \/>[\s\S]*marks\.map/);
});

// The owner, after reading a finished run on his phone: "esa forma de ver la
// conversación como con desplegables no me gusta. No sé qué ganamos con esa
// vista." Nothing: the record is what the screen is for, and it was two taps
// away inside a closed Disclosure.
test("a finished errand shows its record whole, with nothing folded", () => {
  expect(view).not.toContain("<Disclosure");
  expect(view).not.toContain('label="Work log"');
  expect(view).not.toContain("<RunDetails");
  // The record is the shipped Stream, mounted directly as the page.
  expect(view).toMatch(/SubagentReport[\s\S]*<Stream/);
});

// "Es verdad que viene bien ese informe": the figures were never the problem,
// their shape was. They are a foot now -- always on screen, so duration,
// tokens and cost are answerable at any scroll position, which the disclosure
// could not do even while open.
test("the run's figures are a permanent foot, not a fold", () => {
  expect(view).toMatch(/<SubagentFoot/);
  expect(sheet).toMatch(/\.sa-foot\s*\{[^}]*flex:\s*none/s);
  expect(sheet).not.toMatch(/\.sa-report-col|\.sa-headline|\.sa-log\b/);
});

// Job ID and parent session do not fit on the foot (20 mono characters would
// push the cost off a 390px bar) and are the one datum here you copy rather
// than read. They are behind a tap on the figures -- one gesture away, never
// dropped.
test("the identifiers are reachable from the foot", () => {
  expect(view).toMatch(/ids\.push\(\["Parent session", session\.id\]\)/);
  expect(view).toMatch(/<RunIds ids=\{ids\}/);
});

test("finished and cancelled are neutral, never green", () => {
  // Green means running in this system. A green tick on something that has
  // stopped teaches the eye that green is decoration.
  expect(view).toMatch(/outcome === "cancelled".*tone="neutral"/s);
  expect(view).toMatch(/word="Completed"/);
  expect(view).not.toMatch(/tone="(ok|success|done|green)"/);
});

// The head is ONE row on both surfaces. The owner: "es como que hay dos
// cabeceras, cuando yo no veo que hiciera falta dos cabeceras. En una cabría
// todo." What made it two was Stop -- 390px cannot hold a way back, a state
// word and two controls -- so Stop moved to the live bar, where the parent
// conversation has kept it since the composer gave it up.
test("both surfaces mount ONE head, in one row", () => {
  for (const source of [view, mobile]) {
    expect(source).toMatch(/<WorkHead[\s\S]*inlineTitle/);
    // No sub-line: a second line under the row IS the second head.
    expect(source).not.toMatch(/sub=\{<Sub/);
  }
});

test("Stop is on the live bar, with its confirmation intact", () => {
  expect(view).toMatch(/SubagentLiveBar[\s\S]*zl-live-stop/);
  expect(view).toMatch(/confirmCancel \? "sure\?" : "Stop"/);
  // Both surfaces route the same armed flag into the same bar.
  expect(mobile).toMatch(/<SubagentLiveBar view=\{view\} onStop=\{onStop\} confirmCancel=\{confirmCancel\} \/>/);
  // And the phone keeps a 44px target for it: LiveBar.css sizes it for
  // `.mconv`, and the push (`.msa`) is outside that host.
  expect(sheet).toMatch(/\.msa \.zl-live-stop\s*\{[^}]*height:\s*44px/s);
});

test("desktop subagent live content shares the conversation measure", () => {
  // The branch bypasses ConversationScreen's dock, so this remains explicit:
  // deleting any of the three rules makes its composer, status line or record
  // stretch across desktop. The status line is named because it is the one
  // that was missing: it ran edge to edge while the other two held the column.
  expect(composerCss).toMatch(/\.subagent-view > \.zl-live,\s*\.subagent-view > \.zl-composer,\s*\.subagent-view > \.zl-status\s*\{[^}]*max-width:\s*var\(--content-block\)[^}]*margin-inline:\s*auto/s);
  expect(streamCss).toMatch(/\.subagent-view \.zl-transcript:not\(\.is-dense\) > \*\s*\{[^}]*max-width:\s*var\(--content-block\)[^}]*margin-inline:\s*auto/s);
});
