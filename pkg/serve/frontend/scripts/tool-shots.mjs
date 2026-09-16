// tool-shots — CATALOG ONLY. Captures the tool-call conversations at a real
// 390x780, plus the desktop column, straight to /tmp/tool-*.png.
// Not part of the build; run by hand against `catalog-serve` on PORT.
import pw from "/home/ealeixandre/dev/moa/design-visual/tmp/redesign/fidelity/vendor/node_modules/playwright-core/index.js";
const { chromium } = pw;

const PORT = process.env.PORT || 7333;
const CONVS = (process.env.CONVS || "reading,editing,failing").split(",");

const browser = await chromium.launch({
  args: ["--font-render-hinting=none", "--disable-lcd-text", "--force-color-profile=srgb", "--hide-scrollbars"],
});
const page = await browser.newPage({ viewport: { width: 1500, height: 1400 }, deviceScaleFactor: 2 });

// The catalog fires an arrival toast 700ms after load (useCatalogBootstrap);
// it floats over the frames, so it is removed before anything is shot.
async function open(url) {
  await page.goto(url, { waitUntil: "networkidle" });
  await page.waitForTimeout(1500);
  await page.evaluate(() => document.querySelectorAll("[class*='toast']").forEach((n) => n.remove()));
}

// Pass 1 — ?shots=1: the frames at 1:1 and nothing else on the page.
await open(`http://127.0.0.1:${PORT}/?view=tools&shots=1`);
for (const c of CONVS) {
  for (const pinned of ["top", "bottom"]) {
    const el = await page.$(`[data-tools="${c}-${pinned}"]`);
    if (!el) { console.log(`MISS ${c}-${pinned}`); continue; }
    await el.screenshot({ path: `/tmp/tool-phone-${c}-${pinned}.png` });
    console.log("saved", `/tmp/tool-phone-${c}-${pinned}.png`);
  }
  const desk = await page.$(`[data-tools="desk-${c}"]`);
  if (!desk) { console.log(`MISS desk-${c}`); continue; }
  await desk.screenshot({ path: `/tmp/tool-desk-${c}.png` });
  console.log("saved", `/tmp/tool-desk-${c}.png`);
}

// Pass 2 — the standalone phone, scrolled through, so the middle of a long
// transcript (the 12-call fold, the 2000-line output) is actually seen and not
// just its two ends.
for (const c of CONVS) {
  await open(`http://127.0.0.1:${PORT}/?view=toolsphone&conv=${c}`);
  await page.setViewportSize({ width: 390, height: 780 });
  await page.waitForTimeout(600);
  const steps = ["0", "0.5", "1"];
  for (const k of steps) {
    await page.evaluate((frac) => {
      const el = document.querySelector(".zl-transcript");
      if (!el) return;
      el.scrollTop = (el.scrollHeight - el.clientHeight) * Number(frac);
    }, k);
    await page.waitForTimeout(350);
    const name = k === "0" ? "head" : k === "0.5" ? "mid" : "tail";
    await page.screenshot({ path: `/tmp/tool-scroll-${c}-${name}.png` });
    console.log("saved", `/tmp/tool-scroll-${c}-${name}.png`);
  }
  await page.setViewportSize({ width: 1500, height: 1400 });
}

await browser.close();
