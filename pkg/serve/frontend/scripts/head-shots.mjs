// head-shots — CATALOG ONLY. Captures the five floating-header treatments at a
// real 390x780, and MEASURES them off the painted pixels.
//
// The measurement is the point. Every number in head-lab.jsx's MEASURED table
// is sampled here from the real compositor output, not computed from the CSS:
// the capsule is a translucent, backdrop-filtered surface over a gradient
// aurora over a transcript, and what that resolves to is a compositing result
// no arithmetic on the token values can honestly predict.
//
// Two numbers per variant:
//   tone  — the capsule's painted fill vs the canvas immediately beside it, as
//           a WCAG contrast ratio. This is the owner's complaint expressed as a
//           number: 1.0 means "the same paint", which is what today's is close
//           to. It is used as a separation index, not as a text-legibility
//           threshold — WCAG has nothing to say about two adjacent surfaces.
//   title — the title glyphs against the capsule fill they sit on, which is a
//           real WCAG reading and must stay far above 4.5:1 in all five.
//
// Not part of the build; run by hand against `catalog-serve` on PORT.
import pw from "/home/ealeixandre/dev/moa/design-visual/tmp/redesign/fidelity/vendor/node_modules/playwright-core/index.js";
const { chromium } = pw;

const PORT = process.env.PORT || 7333;
const VARIANTS = ["ref", "rung", "lift", "edge", "presence"];

const browser = await chromium.launch({
  args: ["--font-render-hinting=none", "--disable-lcd-text", "--force-color-profile=srgb", "--hide-scrollbars"],
});

// deviceScaleFactor 1 for the measuring pass: at 2 the sampler would read
// interpolated subpixels and the tone step under test is smaller than that
// error. The pretty pass below re-shoots at 2.
const page = await browser.newPage({ viewport: { width: 390, height: 780 }, deviceScaleFactor: 1 });

async function open(url) {
  await page.goto(url, { waitUntil: "networkidle" });
  await page.waitForTimeout(1200);
  await page.evaluate(() => document.querySelectorAll("[class*='toast']").forEach((n) => n.remove()));
  await page.waitForTimeout(300);
}

const lin = (c) => { c /= 255; return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4); };
const L = ([r, g, b]) => 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
const CR = (a, b) => { const [x, y] = [L(a), L(b)].sort((p, q) => q - p); return (x + 0.05) / (y + 0.05); };

// Sample the real pixels of a screenshot buffer through the page itself: the
// browser decodes the PNG and hands back raw RGBA, which avoids pulling an
// image library in for five numbers.
async function sample(buf) {
  const b64 = buf.toString("base64");
  return page.evaluate(async (data) => {
    const img = new Image();
    img.src = "data:image/png;base64," + data;
    await img.decode();
    const c = document.createElement("canvas");
    c.width = img.width; c.height = img.height;
    const ctx = c.getContext("2d", { willReadFrequently: true });
    ctx.drawImage(img, 0, 0);
    const px = (x, y) => { const d = ctx.getImageData(x, y, 1, 1).data; return [d[0], d[1], d[2]]; };

    // The title capsule spans the middle of the row. Its vertical centre is
    // the capsule row's own centre; the canvas is sampled from the 8px gutter
    // between two capsules, at the SAME y, so the pair differs only by the
    // capsule and never by what the transcript happens to be showing.
    const rows = [];
    for (let y = 0; y < img.height; y++) rows.push(y);
    return { w: img.width, h: img.height, px: null, data: Array.from(ctx.getImageData(0, 0, img.width, img.height).data) };
  }, b64);
}

const results = {};

// Measuring the "canvas" from the 8px gutter BETWEEN two capsules was wrong,
// and the first run showed it: the sample came out #1e2333 for the reference
// but #181b29 for Lift and #191d2b for Edge. The transcript had not moved —
// scrollTop is 431 in all five — so the difference was each variant's OWN drop
// shadow spilling into the gap. That flatters the tonal variants and penalises
// the elevation ones, which is exactly backwards.
//
// The honest baseline is what is behind the capsule when the capsule is not
// there. So the reference plate is captured once with the header hidden, and
// every variant's capsule is compared against the SAME pixels it is covering.
// That is the number the owner's complaint is about: capsule versus the thing
// it is supposed to sit on top of.
let PLATE = null;
async function referencePlate() {
  await open(`http://127.0.0.1:${PORT}/?view=headphone&v=ref`);
  await page.evaluate(() => {
    const c = document.querySelector(".zl-chrome");
    if (c) c.style.visibility = "hidden";
  });
  await page.waitForTimeout(350);
  const shot = await page.screenshot();
  const { data, w } = await sample(shot);
  PLATE = { data, w };
}
await referencePlate();

for (const v of VARIANTS) {
  await open(`http://127.0.0.1:${PORT}/?view=headphone&v=${v}`);

  // Geometry straight from the live layout, so the sampler never guesses.
  const geo = await page.evaluate(() => {
    const chip = document.querySelector(".zl-chip");
    const name = document.querySelector(".zl-chip-name");
    const r = chip.getBoundingClientRect();
    const nr = name.getBoundingClientRect();
    return {
      capX: Math.round(r.left + 12), capY: Math.round(r.top + r.height / 2),
      capT: Math.round(r.top), capB: Math.round(r.bottom),
      nameL: Math.round(nr.left), nameR: Math.round(nr.right),
      nameT: Math.round(nr.top), nameB: Math.round(nr.bottom),
    };
  });

  const shot = await page.screenshot();
  const { data, w } = await sample(shot);
  const at = (x, y) => { const i = (y * w + x) * 4; return [data[i], data[i + 1], data[i + 2]]; };
  // The same coordinate, on the plate captured with no header at all.
  const plateAt = (x, y) => {
    const i = (y * PLATE.w + x) * 4;
    return [PLATE.data[i], PLATE.data[i + 1], PLATE.data[i + 2]];
  };

  const cap = at(geo.capX, geo.capY);
  const canvas = plateAt(geo.capX, geo.capY);

  // Fill tone alone cannot see what Lift and Edge do: both keep production's
  // fill exactly, so they score ~1.01 on the number above and yet they are not
  // the same design. What they change is the TRANSITION at the capsule's
  // boundary — a shadow below it, a highlight along its top. So a second
  // number: the largest luminance jump between adjacent pixels on a vertical
  // line crossing the capsule's top and bottom edges, as a percentage of the
  // canvas luminance. A hard border would score very high here, which is the
  // point — it is the axis the owner said he did not want pushed.
  const edgeStep = (yFrom, yTo, x) => {
    let worst = 0;
    for (let y = yFrom; y < yTo; y++) {
      const d = Math.abs(L(at(x, y + 1)) - L(at(x, y)));
      if (d > worst) worst = d;
    }
    return worst;
  };
  const base = L(canvas);
  const topStep = edgeStep(geo.capT - 8, geo.capT + 4, geo.capX);
  const botStep = edgeStep(geo.capB - 4, geo.capB + 10, geo.capX);

  // The title's ink: the darkest-to-lightest extreme inside the name's box.
  // Glyphs are antialiased, so the single lightest pixel found across the run
  // is the one that actually carries the letterform.
  let ink = cap;
  for (let y = geo.nameT; y < geo.nameB; y++) {
    for (let x = geo.nameL; x < Math.min(geo.nameR, geo.nameL + 160); x++) {
      const p = at(x, y);
      if (L(p) > L(ink)) ink = p;
    }
  }

  results[v] = {
    capsule: cap, canvas, ink,
    tone: CR(cap, canvas),
    title: CR(ink, cap),
    topEdge: topStep / base,
    botEdge: botStep / base,
  };

  const hx = (a) => "#" + a.map((c) => c.toString(16).padStart(2, "0")).join("");
  console.log(
    `${v.padEnd(9)} capsule ${hx(cap)}  canvas ${hx(canvas)}  ` +
    `tone ${CR(cap, canvas).toFixed(3)}:1   title ${CR(ink, cap).toFixed(2)}:1   ` +
    `edge top ${(topStep / base * 100).toFixed(0)}% bottom ${(botStep / base * 100).toFixed(0)}%`
  );
}

console.log("\nMEASURED = " + JSON.stringify(
  Object.fromEntries(Object.entries(results).map(([k, r]) => [k, { tone: r.tone.toFixed(2) + ":1", title: r.title.toFixed(1) + ":1" }])),
  null, 2
));

// ── The pretty pass: the five phones at 2x, for the contact sheet ─────────
await page.setViewportSize({ width: 390, height: 780 });
const hi = await browser.newPage({ viewport: { width: 390, height: 780 }, deviceScaleFactor: 2 });
let firstTop = null;
for (const v of VARIANTS) {
  await hi.goto(`http://127.0.0.1:${PORT}/?view=headphone&v=${v}`, { waitUntil: "networkidle" });
  await hi.waitForTimeout(1200);
  await hi.evaluate(() => document.querySelectorAll("[class*='toast']").forEach((n) => n.remove()));
  // The lab's own switcher and readout are chrome, not the design: they are
  // removed before the shot so the header is judged against the transcript.
  await hi.evaluate(() => {
    document.querySelectorAll(".head-pick, .head-readout").forEach((n) => (n.style.display = "none"));
  });
  // Same scroll position for all five: the shots must differ by the header
  // and by nothing else, so the words underneath have to be identical. The
  // lab parks itself on the hard case at mount, and the transcript is short
  // enough that scrollTop clamps to the same 431 every time — asserted rather
  // than assumed, because a drifting baseline is what broke the first run.
  const top = await hi.evaluate(() => document.querySelector(".zl-transcript").scrollTop);
  if (v !== VARIANTS[0] && top !== firstTop) {
    console.log(`WARN ${v} parked at ${top}, expected ${firstTop} — shots not comparable`);
  }
  if (v === VARIANTS[0]) firstTop = top;
  await hi.waitForTimeout(300);
  await hi.screenshot({ path: `/tmp/head-${v}.png` });
  console.log("saved", `/tmp/head-${v}.png`);

  // A tight crop of the header band alone, where the difference lives. Five
  // full phones side by side make the capsule 40px tall on the contact sheet,
  // which is too small to see a tonal step.
  await hi.screenshot({ path: `/tmp/head-${v}-band.png`, clip: { x: 0, y: 0, width: 390, height: 132 } });
  console.log("saved", `/tmp/head-${v}-band.png`);
}

await browser.close();
