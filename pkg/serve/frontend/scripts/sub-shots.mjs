// sub-shots — CATALOG ONLY. Captures the finished-subagent directions.
// Not part of the build; the browser lives in tmp/redesign/fidelity/vendor.
import pw from "/home/ealeixandre/dev/moa/design-visual/tmp/redesign/fidelity/vendor/node_modules/playwright-core/index.js";
const { chromium } = pw;

const PORT = process.env.PORT || 7333;

// dir:case:density. The phone is the one that hurts, so it goes first and
// every direction gets both outcomes there; the desktop gets the completed
// run, which is enough to judge the measure.
const SHOTS = [];
for (const d of ["a", "b", "c"]) {
  SHOTS.push({ dir: d, k: "ok", dens: "phone" });
  SHOTS.push({ dir: d, k: "fail", dens: "phone" });
  SHOTS.push({ dir: d, k: "ok", dens: "desk" });
}

const browser = await chromium.launch({
  args: ["--font-render-hinting=none", "--disable-lcd-text", "--force-color-profile=srgb", "--hide-scrollbars"],
});

for (const s of SHOTS) {
  const phone = s.dens === "phone";
  const page = await browser.newPage({
    viewport: phone ? { width: 390, height: 780 } : { width: 900, height: 700 },
    deviceScaleFactor: 2,
  });
  await page.goto(`http://127.0.0.1:${PORT}/?view=sub&dir=${s.dir}&k=${s.k}&d=${s.dens}`, {
    waitUntil: "networkidle",
  });
  await page.waitForTimeout(900);
  // The catalog fires an arrival toast 700ms after load (useCatalogBootstrap).
  await page.evaluate(() => document.querySelectorAll("[class*='toast']").forEach((n) => n.remove()));
  const out = `/tmp/sub-${s.dir}-${s.k}-${s.dens}.png`;
  await page.screenshot({ path: out });
  console.log("saved", out);
  await page.close();
}

// The contact sheet, full page.
const page = await browser.newPage({ viewport: { width: 1340, height: 1000 }, deviceScaleFactor: 1 });
await page.goto(`http://127.0.0.1:${PORT}/?view=sub`, { waitUntil: "networkidle" });
await page.waitForTimeout(1200);
await page.evaluate(() => document.querySelectorAll("[class*='toast'],.catalog-nav").forEach((n) => n.remove()));
const sheet = await page.$(".sb-sheet");
if (sheet) { await sheet.screenshot({ path: "/tmp/sub-hoja-contacto.png" }); console.log("saved /tmp/sub-hoja-contacto.png"); }
await page.close();

await browser.close();
