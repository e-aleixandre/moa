// head-sheet — CATALOG ONLY. The contact sheet for the floating-header study.
//
// Two rows on one page, because the two things being judged are judged at
// different scales: the top row is the five phones whole (does it still feel
// like a header floating over a conversation?) and the bottom row is the
// header band alone at 2x (is the capsule a separate object?). A tonal step of
// this size is invisible in a 390px-wide thumbnail, which is why the band crop
// exists at all.
//
// Reads the PNGs head-shots.mjs already wrote; measures nothing itself.
import pw from "/home/ealeixandre/dev/moa/design-visual/tmp/redesign/fidelity/vendor/node_modules/playwright-core/index.js";
import { readFileSync } from "fs";
const { chromium } = pw;

const V = [
  ["ref", "Now", "1.01:1", "the same paint — the complaint"],
  ["rung", "Rung", "1.25:1", "tonal step · recommended"],
  ["lift", "Lift", "1.01:1", "elevation only · shadow does the work"],
  ["edge", "Edge light", "1.01:1", "top highlight · closest to a border"],
  ["presence", "Presence", "1.07:1", "tone + light + type"],
];

const b64 = (p) => "data:image/png;base64," + readFileSync(p).toString("base64");

// Two labelled rows rather than two images stacked per card. Stacking them put
// the band crop directly above a full phone that also starts with a header, so
// every card showed the capsule twice and read as a rendering fault.
const bands = V.map(([id, label, tone]) => `
  <figure class="card">
    <img class="band" src="${b64(`/tmp/head-${id}-band.png`)}" alt="${label} header band">
    <figcaption><b>${label}</b><span class="tone">vs what it covers · <em>${tone}</em></span></figcaption>
  </figure>`).join("");

const fulls = V.map(([id, label, , note]) => `
  <figure class="card">
    <img class="full" src="${b64(`/tmp/head-${id}.png`)}" alt="${label} full phone">
    <figcaption><b>${label}</b><span class="note">${note}</span></figcaption>
  </figure>`).join("");

const html = `<!doctype html><meta charset="utf-8">
<style>
  :root { color-scheme: dark; }
  body {
    margin: 0; padding: 40px 32px 56px;
    background: #101018; color: #f4f4f7;
    font: 14px/1.6 ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif;
  }
  h1 { font-size: 22px; letter-spacing: -0.02em; margin: 0 0 8px; }
  .lede { color: #c3c4d1; max-width: 90ch; margin: 0 0 6px; }
  .sub { color: #9091a6; max-width: 90ch; margin: 0 0 32px; font-size: 13px; }
  code { font-family: ui-monospace, SFMono-Regular, monospace; color: #c3c4d1; }
  .row { display: flex; gap: 20px; align-items: flex-start; }
  .card { margin: 0; flex: 1; min-width: 0; }
  .band {
    width: 100%; display: block; border-radius: 10px;
    border: 1px solid rgba(255,255,255,.12);
  }
  .full {
    width: 100%; display: block; border-radius: 10px;
    border: 1px solid rgba(255,255,255,.07);
  }
  h2 {
    font-size: 13px; font-weight: 600; letter-spacing: .08em; text-transform: uppercase;
    color: #9091a6; margin: 36px 0 14px;
  }
  h2 span { text-transform: none; letter-spacing: 0; font-weight: 400; margin-left: 10px; }
  figcaption { display: flex; flex-direction: column; gap: 3px; margin-top: 10px; }
  figcaption b { font-size: 15px; }
  .tone { color: #9091a6; font-size: 12px; }
  .tone em { font-style: normal; font-family: ui-monospace, monospace; color: #cba6f7; }
  .note { color: #c3c4d1; font-size: 12px; }
</style>
<h1>The floating header — five ways, same conversation</h1>
<p class="lede">
  The complaint is not transparency: it is that the capsule is the same tone as
  what is behind it, with nothing to detach it — and a hard border is explicitly
  not wanted. Blur is held at production&rsquo;s 12px in all five, reference
  included, so the only differences here are tone, elevation, edge light and
  type weight.
</p>
<p class="sub">
  Every number is sampled off the painted pixels against a plate captured with
  the header hidden, so each variant is measured against the same pixels it
  covers. Top strip is the header band at 2&times;; below it the whole 390&times;780
  phone, same scroll position, same words underneath.
  Title legibility stays between <code>11.5:1</code> and <code>14.2:1</code> in
  all five — none of these costs readability.
</p>
<h2>The header band, 2&times; <span>&mdash; is the capsule a separate object?</span></h2>
<div class="row">${bands}</div>
<h2>The whole phone, 390&times;780 <span>&mdash; does it still float over a conversation?</span></h2>
<div class="row">${fulls}</div>`;

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 2100, height: 1400 }, deviceScaleFactor: 2 });
await page.setContent(html, { waitUntil: "load" });
await page.waitForTimeout(600);
await page.screenshot({ path: "/tmp/head-contact-sheet.png", fullPage: true });
console.log("saved /tmp/head-contact-sheet.png");
await browser.close();
