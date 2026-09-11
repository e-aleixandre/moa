#!/usr/bin/env node
// fidelity — the pixel comparator for the catalogue → production migration.
//
// The method (tmp/redesign/fidelity/METODO.md): the catalogue as it stands is
// the accepted design, so it is captured as a GOLDEN. A piece is migrated when
// its private copy in zones-lab.jsx is gone and the catalogue imports the
// production component instead — and the proof that the substitution changed
// nothing visible is that the same capture still matches the golden. The
// criterion stops being an opinion and becomes a number.
//
//   node scripts/fidelity.mjs                  compare every scene
//   node scripts/fidelity.mjs --piece live-zone   only that piece's scenes
//   node scripts/fidelity.mjs --scene phone-idle  one scene
//   node scripts/fidelity.mjs --update         (re)write the goldens
//   node scripts/fidelity.mjs --list           the matrix, nothing else
//
// --update is explicit and never implicit: a comparator that regenerates its
// own reference when it fails proves nothing. The one exception is a scene
// with no golden at all, which is reported as NEW rather than as a pass.
//
// Output (all of it outside the repo, in tmp/, which is gitignored):
//   tmp/redesign/fidelity/golden/<scene>.png     the reference
//   tmp/redesign/fidelity/actual/<scene>.png     this run
//   tmp/redesign/fidelity/diff/<scene>.png       pixelmatch's mask
//   tmp/redesign/fidelity/triptych/<scene>.png   golden | actual | diff
//   tmp/redesign/fidelity/STATUS.md              generated, never edited
//
// The triptych is the deliverable, not the percentage: a number says something
// moved, the three panels say what.

import { mkdirSync, writeFileSync, readFileSync, existsSync, rmSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { createRequire } from "node:module";

const here = dirname(fileURLToPath(import.meta.url));
const frontend = resolve(here, "..");
const OUT = resolve(frontend, "../../../tmp/redesign/fidelity");

// playwright-core and pixelmatch are NOT in pkg/serve/frontend/node_modules, on
// purpose, and this is the one thing about this script worth knowing before
// running it. In this worktree that directory is a symlink into the main
// worktree, so `npm i -D` there installs into somebody else's tree; and
// package.json is what CI runs `npm ci` against, so putting browser tooling in
// it makes every CI job pay for a comparator that only runs by hand. They live
// in tmp/redesign/fidelity/vendor/ instead — gitignored, beside the goldens:
//
//   cd tmp/redesign/fidelity/vendor && npm i
//
// If the owner later wants this in CI, it becomes two lines here plus the two
// devDependencies in package.json.
const VENDOR = join(OUT, "vendor");

// Resolve from the vendor directory, falling back to a normal resolution so
// the script still works if someone does put these in package.json one day.
const requireVendor = createRequire(join(VENDOR, "noop.cjs"));

async function dep(name) {
  let target = name;
  try {
    target = pathToFileURL(requireVendor.resolve(name)).href;
  } catch {
    // not vendored; try the ordinary resolution below
  }
  try {
    const mod = await import(target);
    // Resolving by path bypasses the package's "exports" map, so a CJS main
    // (playwright-core/index.js, pngjs) arrives with its real surface under
    // .default while an ESM one (pixelmatch) does not. Merging both keeps the
    // call sites from having to know which is which.
    const base = mod.default && typeof mod.default === "object" ? mod.default : {};
    return { ...base, ...mod };
  } catch {
    console.error(
      `fidelity: missing dependency "${name}".\n` +
        `  the harness deps are deliberately not in package.json; install them with:\n` +
        `    cd ${VENDOR} && npm i`,
    );
    process.exit(2);
  }
}

const { chromium } = await dep("playwright-core");
const pixelmatch = (await dep("pixelmatch")).default;
const { PNG } = await dep("pngjs");

// The scene matrix lives beside the scene component, not here: the browser
// bundle and this script must not be able to disagree about what a scene is.
const { SCENES, sceneViewport, sceneTarget } = await import(
  pathToFileURL(join(frontend, "src/catalog/scenes.js")).href
);

const args = process.argv.slice(2);
const flag = (name) => args.includes(`--${name}`);
const value = (name) => {
  const i = args.indexOf(`--${name}`);
  return i >= 0 ? args[i + 1] : null;
};

const UPDATE = flag("update");
const PIECE = value("piece");
const ONLY = value("scene");
const BASE = value("base") || process.env.FIDELITY_BASE || "http://127.0.0.1:7300";

// Tolerances. METODO §2: "0 %, tolerance ≤0.1 % for antialiasing, any cluster
// is a failure".
//
// The cluster rule is the one that matters, and its limit was calibrated
// against a measurement rather than guessed. Moving .zl-chrome by 4px — one
// declaration, the phone's floating capsules — produces on phone-panel a diff
// of 0.039 % (120 px, under the percentage tolerance) whose largest connected
// run is **26 px**: the drawer covers most of the capsules, so only the corner
// of the burger icon is left showing. A limit of 200 px, which looked sensible
// in the abstract, let four scenes report PASS on that change.
//
// So: 16 px, roughly a 4x4 block. Below that is where genuine antialiasing
// noise lives (a run of changed pixels along one glyph edge); at or above it,
// something moved. The determinism run measures 0 px on every scene, so there
// is no noise floor to clear at all on this machine — the limit exists for the
// day the harness runs somewhere with a different font stack.
const TOLERANCE_PCT = 0.1;
const CLUSTER_PX = 16;
// pixelmatch's own threshold: how different two pixels must be to count at all.
// It is 0 here, not its 0.1 default, and that is not pedantry -- it is the
// difference between a comparator that works and one that lies.
//
// Measured: rounding .zl-send from 10px to 2px changes 132 pixels, plainly
// visible as a square button turning sharp. At threshold 0.1 pixelmatch
// reports ZERO of them, and the harness printed "0.000 % pass" on a change I
// had made on purpose to test it. The reason is that the corner pixels differ
// only by the button's own fill bleeding into its background -- a small
// per-pixel delta, which is exactly what the threshold throws away. The
// calibration that set 0.1 used a 4px MOVE, where displaced glyphs produce
// large per-pixel deltas; it never saw a colour-sized one.
//
// The noise this threshold exists to absorb is antialiasing, and the
// determinism run measures 0 changed pixels on all 29 scenes: on this harness,
// with the clock frozen and motion disabled, there is no noise to absorb. So
// every differing pixel counts, and the cluster rule below is what separates a
// real change from a stray edge.
const PIXEL_THRESHOLD = 0;

function pick() {
  let list = SCENES;
  if (PIECE) list = list.filter((s) => s.piece === PIECE);
  if (ONLY) list = list.filter((s) => s.name === ONLY);
  return list;
}

if (flag("list")) {
  const rows = pick();
  const w = Math.max(...rows.map((s) => s.name.length));
  for (const s of rows) {
    const { width, height } = sceneViewport(s);
    console.log(
      `${s.name.padEnd(w)}  ${String(s.piece).padEnd(14)} ${s.host.padEnd(10)} ` +
        `${width}x${height}  ${sceneUrl(s).slice(BASE.length)}`,
    );
  }
  console.log(`\n${rows.length} scene(s)`);
  process.exit(0);
}

function sceneUrl(scene) {
  return `${BASE}/?view=scene&name=${encodeURIComponent(scene.name)}`;
}

for (const d of ["golden", "actual", "diff", "triptych"]) mkdirSync(join(OUT, d), { recursive: true });

// ── Capture ────────────────────────────────────────────────────────────────

async function capture(browser, scene) {
  const { width, height } = sceneViewport(scene);
  const context = await browser.newContext({
    viewport: { width, height },
    // 1, not 2. A device-pixel-ratio of 2 would capture a phone the way a phone
    // shows it, but it also doubles every antialiasing decision, and the
    // comparator is measuring layout, not rendering. Fixed either way: what
    // matters is that it never changes between runs.
    deviceScaleFactor: 1,
    // The prototype guards five of its animations behind this (zones-lab.css:509
    // and four others); scene.css covers the rest. Both, because the guards are
    // the prototype's own statement of what should not move.
    reducedMotion: "reduce",
    colorScheme: "dark",
    // The fonts are Google-hosted (index.html) and not installed locally, so a
    // capture depends on the network. Fixed locale and timezone so that when
    // something does format a date, it formats it the same way here and in CI.
    locale: "en-GB",
    timezoneId: "UTC",
  });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(String(e)));

  await page.goto(sceneUrl(scene), { waitUntil: "networkidle" });

  const missing = await page.$(".fx-missing");
  if (missing) {
    await context.close();
    throw new Error(`scene did not render: ${(await missing.textContent()).split("\n")[0]}`);
  }

  const target = sceneTarget(scene);
  const el = await page.waitForSelector(target, { timeout: 5000 });

  // The webfonts decide most of the pixels on this page. document.fonts.ready
  // resolves when the faces in use have loaded; without it the first capture of
  // a cold browser is in the fallback font and every later one is not.
  await page.evaluate(() => document.fonts.ready);
  // The transcript pins itself to its bottom in an effect with a ResizeObserver
  // (zones-lab.jsx:1074). One frame is enough for it to settle; this waits for
  // two, which is cheap and covers the observer's own callback.
  await page.evaluate(
    () => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))),
  );

  const buf = await el.screenshot({ type: "png", animations: "disabled" });
  await context.close();
  if (errors.length) throw new Error(`page errors: ${errors.join(" | ")}`);
  return buf;
}

// ── Compare ────────────────────────────────────────────────────────────────

// Largest connected run of changed pixels, so "0.04 % scattered over the text"
// and "0.04 % all in one 20x10 box" are not the same verdict. Flood fill over
// the diff mask, 4-connected, iterative (a recursive one blows the stack on a
// 1298x820 scene).
function largestCluster(mask, width, height) {
  const seen = new Uint8Array(width * height);
  const stack = [];
  let best = 0;
  for (let i = 0; i < mask.length; i++) {
    if (!mask[i] || seen[i]) continue;
    let size = 0;
    stack.push(i);
    seen[i] = 1;
    while (stack.length) {
      const p = stack.pop();
      size++;
      const x = p % width;
      const y = (p / width) | 0;
      if (x > 0 && mask[p - 1] && !seen[p - 1]) { seen[p - 1] = 1; stack.push(p - 1); }
      if (x < width - 1 && mask[p + 1] && !seen[p + 1]) { seen[p + 1] = 1; stack.push(p + 1); }
      if (y > 0 && mask[p - width] && !seen[p - width]) { seen[p - width] = 1; stack.push(p - width); }
      if (y < height - 1 && mask[p + width] && !seen[p + width]) { seen[p + width] = 1; stack.push(p + width); }
    }
    if (size > best) best = size;
  }
  return best;
}

function compare(goldenBuf, actualBuf) {
  const a = PNG.sync.read(goldenBuf);
  const b = PNG.sync.read(actualBuf);
  if (a.width !== b.width || a.height !== b.height) {
    return { sizeChanged: true, golden: a, actual: b, pct: 100, changed: -1, cluster: -1 };
  }
  const diff = new PNG({ width: a.width, height: a.height });
  const changed = pixelmatch(a.data, b.data, diff.data, a.width, a.height, {
    threshold: PIXEL_THRESHOLD,
    diffMask: false,
    alpha: 0.15,
    includeAA: false,
  });
  const total = a.width * a.height;
  // Rebuild the boolean mask from the diff image: pixelmatch paints changed
  // pixels red (and antialiased ones yellow, which includeAA:false leaves out
  // of the count but still paints). Only the red ones are real changes.
  const mask = new Uint8Array(total);
  for (let i = 0; i < total; i++) {
    const o = i * 4;
    if (diff.data[o] > 200 && diff.data[o + 1] < 100 && diff.data[o + 2] < 100) mask[i] = 1;
  }
  return {
    sizeChanged: false,
    golden: a,
    actual: b,
    diff,
    changed,
    pct: (changed / total) * 100,
    cluster: largestCluster(mask, a.width, a.height),
  };
}

// ── Triptych ───────────────────────────────────────────────────────────────

const GAP = 12;
const BG = [20, 20, 28, 255];

function blit(dst, src, dx, dy) {
  for (let y = 0; y < src.height; y++) {
    const ty = dy + y;
    if (ty < 0 || ty >= dst.height) continue;
    for (let x = 0; x < src.width; x++) {
      const tx = dx + x;
      if (tx < 0 || tx >= dst.width) continue;
      const s = (y * src.width + x) * 4;
      const t = (ty * dst.width + tx) * 4;
      dst.data[t] = src.data[s];
      dst.data[t + 1] = src.data[s + 1];
      dst.data[t + 2] = src.data[s + 2];
      dst.data[t + 3] = 255;
    }
  }
}

// Golden | actual | diff, side by side, one file. Three separate PNGs are three
// things to line up by hand; the whole point of the triptych is that the
// difference is visible without doing that.
function triptych(golden, actual, diff) {
  const panels = [golden, actual, diff].filter(Boolean);
  const width = panels.reduce((n, p) => n + p.width, 0) + GAP * (panels.length + 1);
  const height = Math.max(...panels.map((p) => p.height)) + GAP * 2;
  const out = new PNG({ width, height });
  for (let i = 0; i < width * height; i++) {
    out.data[i * 4] = BG[0];
    out.data[i * 4 + 1] = BG[1];
    out.data[i * 4 + 2] = BG[2];
    out.data[i * 4 + 3] = BG[3];
  }
  let x = GAP;
  for (const p of panels) {
    blit(out, p, x, GAP);
    x += p.width + GAP;
  }
  return PNG.sync.write(out);
}

// ── Run ────────────────────────────────────────────────────────────────────

async function serverUp() {
  try {
    const res = await fetch(`${BASE}/`, { signal: AbortSignal.timeout(2500) });
    return res.ok;
  } catch {
    return false;
  }
}

if (!(await serverUp())) {
  console.error(
    `fidelity: nothing answering at ${BASE}\n` +
      `  start the catalogue first:  cd pkg/serve/frontend && npm run catalog\n` +
      `  or point elsewhere:         node scripts/fidelity.mjs --base http://host:port`,
  );
  process.exit(2);
}

const scenes = pick();
if (!scenes.length) {
  console.error(`fidelity: no scene matches${PIECE ? ` --piece ${PIECE}` : ""}${ONLY ? ` --scene ${ONLY}` : ""}`);
  process.exit(2);
}

const browser = await chromium.launch({
  // Chromium comes from the Playwright cache that is already on this machine
  // (~/.cache/ms-playwright/chromium-1243). playwright-core does not download
  // browsers; if the revision is missing this fails loudly rather than pulling
  // 170 MB in the middle of a comparison.
  args: [
    // Deterministic text rendering. Chromium's LCD subpixel antialiasing takes
    // its decision from the display, which a headless run does not have; left
    // alone it is the classic source of a 0.3 % diff that means nothing.
    "--font-render-hinting=none",
    "--disable-lcd-text",
    "--force-color-profile=srgb",
    "--disable-skia-runtime-opts",
    "--hide-scrollbars",
  ],
});

const results = [];
let failed = 0;

for (const scene of scenes) {
  const goldenPath = join(OUT, "golden", `${scene.name}.png`);
  const actualPath = join(OUT, "actual", `${scene.name}.png`);
  const diffPath = join(OUT, "diff", `${scene.name}.png`);
  const tripPath = join(OUT, "triptych", `${scene.name}.png`);

  let shot;
  try {
    shot = await capture(browser, scene);
  } catch (e) {
    results.push({ scene, verdict: "ERROR", note: e.message });
    failed++;
    console.log(`  ERROR  ${scene.name}  ${e.message}`);
    continue;
  }

  if (UPDATE) {
    writeFileSync(goldenPath, shot);
    const png = PNG.sync.read(shot);
    results.push({ scene, verdict: "GOLDEN", pct: 0, size: `${png.width}x${png.height}` });
    console.log(`  golden ${scene.name}  ${png.width}x${png.height}`);
    continue;
  }

  writeFileSync(actualPath, shot);

  if (!existsSync(goldenPath)) {
    results.push({ scene, verdict: "NEW", note: "no golden — run with --update" });
    failed++;
    console.log(`  NEW    ${scene.name}  (no golden; --update to accept)`);
    continue;
  }

  const r = compare(readFileSync(goldenPath), shot);
  if (r.sizeChanged) {
    // A size change IS the finding: the scene's frame moved. Still worth a
    // triptych, so the two shapes can be seen next to each other.
    writeFileSync(tripPath, triptych(r.golden, r.actual, null));
    rmSync(diffPath, { force: true });
    results.push({
      scene, verdict: "FAIL", pct: 100,
      note: `size changed ${r.golden.width}x${r.golden.height} → ${r.actual.width}x${r.actual.height}`,
      size: `${r.actual.width}x${r.actual.height}`,
    });
    failed++;
    console.log(`  FAIL   ${scene.name}  size ${r.golden.width}x${r.golden.height} → ${r.actual.width}x${r.actual.height}`);
    continue;
  }

  writeFileSync(diffPath, PNG.sync.write(r.diff));
  writeFileSync(tripPath, triptych(r.golden, r.actual, r.diff));

  const overTolerance = r.pct > TOLERANCE_PCT;
  const clustered = r.cluster >= CLUSTER_PX;
  const ok = !overTolerance && !clustered;
  if (!ok) failed++;

  const why = overTolerance
    ? `${r.pct.toFixed(3)} % > ${TOLERANCE_PCT} %`
    : clustered
      ? `cluster of ${r.cluster} px (limit ${CLUSTER_PX})`
      : "";
  results.push({
    scene, verdict: ok ? "PASS" : "FAIL", pct: r.pct,
    changed: r.changed, cluster: r.cluster, note: why,
    size: `${r.golden.width}x${r.golden.height}`,
  });
  console.log(
    `  ${ok ? "pass  " : "FAIL  "} ${scene.name.padEnd(22)} ${r.pct.toFixed(3).padStart(8)} %  ` +
      `${String(r.changed).padStart(7)} px  cluster ${String(r.cluster).padStart(6)}${why ? "   " + why : ""}`,
  );
}

await browser.close();

// ── STATUS.md ──────────────────────────────────────────────────────────────
// Generated, never edited by hand (METODO §2). The moment someone writes a
// verdict into it, it is a claim again instead of a measurement.

function statusMd() {
  const when = new Date().toISOString().replace("T", " ").slice(0, 19) + " UTC";
  const byPiece = new Map();
  for (const r of results) {
    const p = r.scene.piece || "(none)";
    if (!byPiece.has(p)) byPiece.set(p, []);
    byPiece.get(p).push(r);
  }
  const lines = [];
  lines.push("# Fidelity status");
  lines.push("");
  lines.push("<!-- GENERATED by pkg/serve/frontend/scripts/fidelity.mjs. Do not edit: the");
  lines.push("     whole point is that this file is a measurement, not a claim. -->");
  lines.push("");
  lines.push(`Run: ${when}`);
  lines.push(`Mode: ${UPDATE ? "**--update** (goldens rewritten)" : "compare"}`);
  if (PIECE) lines.push(`Filter: \`--piece ${PIECE}\``);
  if (ONLY) lines.push(`Filter: \`--scene ${ONLY}\``);
  lines.push(`Tolerance: ≤ ${TOLERANCE_PCT} % changed pixels, and no cluster ≥ ${CLUSTER_PX} px.`);
  lines.push("");

  const pass = results.filter((r) => r.verdict === "PASS").length;
  const fail = results.filter((r) => r.verdict === "FAIL").length;
  const nw = results.filter((r) => r.verdict === "NEW").length;
  const err = results.filter((r) => r.verdict === "ERROR").length;
  const gold = results.filter((r) => r.verdict === "GOLDEN").length;
  lines.push(
    `**${results.length} scene(s)** — ` +
      [gold && `${gold} golden written`, pass && `${pass} pass`, fail && `${fail} FAIL`, nw && `${nw} new`, err && `${err} error`]
        .filter(Boolean)
        .join(", ") || "nothing measured",
  );
  lines.push("");

  for (const [piece, rows] of [...byPiece].sort()) {
    lines.push(`## ${piece}`);
    lines.push("");
    lines.push("| scene | size | diff | cluster | verdict | note |");
    lines.push("| --- | --- | ---: | ---: | --- | --- |");
    for (const r of rows) {
      lines.push(
        `| \`${r.scene.name}\` | ${r.size || "—"} | ${r.pct == null ? "—" : r.pct.toFixed(3) + " %"} | ` +
          `${r.cluster == null ? "—" : r.cluster + " px"} | ${r.verdict} | ${r.note || ""} |`,
      );
    }
    lines.push("");
  }

  lines.push("## Where the images are");
  lines.push("");
  lines.push("Relative to `tmp/redesign/fidelity/`:");
  lines.push("");
  lines.push("- `golden/<scene>.png` — the accepted catalogue, only rewritten with `--update`");
  lines.push("- `actual/<scene>.png` — this run");
  lines.push("- `diff/<scene>.png` — pixelmatch's mask (red = changed)");
  lines.push("- `triptych/<scene>.png` — **golden | actual | diff**, the thing to hand over");
  lines.push("");
  lines.push("## Private copies still in the prototype");
  lines.push("");
  // METODO §2: STATUS lists the functions zones-lab.jsx has not given up yet. A
  // piece is migrated when its function is gone and an import took its place,
  // so counting them is counting the work that is left. Counted, not judged:
  // the script has no idea which of them are meant to survive.
  try {
    const src = readFileSync(join(frontend, "src/catalog/zones-lab.jsx"), "utf8");
    const fns = [...src.matchAll(/^function ([A-Z]\w*)\(/gm)].map((m) => m[1]);
    const imports = [...src.matchAll(/^import .*? from "(\.\.\/[^"]+)"/gm)].map((m) => m[1]);
    lines.push(`\`zones-lab.jsx\`: **${fns.length}** component functions of its own, ` +
      `**${imports.length}** import(s) from production.`);
    lines.push("");
    lines.push("<details><summary>the functions</summary>");
    lines.push("");
    lines.push("```");
    lines.push(fns.join(" "));
    lines.push("```");
    lines.push("");
    lines.push("</details>");
  } catch (e) {
    lines.push(`(could not read zones-lab.jsx: ${e.message})`);
  }
  lines.push("");
  return lines.join("\n");
}

writeFileSync(join(OUT, "STATUS.md"), statusMd());

console.log(`\n${results.length} scene(s), ${failed} failing. STATUS.md written to ${join(OUT, "STATUS.md")}`);
if (!UPDATE && failed) {
  console.log(`triptychs: ${join(OUT, "triptych")}`);
}
process.exit(failed ? 1 : 0);
