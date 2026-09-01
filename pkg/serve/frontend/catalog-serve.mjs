#!/usr/bin/env node
// catalog-serve — design lab. Watches the frontend and serves it on its own
// port. Does not compile or restart moa. The production binary does not
// embed this.

import { context } from "esbuild";
import { mkdirSync, writeFileSync, readFileSync } from "fs";
import { dirname, resolve } from "path";
import { fileURLToPath } from "url";
import { execSync } from "child_process";

const here = dirname(fileURLToPath(import.meta.url));
const outdir = resolve(here, ".catalog-dist");
const port = Number(process.env.PORT || 7300);
const host = process.env.HOST || "0.0.0.0";

mkdirSync(outdir, { recursive: true });

const indexSrc = readFileSync(resolve(here, "src/index.html"), "utf8")
  .replace('href="app.css"', 'href="/catalog-app.css"')
  .replace('src="app.js"', 'src="/catalog-app.js"')
  .replace("<title>moa</title>", "<title>moa · catalog</title>");
writeFileSync(resolve(outdir, "index.html"), indexSrc);

const ctx = await context({
  absWorkingDir: here,
  entryPoints: ["src/catalog-app.jsx"],
  bundle: true,
  outdir,
  format: "esm",
  jsx: "automatic",
  jsxImportSource: "preact",
  sourcemap: true,
  minify: false,
});
await ctx.watch();
await ctx.serve({ servedir: outdir, host, port });

function tailscale4() {
  try {
    const ip = execSync("tailscale ip -4", { encoding: "utf8", stdio: ["ignore", "pipe", "ignore"] }).trim();
    return ip || null;
  } catch {
    return null;
  }
}

const ts = tailscale4();
console.log(`catalog on http://127.0.0.1:${port}/?view=desktop`);
if (ts) console.log(`         http://${ts}:${port}/?view=desktop`);
console.log("watching. Ctrl-C to stop.");
