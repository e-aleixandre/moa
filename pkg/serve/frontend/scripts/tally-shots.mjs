// tally-shots — CATALOG ONLY. Captures the five directions across the five
// states at a real 390×780, plus the pair that decides the question: the turn
// stopped with three live subagents beside a turn that is really running.
// Run by hand against catalog-serve on PORT (7300 by default).
import pw from "/home/ealeixandre/dev/moa/design-visual/tmp/redesign/fidelity/vendor/node_modules/playwright-core/index.js";
const { chromium } = pw;

const PORT = process.env.PORT || 7300;
const browser = await chromium.launch({
  args: ["--font-render-hinting=none", "--disable-lcd-text", "--force-color-profile=srgb", "--hide-scrollbars"],
});
const page = await browser.newPage({ viewport: { width: 2400, height: 1400 }, deviceScaleFactor: 2 });

async function open(url) {
  await page.goto(url, { waitUntil: "networkidle" });
  await page.waitForTimeout(1500);
  await page.evaluate(() => document.querySelectorAll("[class*='toast']").forEach((n) => n.remove()));
}

// Pass 1 — one contact sheet per state: the same state in all five directions,
// side by side, which is the only way to compare them.
await open(`http://127.0.0.1:${PORT}/?view=tally&shots=state`);
for (const s of ["working", "mixed", "stopped", "ended", "crowd"]) {
  const el = await page.$(`[data-sheet="${s}"]`);
  if (!el) { console.log("MISS sheet", s); continue; }
  await el.screenshot({ path: `/tmp/tally-sheet-${s}.png` });
  console.log("saved", `/tmp/tally-sheet-${s}.png`);
}
// And every frame on its own, at 1:1.
for (const d of ["a", "b", "c", "d", "e"]) {
  for (const s of ["working", "mixed", "stopped", "ended", "crowd"]) {
    const el = await page.$(`[data-tally="${d}-${s}"]`);
    if (!el) { console.log("MISS", d, s); continue; }
    await el.screenshot({ path: `/tmp/tally-${d}-${s}.png` });
  }
}
console.log("saved /tmp/tally-<dir>-<state>.png");

// Pass 2 — the decisive pair, per direction.
await open(`http://127.0.0.1:${PORT}/?view=tally&shots=pair`);
for (const d of ["a", "b", "c", "d", "e"]) {
  const el = await page.$(`[data-pair="${d}"]`);
  if (!el) { console.log("MISS pair", d); continue; }
  await el.screenshot({ path: `/tmp/tally-pair-${d}.png` });
  console.log("saved", `/tmp/tally-pair-${d}.png`);
}

// Pass 3 — what ships today, in the same pair, as the reference to beat.
await open(`http://127.0.0.1:${PORT}/?view=tally&shots=today`);
const today = await page.$("[data-pair='today']");
if (today) {
  await today.screenshot({ path: "/tmp/tally-pair-today.png" });
  console.log("saved /tmp/tally-pair-today.png");
}

// Pass 4 — the desktop column, one per direction, in the stopped state.
for (const d of ["a", "b", "c", "d", "e"]) {
  await open(`http://127.0.0.1:${PORT}/?view=tally&dir=${d}&state=stopped`);
  const el = await page.$(`[data-tally="desk-${d}-stopped"]`);
  if (!el) { console.log("MISS desk", d); continue; }
  await el.screenshot({ path: `/tmp/tally-desk-${d}.png` });
  console.log("saved", `/tmp/tally-desk-${d}.png`);
}

await browser.close();
