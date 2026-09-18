// owners-shots — CATALOG ONLY. Captures the Owners surface at 1440x900 and
// 390x844, straight to /tmp/pw-out/owners-*.png. Not part of the build; run by
// hand against catalog-serve on PORT.
import { mkdirSync } from "node:fs";
import pw from "/home/ealeixandre/dev/moa/design-visual/tmp/redesign/fidelity/vendor/node_modules/playwright-core/index.js";
const { chromium } = pw;

const PORT = process.env.PORT || 7377;
const OUT = "/tmp/pw-out";
mkdirSync(OUT, { recursive: true });

const browser = await chromium.launch({
  args: ["--font-render-hinting=none", "--disable-lcd-text", "--force-color-profile=srgb", "--hide-scrollbars"],
});
const page = await browser.newPage({ viewport: { width: 1500, height: 1000 }, deviceScaleFactor: 2 });

// The catalog fires an arrival toast 700ms after load (useCatalogBootstrap);
// it floats over the frames, so it is removed before anything is shot.
async function open(url) {
  await page.goto(url, { waitUntil: "networkidle" });
  await page.waitForTimeout(900);
  await page.evaluate(() => document.querySelectorAll("[class*='toast']").forEach((n) => n.remove()));
}

async function shot(name, query, selector) {
  await open(`http://127.0.0.1:${PORT}/?view=owners&shots=1&${query}`);
  const el = await page.$(selector);
  if (!el) { console.log("MISS", name, query, selector); return; }
  await el.screenshot({ path: `${OUT}/${name}.png` });
  console.log("saved", `${OUT}/${name}.png`);
}

const DESK = ".owl-desk";
const PHONE = ".owl-phone";

// The order is the order the surface is read in, and the names are owners2-*:
// the owners-* set photographed the previous direction (Owners as a door
// beside the Inbox) and must not be mixed with this one.
await shot("owners2-01-mode-desktop", "owners=two&owner=mode", DESK);
await shot("owners2-02-mode-phone", "owners=two&owner=mode", PHONE);
await shot("owners2-03-selected-desktop", "owners=two&owner=selected", DESK);
await shot("owners2-04-selected-phone", "owners=two&owner=selected", PHONE);
await shot("owners2-05-triage-desktop", "owners=waiting&owner=mode&triage=on", DESK);
await shot("owners2-06-triage-phone", "owners=waiting&owner=mode&triage=on", PHONE);
await shot("owners2-07-new-desktop", "owners=two&owner=new", DESK);
await shot("owners2-08-new-phone", "owners=two&owner=new", PHONE);
await shot("owners2-09-empty-desktop", "owners=none&owner=mode", DESK);
await shot("owners2-10-empty-phone", "owners=none&owner=mode", PHONE);
await shot("owners2-11-loading-desktop", "owners=loading&owner=mode", DESK);
await shot("owners2-12-error-desktop", "owners=error&owner=mode", DESK);
await shot("owners2-13-recent-desktop", "owners=two&owner=child", DESK);
await shot("owners2-14-recent-phone", "owners=two&owner=child", PHONE);
await shot("owners2-15-overview-desktop", "owners=two&owner=overview", DESK);
await shot("owners2-16-book-desktop", "owners=two&owner=book", DESK);
await shot("owners2-17-project-desktop", "owners=two&owner=project", DESK);
await shot("owners2-18-overview-phone", "owners=two&owner=overview", PHONE);

// The whole lab page, so the notes and the decision are readable in one image.
await open(`http://127.0.0.1:${PORT}/?view=owners`);
await page.setViewportSize({ width: 1500, height: 1200 });
await page.waitForTimeout(500);
await page.screenshot({ path: `${OUT}/owners2-19-lab-page.png`, fullPage: true });
console.log("saved", `${OUT}/owners2-19-lab-page.png`);

await browser.close();
