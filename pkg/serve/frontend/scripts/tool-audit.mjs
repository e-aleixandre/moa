// tool-audit — CATALOG ONLY. Measures the tool-call rows in the live DOM so
// the review's claims are numbers, not impressions: which icons are literally
// identical, how many characters of an argument survive at 390px, which tap
// targets fall under 44px. Run by hand against catalog-serve on PORT.
import pw from "/home/ealeixandre/dev/moa/design-visual/tmp/redesign/fidelity/vendor/node_modules/playwright-core/index.js";
const { chromium } = pw;

const PORT = process.env.PORT || 7333;
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 390, height: 780 } });

const out = {};

for (const conv of ["reading", "editing", "failing"]) {
  await page.goto(`http://127.0.0.1:${PORT}/?view=toolsphone&conv=${conv}`, { waitUntil: "networkidle" });
  await page.waitForTimeout(900);

  // Expand every fold header so the audit sees every row, not just the tail.
  for (let i = 0; i < 12; i++) {
    const heads = await page.$$(".zl-lg-head");
    let clicked = false;
    for (const h of heads) {
      const t = await h.innerText();
      if (/earlier action/.test(t)) { await h.click(); clicked = true; await page.waitForTimeout(120); }
    }
    if (!clicked) break;
  }
  await page.waitForTimeout(400);

  out[conv] = await page.evaluate(() => {
    const rows = [...document.querySelectorAll(".zl-lg-row")];
    return rows.map((r) => {
      const tool = r.querySelector(".zl-lg-tool");
      const arg = r.querySelector(".zl-lg-arg");
      const svg = r.querySelector(".zl-tool-ico");
      const mark = r.querySelector(".zl-lg-mark");
      const rect = r.getBoundingClientRect();
      const clipped = (el) => (el ? el.scrollWidth > el.clientWidth + 1 : false);
      return {
        tool: tool ? tool.textContent : "",
        toolClipped: clipped(tool),
        toolShown: tool ? tool.clientWidth : 0,
        toolFull: tool ? tool.scrollWidth : 0,
        arg: arg ? arg.textContent : "",
        argClipped: clipped(arg),
        argShownPx: arg ? arg.clientWidth : 0,
        argFullPx: arg ? arg.scrollWidth : 0,
        out: (r.querySelector(".zl-lg-out") || {}).textContent || "",
        markClass: mark ? mark.className : "",
        icon: svg ? svg.innerHTML.replace(/\s+/g, " ").trim() : "",
        height: Math.round(rect.height),
        isButton: r.tagName === "BUTTON",
      };
    });
  });
}

// --- icons: which distinct tools render a byte-identical glyph -------------
const byIcon = new Map();
for (const conv of Object.keys(out)) {
  for (const r of out[conv]) {
    if (!r.icon) continue;
    if (!byIcon.has(r.icon)) byIcon.set(r.icon, new Set());
    byIcon.get(r.icon).add(r.tool);
  }
}
console.log("\n=== IDENTICAL ICONS (distinct tools sharing one glyph) ===");
let collisions = 0;
for (const [, tools] of byIcon) {
  if (tools.size > 1) { collisions++; console.log("  ", [...tools].sort().join("  ==  ")); }
}
console.log(`   ${collisions} collision group(s); ${byIcon.size} distinct glyphs for ${new Set([...byIcon.values()].flatMap(s=>[...s])).size} tools`);

// --- truncation at 390px ---------------------------------------------------
console.log("\n=== TRUNCATED AT 390px ===");
for (const conv of Object.keys(out)) {
  for (const r of out[conv]) {
    if (!r.argClipped && !r.toolClipped) continue;
    const pct = r.argFullPx ? Math.round((r.argShownPx / r.argFullPx) * 100) : 100;
    console.log(`   [${conv}] ${r.tool}${r.toolClipped ? " (NAME CLIPPED)" : ""} · arg shows ${pct}% · "${r.arg.slice(0, 46)}"`);
  }
}

// --- tap targets -----------------------------------------------------------
console.log("\n=== TAP TARGETS UNDER 44px ===");
let small = 0;
for (const conv of Object.keys(out)) {
  for (const r of out[conv]) {
    if (r.isButton && r.height < 44) { small++; console.log(`   [${conv}] ${r.tool} ${r.height}px`); }
  }
}
if (!small) console.log("   none — every interactive row is >= 44px");

// --- the `out` slot: what it actually says ---------------------------------
console.log("\n=== THE RIGHT-HAND `out` SLOT, ALL VALUES ===");
const outs = new Map();
for (const conv of Object.keys(out)) for (const r of out[conv]) {
  const k = r.out.trim() || "(empty)";
  outs.set(k, (outs.get(k) || 0) + 1);
}
console.log("  ", [...outs.entries()].map(([k, n]) => `${k}×${n}`).join("  |  "));

console.log("\n=== ROW COUNT ===");
for (const c of Object.keys(out)) console.log(`   ${c}: ${out[c].length} rows`);

await browser.close();
