// lp2-shots — CATALOG ONLY. Captures the redesigned Live Preview PANEL whole:
// chrome, stage, and the loaded state. Not part of the build.
import pw from "/home/ealeixandre/dev/moa/design-visual/tmp/redesign/fidelity/vendor/node_modules/playwright-core/index.js";
const { chromium } = pw;

const PORT = process.env.PORT || 7311;
// id → output name. The owner asked for three files per proposal: empty,
// loaded, phone.
const SHOTS = (process.env.SHOTS || "1:1-desktop:desktop,1b:1-ya-usado:desktop,1c:1-cargada:desktop,1:1-movil:phone")
  .split(",")
  .map((s) => {
    const [id, name, dens] = s.split(":");
    return { id, name, phone: dens === "phone" };
  });
const PREFIX = process.env.PREFIX || "lp2";
const VIEW = process.env.VIEW || "lp2";

const browser = await chromium.launch({ args: ["--font-render-hinting=none", "--disable-lcd-text", "--force-color-profile=srgb", "--hide-scrollbars"] });
for (const shot of SHOTS) {
  const page = await browser.newPage({
    viewport: { width: shot.phone ? 520 : 1340, height: shot.phone ? 980 : 1060 },
    deviceScaleFactor: 1,
  });
  const url = `http://127.0.0.1:${PORT}/?view=${VIEW}&t=${shot.id}${shot.phone ? "&dens=phone" : ""}`;
  await page.goto(url, { waitUntil: "networkidle" });
  await page.waitForTimeout(1200);
  // The catalog fires an arrival toast 700ms after load (useCatalogBootstrap)
  // and it floats exactly over the panel's right end.
  await page.evaluate(() => {
    document.querySelectorAll("[class*='toast']").forEach((n) => n.remove());
  });
  const el = await page.$(`[data-t="${shot.id}"]`);
  if (!el) { console.log(`MISS ${shot.id}`); await page.close(); continue; }
  const out = `/tmp/${PREFIX}-${shot.name}.png`;
  await el.screenshot({ path: out });
  console.log("saved", out);
  await page.close();
}
await browser.close();
