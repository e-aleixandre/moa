// owners5-shots — the REAL app on :8098, iteration 3 in production.
// Desktop 1440x900 and phone 390x844, straight to /tmp/pw-out/owners5-*.
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

async function boot(page) {
  await page.goto(BASE, { waitUntil: "networkidle" });
  await page.waitForTimeout(1500);
}

const shot = async (page, name) => {
  await page.screenshot({ path: `${OUT}/${name}.png` });
  log("saved", name);
};

/* ── Desktop ─────────────────────────────────────────────────────────── */
const d = await newPage(1440, 900);
await boot(d);
await shot(d, "owners5-01-recent-desktop");

// The OWNERS section and its heading.
const ownersHead = await d.$("button.zl-group.ow-sec");
log("owners heading:", ownersHead ? await ownersHead.textContent() : "MISSING");

// New owner: the page pushed inside the column, with the identity picker.
const newOwner = await d.$(".ow-newowner");
if (newOwner) {
  await newOwner.click();
  await d.waitForTimeout(700);
  await shot(d, "owners5-02-new-owner-desktop");
  // Choose a shape and a colour, then photograph the preview following them.
  const shapes = await d.$$(".ow-idp-row.is-shapes .ow-swatch");
  const colours = await d.$$(".ow-idp-row.is-colours .ow-swatch");
  log("picker:", shapes.length, "shapes,", colours.length, "colours");
  if (shapes[3]) await shapes[3].click();
  if (colours[1]) await colours[1].click();
  await d.waitForTimeout(300);
  await shot(d, "owners5-03-new-owner-picked-desktop");
  // Create it against the real backend, in a folder with no owner yet.
  const dirField = await d.$('input[aria-label="Project folder"]');
  if (dirField) {
    await dirField.fill("/tmp/moa-dv3/proj-beta");
    await d.waitForTimeout(600);
  }
  const nameField = await d.$('input[aria-label="Owner name"]');
  if (nameField) await nameField.fill("Beta owner");
  await d.waitForTimeout(300);
  const create = await d.$(".ow-form-foot .btn, .ow-form-foot button.ow-cta, .ow-form-foot button");
  if (create) {
    await create.click();
    await d.waitForTimeout(3000);
  }
  await shot(d, "owners5-04-after-create-desktop");
}

// Back on the list: two owners in the OWNERS section.
await d.goto(BASE, { waitUntil: "networkidle" });
await d.waitForTimeout(1500);
await shot(d, "owners5-05-owners-section-desktop");

// Collapse ACTIVE, then collapse OWNERS, to photograph both mechanics.
async function toggle(page, label) {
  const heads = await page.$$("button.zl-group.ow-sec");
  for (const h of heads) {
    const t = (await h.textContent()) || "";
    if (t.toLowerCase().includes(label)) { await h.click(); await page.waitForTimeout(500); return true; }
  }
  log("no heading for", label);
  return false;
}
await toggle(d, "active");
await shot(d, "owners5-06-active-collapsed-desktop");
await toggle(d, "owners");
await shot(d, "owners5-07-owners-collapsed-desktop");
await toggle(d, "owners");
await toggle(d, "active");

// By project: the owner as the first row of its group.
const byProject = await d.$('[aria-label="Group by project"]');
if (byProject) { await byProject.click(); await d.waitForTimeout(800); }
await shot(d, "owners5-08-by-project-desktop");

// A child session: the chip in the head, then the owner's dossier.
const recent = await d.$('[aria-label="Sort by recent"]');
if (recent) { await recent.click(); await d.waitForTimeout(600); }
const child = await d.$(".zl-session .zl-row");
if (child) { await child.click(); await d.waitForTimeout(1200); }
await shot(d, "owners5-09-child-chip-desktop");

// The chip opens the owner's conversation; its dossier is the third zone.
const chip = await d.$(".ow-chip");
if (chip) { await chip.click(); await d.waitForTimeout(1500); }
await shot(d, "owners5-10-owner-conversation-desktop");
const dossier = await d.$('[aria-label*="dossier"], .zl-head-actions button[aria-label*="session"], [aria-label="This session"]');
if (dossier) { await dossier.click(); await d.waitForTimeout(1000); }
await shot(d, "owners5-11-owner-dossier-desktop");
await d.context().close();

/* ── Phone ───────────────────────────────────────────────────────────── */
const p = await newPage(390, 844);
await boot(p);
await shot(p, "owners5-20-phone-conversation");
// Open the drawer: the same Sidebar at 300px.
const door = await p.$('.mchip, [aria-label*="essions"], .mtitle-chip');
if (door) { await door.click(); await p.waitForTimeout(900); }
await shot(p, "owners5-21-drawer-recent-phone");
await toggle(p, "active");
await shot(p, "owners5-22-drawer-active-collapsed-phone");
await toggle(p, "active");
const byProjectP = await p.$('[aria-label="Group by project"]');
if (byProjectP) { await byProjectP.click(); await p.waitForTimeout(800); }
await shot(p, "owners5-23-drawer-by-project-phone");
const recentP = await p.$('[aria-label="Sort by recent"]');
if (recentP) { await recentP.click(); await p.waitForTimeout(600); }
const newOwnerP = await p.$(".ow-newowner");
if (newOwnerP) { await newOwnerP.click(); await p.waitForTimeout(900); }
await shot(p, "owners5-24-new-owner-phone");

await browser.close();
log("done");
