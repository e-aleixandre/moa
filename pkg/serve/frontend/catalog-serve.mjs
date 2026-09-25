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

// ?view=composer-wave has to show the shipped Composer recording and on a
// call, states only a microphone and a WebRTC call can reach. Only for the
// Composer's own imports, its two voice hooks resolve to lab doubles; the
// doubles call the real hook and override it only under the lab's provider,
// so every other view still gets the real behaviour.
function labVoiceDoubles() {
  return {
    name: "lab-voice-doubles",
    setup(build) {
      build.onResolve({ filter: /hooks\/useVoice(Gesture|Live)\.js$/ }, (args) => {
        if (!/layout[\\/]Composer[\\/]Composer\.jsx$/.test(args.importer)) return undefined;
        const which = args.path.includes("Gesture") ? "gesture" : "live";
        return { path: resolve(here, `src/catalog/composer-wave/voice-${which}-double.js`) };
      });
    },
  };
}

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
  plugins: [labVoiceDoubles()],
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
