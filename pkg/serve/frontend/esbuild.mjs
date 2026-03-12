import { build, context } from "esbuild";
import { copyFileSync, mkdirSync } from "fs";

const watch = process.argv.includes("--watch");
const outdir = "../static";

mkdirSync(outdir, { recursive: true });

const copyHtml = {
  name: "copy-html",
  setup(b) {
    b.onEnd(() => {
      copyFileSync("src/index.html", `${outdir}/index.html`);
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
  plugins: [copyHtml],
};

if (watch) {
  const ctx = await context(config);
  await ctx.watch();
  console.log("watching...");
} else {
  await build(config);
  console.log("built to", outdir);
}
