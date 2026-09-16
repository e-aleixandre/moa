// led-shots — CATALOG ONLY. Before/after evidence for the ledger row fixes.
// Shoots the five scenes the review asked for at a real 390x780, straight to
// /tmp/led-<phase>-*.png. Run by hand against catalog-serve on PORT.
//
//   PHASE=before node scripts/led-shots.mjs
//   PHASE=after  node scripts/led-shots.mjs
//
// Every scene is located by CONTENT, not by index: the row that loses its name
// is found by its command text, so the same scene is captured before and after
// even though the fixes change how many rows are drawn.
import pw from "/home/ealeixandre/dev/moa/design-visual/tmp/redesign/fidelity/vendor/node_modules/playwright-core/index.js";
const { chromium } = pw;

const PORT = process.env.PORT || 7333;
const PHASE = process.env.PHASE || "after";
const out = (n) => `/tmp/led-${PHASE}-${n}.png`;

const browser = await chromium.launch({
  args: ["--font-render-hinting=none", "--disable-lcd-text", "--force-color-profile=srgb", "--hide-scrollbars"],
});
const page = await browser.newPage({ viewport: { width: 390, height: 780 }, deviceScaleFactor: 2 });

async function open(conv) {
  await page.goto(`http://127.0.0.1:${PORT}/?view=toolsphone&conv=${conv}`, { waitUntil: "networkidle" });
  await page.waitForTimeout(1200);
  await page.evaluate(() => document.querySelectorAll("[class*='toast']").forEach((n) => n.remove()));
}

// expandFolds opens every folded ("N earlier · …") header so a scene inside a folded
// group is reachable. Bounded: a fold that reappears is a bug, not a loop.
async function expandFolds() {
  for (let i = 0; i < 12; i++) {
    const heads = await page.$$(".zl-lg-head");
    let clicked = false;
    for (const h of heads) {
      const t = await h.innerText();
      if (/\bearlier\b/i.test(t)) { await h.click(); clicked = true; await page.waitForTimeout(140); }
    }
    if (!clicked) break;
  }
  await page.waitForTimeout(400);
}

// shotLedgerContaining screenshots the whole ledger card that holds a row
// matching `needle`, with a little air around it.
async function shotLedgerContaining(needle, name, pad = 10) {
  const box = await page.evaluate((text) => {
    const rows = [...document.querySelectorAll(".zl-lg-row")];
    const hit = rows.find((r) => r.innerText.includes(text));
    if (!hit) return null;
    const card = hit.closest(".zl-ledger") || hit;
    card.scrollIntoView({ block: "center" });
    const r = card.getBoundingClientRect();
    return { x: r.x, y: r.y, width: r.width, height: r.height };
  }, needle);
  if (!box) { console.log(`MISS ${name} (${needle})`); return false; }
  await page.waitForTimeout(300);
  const fresh = await page.evaluate((text) => {
    const rows = [...document.querySelectorAll(".zl-lg-row")];
    const hit = rows.find((r) => r.innerText.includes(text));
    const card = hit.closest(".zl-ledger") || hit;
    const r = card.getBoundingClientRect();
    return { x: r.x, y: r.y, width: r.width, height: r.height };
  }, needle);
  const clip = {
    x: Math.max(0, fresh.x - pad),
    y: Math.max(0, fresh.y - pad),
    width: Math.min(390, fresh.width + pad * 2),
    height: Math.min(780, fresh.height + pad * 2),
  };
  if (clip.height <= 0 || clip.width <= 0) { console.log(`MISS ${name} (offscreen)`); return false; }
  await page.screenshot({ path: out(name), clip });
  console.log("saved", out(name));
  return true;
}

/* 1 — the row that lost its name: the one-line `git log --oneline` bash whose
      raw result was eating the whole label. */
await open("reading");
await expandFolds();
await shotLedgerContaining("git log --oneline", "1-lost-name");

/* 2 — a group carrying both an error and a rejected call. */
await open("failing");
await expandFolds();
await shotLedgerContaining("sudo systemctl stop pulse-api", "2-error-rejected");

/* 4 — a long group, FOLDED: the tail plus the header that hides the rest. */
await open("reading");
await page.evaluate(() => {
  const rows = [...document.querySelectorAll(".zl-lg-head")];
  const hit = rows.find((r) => /\bearlier\b/i.test(r.innerText));
  if (hit) (hit.closest(".zl-ledger") || hit).scrollIntoView({ block: "center" });
});
await page.waitForTimeout(400);
{
  const box = await page.evaluate(() => {
    const heads = [...document.querySelectorAll(".zl-lg-head")];
    const hit = heads.find((h) => /\bearlier\b/i.test(h.innerText));
    if (!hit) return null;
    const r = (hit.closest(".zl-ledger") || hit).getBoundingClientRect();
    return { x: r.x, y: r.y, width: r.width, height: r.height };
  });
  if (box) {
    await page.screenshot({
      path: out("4-folded-group"),
      clip: { x: Math.max(0, box.x - 10), y: Math.max(0, box.y - 10), width: Math.min(390, box.width + 20), height: Math.min(780 - Math.max(0, box.y - 10), box.height + 20) },
    });
    console.log("saved", out("4-folded-group"));
  } else console.log("MISS 4-folded-group");
}

/* 5 — a live row, with its streaming tail. */
await open("failing");
await page.evaluate(() => {
  const el = document.querySelector(".zl-transcript");
  if (el) el.scrollTop = el.scrollHeight;
});
await page.waitForTimeout(700);
{
  const box = await page.evaluate(() => {
    const live = document.querySelector(".zl-lg-row.is-live");
    if (!live) return null;
    const r = (live.closest(".zl-ledger") || live).getBoundingClientRect();
    return { x: r.x, y: r.y, width: r.width, height: r.height };
  });
  if (box) {
    await page.screenshot({
      path: out("5-live-row"),
      clip: { x: Math.max(0, box.x - 10), y: Math.max(0, box.y - 10), width: Math.min(390, box.width + 20), height: Math.min(780 - Math.max(0, box.y - 10), box.height + 20) },
    });
    console.log("saved", out("5-live-row"));
  } else console.log("MISS 5-live-row");
}

/* 3 — the icon table: all 20 tools at real size, one glyph per row, drawn by
      mounting the shipped ActivityLedger through the catalogue's own route so
      nothing here re-implements a row. Uses ?view=ledgericons. */
await page.goto(`http://127.0.0.1:${PORT}/?view=ledgericons`, { waitUntil: "networkidle" });
await page.setViewportSize({ width: 390, height: 1100 });
await page.waitForTimeout(1000);
await page.evaluate(() => document.querySelectorAll("[class*='toast']").forEach((n) => n.remove()));
{
  const el = await page.$("[data-led-icons]");
  if (el) { await el.screenshot({ path: out("3-icons") }); console.log("saved", out("3-icons")); }
  else console.log("MISS 3-icons");
}

await browser.close();
