// tally2-shots — CATALOG ONLY. Round two: the three proposals at a real
// 390×780, the pair that decides the question (turn stopped with three live
// subagents beside a turn that is really running), and the strip of P2
// entering the conversation. Run by hand against catalog-serve on PORT (7300).
import pw from "/home/ealeixandre/dev/moa/design-visual/tmp/redesign/fidelity/vendor/node_modules/playwright-core/index.js";
const { chromium } = pw;

const PORT = process.env.PORT || 7300;
const browser = await chromium.launch({
  args: ["--font-render-hinting=none", "--disable-lcd-text", "--force-color-profile=srgb", "--hide-scrollbars"],
});
const page = await browser.newPage({ viewport: { width: 1360, height: 1200 }, deviceScaleFactor: 2 });

async function open(url) {
  await page.goto(url, { waitUntil: "networkidle" });
  await page.waitForTimeout(1200);
  await page.evaluate(() => document.querySelectorAll("[class*='toast']").forEach((n) => n.remove()));
}

// The decisive pair, per proposal.
await open(`http://127.0.0.1:${PORT}/?view=tally2&shots=pair`);
for (const d of ["p1", "p2", "p3"]) {
  const el = await page.$(`[data-pair="${d}"]`);
  if (!el) { console.log("MISS pair", d); continue; }
  await el.screenshot({ path: `/tmp/tally2-pair-${d}.png` });
  console.log("saved", `/tmp/tally2-pair-${d}.png`);
}

// Entering with the turn stopped: the unfold, in three frames.
await open(`http://127.0.0.1:${PORT}/?view=tally2&shots=enter`);
const strip = await page.$("[data-pair='enter']");
if (strip) {
  await strip.screenshot({ path: "/tmp/tally2-enter.png" });
  console.log("saved /tmp/tally2-enter.png");
}

// One contact sheet per state: the same state in all three, side by side.
await open(`http://127.0.0.1:${PORT}/?view=tally2&shots=state`);
for (const s of ["working", "mixed", "stopped", "child", "crowd"]) {
  const el = await page.$(`[data-sheet="${s}"]`);
  if (!el) { console.log("MISS sheet", s); continue; }
  await el.screenshot({ path: `/tmp/tally2-sheet-${s}.png` });
  console.log("saved", `/tmp/tally2-sheet-${s}.png`);
}

// Unfolded, which is where P1 and P3 part company, and the desktop column.
for (const d of ["p1", "p2", "p3"]) {
  await open(`http://127.0.0.1:${PORT}/?view=tally2&dir=${d}&state=stopped`);
  const tally = await page.$(".tb-stage .zl-live-tally");
  if (tally && d !== "p2") await tally.click();
  await page.waitForTimeout(400);
  const el = await page.$(`[data-tally="${d}-stopped"]`);
  if (el) {
    await el.screenshot({ path: `/tmp/tally2-open-${d}.png` });
    console.log("saved", `/tmp/tally2-open-${d}.png`);
  }
  const desk = await page.$(`[data-tally="desk-${d}-stopped"]`);
  if (desk) {
    await desk.screenshot({ path: `/tmp/tally2-desk-${d}.png` });
    console.log("saved", `/tmp/tally2-desk-${d}.png`);
  }
}

await browser.close();
