// lp-shots — CATALOG ONLY. Captures each Live Preview treatment panel whole
// (toolbar included) at desktop and phone density. Not part of the build.
import pw from "/home/ealeixandre/dev/moa/design-visual/tmp/redesign/fidelity/vendor/node_modules/playwright-core/index.js";
const { chromium } = pw;

const PORT = process.env.PORT || 7311;
const ids = (process.env.IDS || "a,a2,b,c").split(",");
const dens = process.env.DENS || "desktop";
const phone = dens === "phone";

const browser = await chromium.launch({ args: ["--font-render-hinting=none", "--disable-lcd-text", "--force-color-profile=srgb", "--hide-scrollbars"] });
const page = await browser.newPage({
  viewport: { width: phone ? 520 : 1340, height: phone ? 980 : 1120 },
  deviceScaleFactor: 1,
});
for (const id of ids) {
  const url = `http://127.0.0.1:${PORT}/?view=lp&t=${id}${phone ? "&dens=phone" : ""}`;
  await page.goto(url, { waitUntil: "networkidle" });
  await page.waitForTimeout(1200);
  // The catalog fires an arrival toast 700ms after load (useCatalogBootstrap).
  // It floats over the panel's right end — exactly where Inspect and close
  // live — so it is removed before the frame is shot.
  await page.evaluate(() => {
    document.querySelectorAll("[class*='toast']").forEach((n) => n.remove());
  });

  const el = await page.$(`[data-t="${id}"]`);
  if (!el) { console.log(`MISS ${id}`); continue; }
  const out = `/tmp/lp-${id}-${phone ? "movil" : "desktop"}.png`;
  await el.screenshot({ path: out });
  console.log("saved", out);
}
await browser.close();
