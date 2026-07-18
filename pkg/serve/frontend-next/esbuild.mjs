import { build, context } from "esbuild";
import { copyFileSync, mkdirSync } from "fs";

const watch = process.argv.includes("--watch");
const outdir = "../static-next";

mkdirSync(outdir, { recursive: true });

// Static assets copied verbatim into the build output. Fase 0: solo el
// index.html — sin manifest/sw/iconos PWA todavía.
const staticAssets = ["index.html"];

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
