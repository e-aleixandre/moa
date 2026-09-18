// u7-phone-shot — CATALOG ONLY. The lab page itself at 390x780: this is how
// the owner actually reads it, and the selector has to be usable there.
import pw from "/home/ealeixandre/dev/moa/design-visual/tmp/redesign/fidelity/vendor/node_modules/playwright-core/index.js";
const { chromium } = pw;

const PORT = process.env.PORT || 7333;
const browser = await chromium.launch({ args: ["--font-render-hinting=none", "--disable-lcd-text", "--force-color-profile=srgb", "--hide-scrollbars"] });
const page = await browser.newPage({ viewport: { width: 390, height: 780 }, deviceScaleFactor: 1, isMobile: true, hasTouch: true });
await page.goto(`http://127.0.0.1:${PORT}/?view=u7`, { waitUntil: "networkidle" });
await page.waitForTimeout(1500);
await page.evaluate(() => document.querySelectorAll("[class*='toast']").forEach((n) => n.remove()));
await page.screenshot({ path: "/tmp/user2-lab-en-el-telefono.png" });
console.log("saved /tmp/user2-lab-en-el-telefono.png");
// Tapping a chip must swap the frame without a reload.
await page.click("text=B3 · Burbuja");
await page.waitForTimeout(400);
await page.screenshot({ path: "/tmp/user2-lab-en-el-telefono-b3.png" });
console.log("saved /tmp/user2-lab-en-el-telefono-b3.png");
await browser.close();
