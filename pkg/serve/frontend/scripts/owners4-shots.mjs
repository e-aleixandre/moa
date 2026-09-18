// owners4-shots — CATALOG ONLY. Iteration 3 of the Owners surface, at
// 1440x900 (desktop) and 390x844 (phone), straight to /tmp/pw-out/owners4-*.
// Not part of the build; run by hand against catalog-serve on PORT.
//
// Adapted from scripts/owners-shots.mjs: the presets are iteration 3's and the
// prefix is owners4-, because owners2-* and owners3-* photographed the mode
// direction and must not be mixed with the section one.
import { mkdirSync } from "node:fs";
import pw from "/home/ealeixandre/dev/moa/design-visual/tmp/redesign/fidelity/vendor/node_modules/playwright-core/index.js";
const { chromium } = pw;

const PORT = process.env.PORT || 7301;
const OUT = "/tmp/pw-out";
mkdirSync(OUT, { recursive: true });

const browser = await chromium.launch({
  args: ["--font-render-hinting=none", "--disable-lcd-text", "--force-color-profile=srgb", "--hide-scrollbars"],
});
const page = await browser.newPage({ viewport: { width: 1520, height: 1000 }, deviceScaleFactor: 2 });

async function open(url) {
  await page.goto(url, { waitUntil: "networkidle" });
  await page.waitForTimeout(800);
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

const PRESETS = [
  ["01", "recent", "recent"],
  ["02", "collapsed-active", "active-collapsed"],
  ["03", "project", "by-project"],
  ["04", "asks", "owner-asks"],
  ["05", "owners-collapsed", "owners-collapsed"],
  ["06", "new", "new-owner"],
  ["07", "gallery", "avatar-gallery"],
];

for (const [n, preset, label] of PRESETS) {
  await shot(`owners4-${n}-${label}-desktop`, `preset=${preset}`, DESK);
  await shot(`owners4-${n}-${label}-phone`, `preset=${preset}`, PHONE);
}

// The whole lab page, so the notes and the decision are readable in one image.
await open(`http://127.0.0.1:${PORT}/?view=owners`);
await page.setViewportSize({ width: 1520, height: 1200 });
await page.waitForTimeout(400);
await page.screenshot({ path: `${OUT}/owners4-08-lab-page.png`, fullPage: true });
console.log("saved", `${OUT}/owners4-08-lab-page.png`);

await browser.close();
