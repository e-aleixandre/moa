// home-shots — CATALOG ONLY. Captures each first-screen direction in each of
// the four states, at a real 390×780, straight to /tmp/home-*.png.
// Not part of the build; run by hand against `catalog-serve` on PORT.
import pw from "/home/ealeixandre/dev/moa/design-visual/tmp/redesign/fidelity/vendor/node_modules/playwright-core/index.js";
const { chromium } = pw;

const PORT = process.env.PORT || 7333;
const DIRS = (process.env.DIRS || "a,b,c").split(",");
const STATES = (process.env.STATES || "empty,few,many,waiting").split(",");

const browser = await chromium.launch({
  args: ["--font-render-hinting=none", "--disable-lcd-text", "--force-color-profile=srgb", "--hide-scrollbars"],
});
const page = await browser.newPage({ viewport: { width: 1400, height: 1400 }, deviceScaleFactor: 2 });

// The catalog fires an arrival toast 700ms after load (useCatalogBootstrap);
// it floats over the frames, so it is removed before anything is shot.
async function open(url) {
  await page.goto(url, { waitUntil: "networkidle" });
  await page.waitForTimeout(1400);
  await page.evaluate(() => document.querySelectorAll("[class*='toast']").forEach((n) => n.remove()));
}

// Pass 1 — ?shots=1: every phone frame at 1:1 and nothing else on the page.
await open(`http://127.0.0.1:${PORT}/?view=home&shots=1`);
for (const d of DIRS) {
  for (const s of STATES) {
    const el = await page.$(`[data-home="${d}-${s}"]`);
    if (!el) { console.log(`MISS ${d}-${s}`); continue; }
    await el.screenshot({ path: `/tmp/home-${d}-${s}.png` });
    console.log("saved", `/tmp/home-${d}-${s}.png`);
  }
}

// Pass 2 — the reading page, which is where the contact sheet and the two
// desktop frames live. They are scaled there on purpose: the sheet is a
// comparison, so what matters is seeing the three at once.
await open(`http://127.0.0.1:${PORT}/?view=home`);
for (const [name, sel] of [["contact-waiting", 0], ["contact-few", 1]]) {
  const strip = (await page.$$(".home-strip"))[sel];
  if (!strip) { console.log(`MISS ${name}`); continue; }
  await strip.screenshot({ path: `/tmp/home-${name}.png` });
  console.log("saved", `/tmp/home-${name}.png`);
}
for (const v of ["blank", "card"]) {
  const el = await page.$(`[data-home="desk-${v}"]`);
  if (!el) { console.log(`MISS desk-${v}`); continue; }
  await el.screenshot({ path: `/tmp/home-desktop-${v}.png` });
  console.log("saved", `/tmp/home-desktop-${v}.png`);
}

await browser.close();
