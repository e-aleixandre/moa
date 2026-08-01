import { build, context } from "esbuild";
import { copyFileSync, mkdirSync, readFileSync, writeFileSync } from "fs";
import { createHash } from "crypto";

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
      stampBuildID();
    });
  },
};

// The bundle ships under fixed names, so nothing in a served response tells a
// client whether its code is still the code the server has. stampBuildID
// derives one id from the built output and writes it twice: into the bundle
// itself (so a running client knows which build it is) and into build-id.txt
// (which the server reports on /api/version). Same build, same id — a mismatch
// means the page is stale. Digesting the output makes it reproducible and
// independent of release tooling: a self-built binary carries no version.
function stampBuildID() {
  const js = readFileSync(`${outdir}/app.js`);
  const css = readFileSync(`${outdir}/app.css`);
  const id = createHash("sha256").update(js).update(css).digest("hex").slice(0, 12);
  writeFileSync(`${outdir}/build-id.txt`, `${id}\n`);
  // Appended rather than injected via `define`, which would feed back into the
  // digest it is derived from.
  writeFileSync(`${outdir}/app.js`, `${js}\nglobalThis.__MOA_BUILD_ID__=${JSON.stringify(id)};\n`);
}

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
