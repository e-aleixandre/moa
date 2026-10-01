// Real-Chromium regression test for SEC-02: Markdown output is inserted into
// the main document, so it must not carry CSS or trigger remote fetches.
//
//   cd pkg/serve/frontend && node --test scripts/markdown-security.test.mjs
//
// playwright-core is deliberately NOT in package.json (see fidelity.mjs). Point
// MOA_PLAYWRIGHT_CORE at an installed copy, or install it into the gitignored
// tmp/redesign/fidelity/vendor directory. Chromium comes from
// MOA_CHROMIUM_PATH (CI uses the runner's Chrome) or the Playwright cache.
import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import http from "node:http";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { createRequire } from "node:module";
import { build } from "esbuild";

const frontend = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const VENDOR = resolve(frontend, "../../../tmp/redesign/fidelity/vendor");

async function loadChromium() {
  const candidates = [];
  if (process.env.MOA_PLAYWRIGHT_CORE) candidates.push(process.env.MOA_PLAYWRIGHT_CORE);
  try {
    candidates.push(createRequire(join(VENDOR, "noop.cjs")).resolve("playwright-core"));
  } catch {}
  candidates.push("playwright-core");
  for (const c of candidates) {
    try {
      const target = c.startsWith("/") ? pathToFileURL(require_main(c)).href : c;
      const mod = await import(target);
      const chromium = mod.chromium ?? mod.default?.chromium;
      if (chromium) return chromium;
    } catch {}
  }
  throw new Error(
    "markdown-security: playwright-core not found. It is not in package.json on purpose;\n" +
      `  install it with: cd ${VENDOR} && npm i playwright-core\n` +
      "  or set MOA_PLAYWRIGHT_CORE to an existing playwright-core directory.",
  );
}
// A directory resolves through its package.json main; a file resolves as is.
function require_main(p) {
  return createRequire(join(p, "noop.cjs")).resolve(p);
}

const PNG = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==",
  "base64",
);
const DATA_PNG = "data:image/png;base64," + PNG.toString("base64");

// `bun test` globs *.test.mjs too (and its node:test shim ignores `skip`); this
// needs a vendored Playwright, so it registers nothing outside `node --test`.
const skip = typeof Bun !== "undefined";

const run = skip ? () => {} : test;

let serverA, serverB, portA, portB, browser, page, bundle;
const bRequests = [];

const listen = (srv) =>
  new Promise((r) => srv.listen(0, "127.0.0.1", () => r(srv.address().port)));

if (!skip) before(async () => {
  const out = await build({
    stdin: {
      contents:
        "import './src/util/sanitize.js';" +
        "import { renderMarkdown, renderMarkdownWithCaret } from './src/data/util/markdown.js';" +
        "window.md = { renderMarkdown, renderMarkdownWithCaret };",
      resolveDir: frontend,
    },
    bundle: true, write: false, format: "iife", platform: "browser", logLevel: "silent",
  });
  bundle = out.outputFiles[0].text;

  serverA = http.createServer((req, res) => {
    if (req.url === "/bundle.js") {
      res.setHeader("content-type", "text/javascript");
      return res.end(bundle);
    }
    if (req.url.startsWith("/same.png")) {
      res.setHeader("content-type", "image/png");
      return res.end(PNG);
    }
    res.setHeader("content-type", "text/html");
    res.end("<!doctype html><body><div id=root></div><script src=/bundle.js></script>");
  });
  serverB = http.createServer((req, res) => {
    bRequests.push(req.url);
    res.setHeader("content-type", "image/png");
    res.end(PNG);
  });
  portA = await listen(serverA);
  portB = await listen(serverB);

  const chromium = await loadChromium();
  // CI points this at the runner's preinstalled Chrome instead of downloading one.
  browser = await chromium.launch({ executablePath: process.env.MOA_CHROMIUM_PATH || undefined });
  const context = await browser.newContext();
  page = await context.newPage();
  // A cached response never reaches server B, so a bypass seen once would hide
  // behind the cache on the next entry point.
  const cdp = await context.newCDPSession(page);
  await cdp.send("Network.enable");
  await cdp.send("Network.setCacheDisabled", { cacheDisabled: true });
  await page.goto(`http://127.0.0.1:${portA}/`);
});

if (!skip) after(async () => {
  await browser?.close();
  serverA?.close();
  serverB?.close();
});

// sanitize.js is imported because it registers the global anchor target/rel hook
// the app relies on for markdown links.
// Render in the page and insert into the live document, as the app does.
async function render(fn, md) {
  return page.evaluate(
    ([fn, md]) => {
      const root = document.getElementById("root");
      root.innerHTML = window.md[fn](md);
      return root.innerHTML;
    },
    [fn, md],
  );
}
const settle = () => page.waitForTimeout(500);

// Leading text: DOMPurify drops a style element that is the very first node.
const POC =
  "intro\n\n<style>body{--audit-css:injected}</style>\n" +
  '<span style="position:fixed;inset:0;z-index:2147483647">overlay</span>\n' +
  "![remote](https://attacker.invalid/pixel)\n";

run("markdown cannot inject stylesheet or inline CSS", async () => {
  for (const fn of ["renderMarkdown", "renderMarkdownWithCaret", "renderMarkdown"]) {
    await render(fn, POC);
    const r = await page.evaluate(() => {
      const root = document.getElementById("root");
      return {
        styles: root.querySelectorAll("style").length,
        styled: root.querySelectorAll("[style]").length,
        prop: getComputedStyle(document.body).getPropertyValue("--audit-css").trim(),
        fixed: [...root.querySelectorAll("*")].filter((e) => getComputedStyle(e).position === "fixed").length,
      };
    });
    assert.deepEqual(r, { styles: 0, styled: 0, prop: "", fixed: 0 }, fn);
  }
});

run("markdown cannot automatically fetch remote images", async () => {
  bRequests.length = 0;
  const remote = `http://127.0.0.1:${portB}`; // same host, different port
  const local = `http://localhost:${portB}`; // different host
  const md = [
    `![md-img](${remote}/a.png)`,
    `<img src="${local}/b.png" alt="raw-img">`,
    `<img src="//127.0.0.1:${portB}/c.png" alt="proto-rel">`,
    `<img src="/same.png?x=1" srcset="${remote}/d.png 1x" alt="srcset-img">`,
    `![scheme](https://127.0.0.1:${portA}/e.png)`,
  ].join("\n\n");
  for (const fn of ["renderMarkdown", "renderMarkdownWithCaret"]) {
    const html = await render(fn, md);
    await settle();
    assert.equal(bRequests.length, 0, `${fn}: server B received requests`);
    const r = await page.evaluate(() => ({
      srcs: [...document.querySelectorAll("#root img")].map((i) => i.getAttribute("src")),
      srcset: document.querySelectorAll("#root [srcset]").length,
      text: document.getElementById("root").textContent,
    }));
    for (const s of r.srcs) assert.ok(!/127\.0\.0\.1:\d+|localhost/.test(s) || s.startsWith("/"), `${fn}: remote img ${s}`);
    assert.equal(r.srcset, 0, `${fn}: srcset survived`);
    for (const alt of ["md-img", "raw-img", "proto-rel", "scheme"]) assert.ok(r.text.includes(alt), `${fn}: alt ${alt}`);
    assert.ok(!html.includes(`:${portB}`), `${fn}: remote URL survived in output`);
  }
});

run("same-origin and raster data images still render", async () => {
  await render("renderMarkdown", `![s](/same.png)\n\n![d](${DATA_PNG})\n\n![rel](same.png)`);
  await page.waitForFunction(() =>
    [...document.querySelectorAll("#root img")].every((i) => i.complete));
  const r = await page.evaluate(() =>
    [...document.querySelectorAll("#root img")].map((i) => i.naturalWidth));
  assert.equal(r.length, 3);
  assert.ok(r.every((w) => w > 0), JSON.stringify(r));
});

run("legitimate markdown structure survives", async () => {
  const html = await render(
    "renderMarkdown",
    [
      "| a | b |", "|---|---|", "| 1 | 2 |", "",
      "```js", "const x = 1;", "```", "",
      "- [x] done", "- [ ] todo", "",
      "[link](https://example.com)", "",
      "`0123456789abcdef01234567`", "",
      '<script>window.pwn=1</script><img src=x onerror="window.pwn=1"> [bad](javascript:alert(1))',
    ].join("\n"),
  );
  const r = await page.evaluate(() => {
    const q = (s) => document.querySelectorAll("#root " + s).length;
    const a = document.querySelector("#root a[href^='https://example.com']");
    return {
      wrap: q(".md-table-wrap table"),
      copy: q(".code-block button.code-block-copy"),
      hljs: q("code.hljs .hljs-keyword, code.hljs span[class^='hljs-']"),
      lang: document.querySelector("#root .code-block")?.getAttribute("data-lang"),
      checks: [...document.querySelectorAll("#root input[type=checkbox]")].map((c) => [c.disabled, c.checked]),
      target: a?.getAttribute("target"), rel: a?.getAttribute("rel"),
      sessionRef: q("code.session-ref"),
      script: q("script"), onerror: q("[onerror]"),
      jsHref: q("a[href^='javascript:']"), pwn: window.pwn ?? null,
    };
  });
  assert.equal(r.wrap, 1);
  assert.equal(r.copy, 1);
  assert.ok(r.hljs > 0, "highlight classes");
  assert.equal(r.lang, "js");
  assert.deepEqual(r.checks, [[true, true], [true, false]]);
  assert.equal(r.target, "_blank");
  assert.equal(r.rel, "noopener noreferrer");
  assert.equal(r.sessionRef, 1);
  assert.deepEqual([r.script, r.onerror, r.jsHref, r.pwn], [0, 0, 0, null]);
  assert.ok(html.length > 0);

  const caret = await render("renderMarkdownWithCaret", "streaming **text**");
  assert.ok(caret.includes('class="zl-caret"'), caret);
});

const SVG_DATA =
  "data:image/svg+xml;base64," +
  Buffer.from('<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24"><rect width="24" height="24" fill="red"/></svg>').toString("base64");

// Every payload that could make the browser fetch from another origin, or
// carry CSS/JS, as soon as the sanitized HTML is inserted.
function hostilePayloads(tag) {
  const b = `127.0.0.1:${portB}`;
  const remote = `http://${b}`;
  const a = `127.0.0.1:${portA}`;
  return [
    ["markdown-img", `![remote](${remote}/md.png?${tag})`],
    ["raw-img", `<img src="${remote}/raw.png?${tag}" alt="remote">`],
    ["other-host", `<img src="http://localhost:${portB}/host.png?${tag}" alt="remote">`],
    ["protocol-relative", `<img src="//${b}/proto.png?${tag}" alt="remote">`],
    ["uppercase", `<IMG SRC="HTTP://${b}/upper.png?${tag}" ALT="remote">`],
    ["entities", `<img src="http&#58;//127.0.0.1&#58;${portB}/entity.png?${tag}" alt="remote">`],
    ["newline-url", `<img src="h&#10;ttp://${b}/newline.png?${tag}" alt="remote">`],
    ["backslashes", `<img src="http:\\\\${b}/back.png?${tag}" alt="remote">`],
    ["userinfo", `<img src="http://${a}@${b}/user.png?${tag}" alt="remote">`],
    ["srcset", `<img src="/same.png" srcset="${remote}/srcset.png?${tag} 1x, ${remote}/srcset2.png?${tag} 2x">`],
    ["picture-source", `<picture><source srcset="${remote}/source.png?${tag}"><img src="/same.png"></picture>`],
    ["picture-only-source", `<picture><source srcset="${remote}/source-only.png?${tag}"></picture>`],
    ["svg-image", `<svg><image href="${remote}/svg.png?${tag}"/></svg>`],
    ["svg-xlink", `<svg><image xlink:href="${remote}/svg-xlink.png?${tag}"/></svg>`],
    ["svg-foreignobject", `<svg><foreignObject><img src="${remote}/foreign.png?${tag}"></foreignObject></svg>`],
    ["style-tag", `intro\n\n<style>body{background-image:url(${remote}/css-tag.png?${tag});--review-injected:yes}</style>`],
    ["style-attribute", `<span style="background-image:url(${remote}/css-attr.png?${tag})">hello</span>`],
    ["style-entity", `<div STYLE="background:url(&quot;${remote}/css-entity.png?${tag}&quot;)">hello</div>`],
    ["link-stylesheet", `<link rel="stylesheet" href="${remote}/sheet.css?${tag}">`],
    ["link-preload", `<link rel="preload" as="image" href="${remote}/preload.png?${tag}">`],
    ["video-poster", `<video poster="${remote}/poster.png?${tag}"></video>`],
    ["video-src", `<video src="${remote}/video.mp4?${tag}" autoplay></video>`],
    ["audio-src", `<audio src="${remote}/audio.mp3?${tag}" autoplay></audio>`],
    ["table-background", `<table background="${remote}/background.png?${tag}"><tr><td>x</td></tr></table>`],
    ["body-background", `<body background="${remote}/body.png?${tag}">x</body>`],
    ["object", `<object data="${remote}/object.html?${tag}"></object>`],
    ["iframe", `<iframe src="${remote}/frame.html?${tag}"></iframe>`],
    ["embed", `<embed src="${remote}/embed.html?${tag}">`],
    ["input-image", `<input type="image" src="${remote}/input-image.png?${tag}" alt="input">`],
    ["input-image-uppercase", `<input TYPE="IMAGE" SRC="${remote}/input-upper.png?${tag}" alt="input">`],
    ["input-image-entities", `<input type="im&#97;ge" src="http&#58;//127.0.0.1:${portB}/input-entity.png?${tag}" alt="input">`],
    ["input-checkbox-src", `<input type="checkbox" src="${remote}/input-checkbox.png?${tag}" disabled checked>`],
    ["input-button-formaction", `<input type="submit" formaction="${remote}/submit?${tag}" value="go">`],
    ["button-formaction", `<button formaction="${remote}/button?${tag}">go</button>`],
    ["data-text", `<img src="data:text/html,hello" alt="non-image">`],
    ["data-svg-nonimage", `<img src="data:application/svg+xml;base64,${SVG_DATA.split(",")[1]}" alt="non-image">`],
    ["anchor-link", `[normal external link](${remote}/navigate?${tag})`],
    ["adjacent-script", `<img src="${remote}/first.png?${tag}" alt="first"><script>window.reviewPwn=1</script><img src="${remote}/last.png?${tag}" onerror="window.reviewPwn=1">`],
    ["meta-refresh", `<meta http-equiv="refresh" content="0;url=${remote}/meta?${tag}">`],
    ["base-href", `<base href="${remote}/"><img src="relative.png?${tag}" alt="based">`],
  ];
}

run("no hostile payload fetches remotely or carries CSS/JS, in either entry point", async () => {
  const failures = [];
  for (const fn of ["renderMarkdown", "renderMarkdownWithCaret"]) {
    for (const [name, source] of hostilePayloads(fn)) {
      bRequests.length = 0;
      await render(fn, source);
      await page.waitForTimeout(150);
      const r = await page.evaluate(() => {
        const root = document.getElementById("root");
        return {
          css: root.querySelectorAll("style,[style],link,base,meta").length,
          injected: getComputedStyle(document.body).getPropertyValue("--review-injected").trim(),
          script: window.reviewPwn ?? null,
          inputs: [...root.querySelectorAll("input")].filter((i) => i.type !== "checkbox" || i.hasAttribute("src") || !i.disabled).length,
          formaction: root.querySelectorAll("[formaction]").length,
        };
      });
      if (bRequests.length || r.css || r.injected || r.script || r.inputs || r.formaction) {
        failures.push({ fn, name, requests: bRequests.length, ...r });
      }
    }
  }
  assert.deepEqual(failures, []);
});

run("image allowlist keeps same-origin and data images, including SVG", async () => {
  const origin = `http://127.0.0.1:${portA}`;
  const keep = [
    `<img src="${SVG_DATA}" alt="svg">`,
    `<img src="${DATA_PNG}" alt="png">`,
    `<img src="${origin}/same.png?abs" alt="abs">`,
    `<img src="same.png?rel" alt="rel">`,
    `<img src="/same.png?root" alt="root">`,
  ].join("\n");
  for (const fn of ["renderMarkdown", "renderMarkdownWithCaret"]) {
    await render(fn, keep);
    await page.waitForFunction(() => [...document.querySelectorAll("#root img")].every((i) => i.complete));
    const widths = await page.evaluate(() => [...document.querySelectorAll("#root img")].map((i) => i.naturalWidth));
    assert.equal(widths.length, 5, fn);
    assert.ok(widths.every((w) => w > 0), `${fn}: ${JSON.stringify(widths)}`);
  }
});
