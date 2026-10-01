// Real-Chromium regression test for SEC-02: Markdown output is inserted into
// the main document, so it must not carry CSS or trigger remote fetches.
//
//   cd pkg/serve/frontend && node --test scripts/markdown-security.test.mjs
//
// playwright-core is deliberately NOT in package.json (see fidelity.mjs). Point
// MOA_PLAYWRIGHT_CORE at an installed copy, or install it into the gitignored
// tmp/redesign/fidelity/vendor directory. Chromium comes from
// PLAYWRIGHT_BROWSERS_PATH / the default Playwright cache.
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
  browser = await chromium.launch();
  page = await browser.newPage();
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
