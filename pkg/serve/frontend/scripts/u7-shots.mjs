// u7-shots — CATALOG ONLY. One frame per variant and density, plus a mobile
// contact sheet. Not part of the build.
import pw from "/home/ealeixandre/dev/moa/design-visual/tmp/redesign/fidelity/vendor/node_modules/playwright-core/index.js";
const { chromium } = pw;

const PORT = process.env.PORT || 7333;
const IDS = (process.env.IDS || "espejo,bano,titular,hendido,losa,capsula,burbuja,filo").split(",");

const browser = await chromium.launch({
  args: ["--font-render-hinting=none", "--disable-lcd-text", "--force-color-profile=srgb", "--hide-scrollbars"],
});
const page = await browser.newPage({ viewport: { width: 1500, height: 1400 }, deviceScaleFactor: 1 });
await page.goto(`http://127.0.0.1:${PORT}/?view=u7`, { waitUntil: "networkidle" });
await page.waitForTimeout(1500);
// The catalog fires an arrival toast 700ms after load (useCatalogBootstrap).
await page.evaluate(() => {
  document.querySelectorAll("[class*='toast']").forEach((n) => n.remove());
  // The variant selector is sticky, so it floats OVER whichever frame is
  // being shot. It is lab chrome, not the design.
  document.querySelectorAll(".u7-bar, .catalog-nav").forEach((n) => { n.style.display = "none"; });
});

for (const id of IDS) {
  for (const kind of ["phone", "desk"]) {
    const el = await page.$(`#u7-${id} [data-shot="${id}-${kind}"]`);
    if (!el) { console.log(`MISS ${id}-${kind}`); continue; }
    const out = `/tmp/user2-${id}-${kind === "phone" ? "movil" : "escritorio"}.png`;
    await el.screenshot({ path: out });
    console.log("saved", out);
  }
}
await browser.close();
