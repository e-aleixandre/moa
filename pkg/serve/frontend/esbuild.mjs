import { build, context } from "esbuild";
import { copyFileSync, mkdirSync } from "fs";

const watch = process.argv.includes("--watch");
const outdir = "../static";

mkdirSync(outdir, { recursive: true });

// Static assets copied verbatim into the build output: the shell, the PWA
// manifest, the service worker (push + installability) and the icons the
// manifest points at. They are served from the root, which is also the
// manifest's scope, so the paths inside them are absolute.
const staticAssets = [
  "index.html",
  "manifest.webmanifest",
  "sw.js",
  "icon-192.png",
  "icon-512.png",
  "icon-maskable-512.png",
  "apple-touch-icon.png",
];

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
