import { build, context } from "esbuild";
import { copyFileSync, mkdirSync } from "fs";

const watch = process.argv.includes("--watch");
const outdir = "../static-next";

mkdirSync(outdir, { recursive: true });

// Static assets copied verbatim into the build output. index.html plus the PWA
// manifest (5N). `manifest.webmanifest` is scoped to /next/ (an already-installed
// PWA launches there); `manifest-root.webmanifest` is the same but scoped to /
// and is served by the backend at /manifest.webmanifest for a fresh install from
// the root (post-cutover). Icons are NOT copied here — the manifest points at the
// ROOT icons in pkg/serve/static/ (/icon-192.png, etc.), and the app registers
// the root /sw.js (push runs through it).
const staticAssets = ["index.html", "manifest.webmanifest", "manifest-root.webmanifest"];

const copyStatic = {
  name: "copy-static",
  setup(b) {
    b.onEnd(() => {
      for (const f of staticAssets) {
        copyFileSync(`src/${f}`, `${outdir}/${f}`);
      }
    });
  },
};

const config = {
  entryPoints: ["src/app.jsx"],
  bundle: true,
  outdir,
  format: "esm",
  minify: !watch,
  sourcemap: watch,
  jsx: "automatic",
  jsxImportSource: "preact",
  plugins: [copyStatic],
};

if (watch) {
  const ctx = await context(config);
  await ctx.watch();
  console.log("watching...");
} else {
  await build(config);
  console.log("built to", outdir);
}
