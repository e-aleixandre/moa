// owners6-shots — the owner's three corrections, in the REAL app on :8098.
// 1. the avatar palette at 32px, 2. the model selector inside New owner,
// 3. New owner as a modal (desktop) and a bottom sheet (phone).
// Not part of the build; run by hand against a moa serve on PORT.
import { mkdirSync } from "node:fs";
import pw from "/home/ealeixandre/dev/moa/design-visual/tmp/redesign/fidelity/vendor/node_modules/playwright-core/index.js";
const { chromium } = pw;

const PORT = process.env.PORT || 8098;
const BASE = `http://127.0.0.1:${PORT}`;
const OUT = "/tmp/pw-out";
mkdirSync(OUT, { recursive: true });

const browser = await chromium.launch({
  args: ["--font-render-hinting=none", "--disable-lcd-text", "--force-color-profile=srgb", "--hide-scrollbars"],
});
const log = (...a) => console.log(...a);

async function newPage(width, height) {
  const ctx = await browser.newContext({ viewport: { width, height }, deviceScaleFactor: 2 });
  const page = await ctx.newPage();
  page.on("pageerror", (e) => log("PAGEERROR", e.message));
  page.on("console", (m) => { if (m.type() === "error") log("CONSOLE", m.text()); });
  return page;
}
const boot = async (page) => { await page.goto(BASE, { waitUntil: "networkidle" }); await page.waitForTimeout(1500); };
const shot = async (page, name) => { await page.screenshot({ path: `${OUT}/${name}.png` }); log("saved", name); };

/* ── Desktop ─────────────────────────────────────────────────────────── */
const d = await newPage(1440, 900);
await boot(d);
await shot(d, "owners6-01-recent-desktop");

// New owner is a MODAL now, not a page of the column.
await d.click(".ow-newowner");
await d.waitForTimeout(700);
log("modal:", await d.$(".sheet.ow-dialog") ? "present" : "MISSING");
await shot(d, "owners6-02-new-owner-modal-desktop");

// The model row opens the product's ModelSelector as a popover.
await d.click(".ow-modelrow");
await d.waitForTimeout(600);
log("popover:", await d.$(".ow-model-anchor .zl-pop") ? "present" : "MISSING");
log("model row says:", (await d.textContent(".ow-modelrow"))?.trim());
await shot(d, "owners6-03-model-popover-desktop");

// Thinking lives inside it, as everywhere else in the app.
const think = await d.$$(".zl-pop .zl-seg-opt");
log("thinking options:", think.length);
if (think[1]) { await think[1].click(); await d.waitForTimeout(400); }
await shot(d, "owners6-04-model-thinking-desktop");
// Pick a different model from All models.
await d.click(".zl-pop .zl-pick-all");
await d.waitForTimeout(400);
await shot(d, "owners6-05-model-providers-desktop");
const prov = await d.$$(".zl-pop .zl-prov");
if (prov[0]) { await prov[0].click(); await d.waitForTimeout(400); }
const chips = await d.$$(".zl-pop .zl-mchip");
log("chips in provider:", chips.length);
if (chips[1]) { await chips[1].click(); await d.waitForTimeout(500); }
log("model row now says:", (await d.textContent(".ow-modelrow"))?.trim());
await shot(d, "owners6-06-model-chosen-desktop");

// Pick a shape and colour, then create for real.
const shapes = await d.$$(".ow-idp-row.is-shapes .ow-swatch");
const colours = await d.$$(".ow-idp-row.is-colours .ow-swatch");
log("picker:", shapes.length, "shapes,", colours.length, "colours");
if (shapes[2]) await shapes[2].click();
if (colours[4]) await colours[4].click();
await d.waitForTimeout(300);
await shot(d, "owners6-07-avatar-picked-desktop");

// 409: a folder that already has an owner, so the error is visible.
await d.fill('input[aria-label="Project folder"]', "/tmp/moa-dv3/proj-alpha");
await d.waitForTimeout(700);
await d.click(".ow-form-foot button");
await d.waitForTimeout(2500);
log("failure:", (await d.textContent(".ow-fail"))?.trim() || "NONE");
await shot(d, "owners6-08-create-conflict-desktop");

// Then a folder with no owner: the real create.
await d.fill('input[aria-label="Project folder"]', "/tmp/moa-dv3/proj-eps");
await d.waitForTimeout(700);
await d.fill('input[aria-label="Owner name"]', "Epsilon owner");
await d.waitForTimeout(300);
await d.click(".ow-form-foot button");
await d.waitForTimeout(4000);
log("modal after create:", await d.$(".sheet.ow-dialog") ? "STILL OPEN" : "closed");
await shot(d, "owners6-09-after-create-desktop");

// Back to the list: the new owner is there, with its chosen face.
await d.goto(BASE, { waitUntil: "networkidle" });
await d.waitForTimeout(1800);
await shot(d, "owners6-10-owners-section-desktop");
await d.context().close();

/* ── Phone ───────────────────────────────────────────────────────────── */
const p = await newPage(390, 844);
await boot(p);
// With no session open the phone shows the empty state, whose "All sessions"
// is the door to the drawer; with one open it is the header capsule.
// With no session open the phone shows the empty state, whose "All sessions"
// opens the drawer; the first button there is New session, which is the
// palette. Pick by text so the wrong door is not taken.
const doors = await p.$$(".mconv-empty-actions button, .mchip, .mtitle-chip");
let opened = false;
for (const b of doors) {
  const t = (await b.textContent()) || "";
  if (/all sessions/i.test(t)) { await b.click(); opened = true; break; }
}
if (!opened && doors[0]) await doors[0].click();
await p.waitForTimeout(1400);
await shot(p, "owners6-20-drawer-phone");
await p.waitForSelector(".ow-newowner", { timeout: 10000 });
await p.click(".ow-newowner");
await p.waitForTimeout(1200);
log("phone sheet:", await p.$(".msheet") ? "present" : "MISSING");
await shot(p, "owners6-21-new-owner-sheet-phone");
await p.click(".ow-modelrow");
await p.waitForTimeout(800);
log("phone picker sheet:", await p.$(".zl-sheet") ? "present" : "MISSING");
await shot(p, "owners6-22-model-sheet-phone");
await browser.close();
log("done");
