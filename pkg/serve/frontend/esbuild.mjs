import { build, context } from "esbuild";
import {
  existsSync, linkSync, mkdirSync, readFileSync, readdirSync, renameSync,
  rmSync, unlinkSync, writeFileSync,
} from "fs";
import { createHash } from "crypto";
import { basename, dirname, resolve } from "path";

const watch = process.argv.includes("--watch");
const prune = process.argv.includes("--prune");
const outdir = "../static";
const buildsDir = `${outdir}/build`;
const publishLock = `${outdir}.publish.lock`;
const appEntry = resolve("src/app.jsx");

mkdirSync(outdir, { recursive: true });

// Root-scoped PWA assets. The shell and its JS/CSS live together under the
// content-addressed build directory selected by build-id.txt.
const staticAssets = [
  "index.html",
  "manifest.webmanifest",
  "sw.js",
  "icon-192.png",
  "icon-512.png",
  "icon-maskable-512.png",
  "apple-touch-icon.png",
];

const publishFrontend = {
  name: "publish-frontend",
  setup(b) {
    // Static assets are not imported by the JS graph. Returning the app entry
    // through the plugin lets esbuild watch them without adding a fake runtime
    // import or a second output bundle.
    b.onLoad({ filter: /app\.jsx$/ }, (args) => {
      if (args.path !== appEntry) return undefined;
      return {
        contents: readFileSync(args.path),
        loader: "jsx",
        resolveDir: dirname(args.path),
        watchFiles: [args.path, ...staticAssets.map((f) => resolve("src", f))],
      };
    });
    b.onEnd(async (result) => {
      // onEnd also runs after failed watch rebuilds. Esbuild keeps outputs in
      // memory, and publishBuild reads and validates every static source before
      // replacing anything in the served tree, so the last good build survives.
      if (result.errors.length > 0) return;
      await withPublishLock(() => publishBuild(result.outputFiles));
    });
  },
};

function processIsAlive(pid) {
  if (!Number.isInteger(pid) || pid <= 0) return false;
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    return error && error.code !== "ESRCH";
  }
}

async function withPublishLock(fn) {
  const deadline = Date.now() + 30_000;
  const candidate = `${publishLock}.${process.pid}`;
  writeFileSync(candidate, `${process.pid}\n`);
  let acquired = false;
  try {
    for (;;) {
      try {
        // Hard-linking a fully written owner record makes the lock visible
        // atomically; a crash can never strand an empty canonical lock.
        linkSync(candidate, publishLock);
        acquired = true;
        break;
      } catch (error) {
        if (!error || error.code !== "EEXIST") throw error;
        let owner = 0;
        try {
          owner = Number.parseInt(readFileSync(publishLock, "utf8"), 10);
        } catch (_) { /* the owner may have just released the lock */ }
        if (!owner || !processIsAlive(owner)) {
          try { unlinkSync(publishLock); } catch (_) { /* another waiter won */ }
          continue;
        }
        if (Date.now() >= deadline) throw new Error("timed out waiting for frontend publish lock");
        await new Promise((resolveWait) => setTimeout(resolveWait, 50));
      }
    }
    rmSync(candidate, { force: true });
    return fn();
  } finally {
    rmSync(candidate, { force: true });
    if (acquired) rmSync(publishLock, { force: true });
  }
}

function writeAtomic(file, data) {
  const tmp = `${file}.tmp-${process.pid}`;
  writeFileSync(tmp, data);
  renameSync(tmp, file);
}

function collectBuild(outputFiles) {
  const files = new Map();
  for (const output of outputFiles || []) {
    files.set(basename(output.path), Buffer.from(output.contents));
  }
  for (const file of staticAssets) {
    files.set(file, readFileSync(`src/${file}`));
  }
  for (const required of ["app.js", "app.css", ...staticAssets]) {
    if (!files.has(required)) throw new Error(`frontend build did not produce ${required}`);
  }
  return files;
}

// The id covers the runtime frontend tree rather than only JS/CSS: shell,
// service-worker, manifest, and icon-only changes must also make a running PWA
// adopt the new build. File names and separators make the digest unambiguous.
function calculateBuildID(files) {
  const hash = createHash("sha256");
  for (const file of [...files.keys()].sort()) {
    hash.update(file).update("\0").update(files.get(file)).update("\0");
  }
  return hash.digest("hex").slice(0, 12);
}

function versionShell(html, id) {
  const css = 'href="app.css"';
  const js = 'src="app.js"';
  if (!html.includes(css) || !html.includes(js)) {
    throw new Error("index.html must reference app.css and app.js without a build path");
  }
  return html
    .replace(css, `href="/build/${id}/app.css"`)
    .replace(js, `src="/build/${id}/app.js"`);
}

function writeStagedBuild(files, id) {
  mkdirSync(buildsDir, { recursive: true });
  const staged = `${buildsDir}/.${id}.tmp-${process.pid}`;
  const target = `${buildsDir}/${id}`;
  rmSync(staged, { recursive: true, force: true });
  mkdirSync(staged);

  const html = files.get("index.html").toString("utf8");
  for (const [file, contents] of files) {
    if (staticAssets.includes(file)) continue;
    const output = file === "app.js"
      ? `${contents.toString("utf8")}\nglobalThis.__MOA_BUILD_ID__=${JSON.stringify(id)};\n`
      : contents;
    writeFileSync(`${staged}/${file}`, output);
  }
  writeFileSync(`${staged}/shell.html`, versionShell(html, id));

  // A repeated deterministic build already has these exact runtime files.
  if (existsSync(target)) rmSync(staged, { recursive: true, force: true });
  else renameSync(staged, target);
}

// publishBuild stages immutable JS/CSS/shell files under their build id, copies
// root-scoped PWA assets, then atomically switches build-id.txt. The server uses
// that one file for both / and /api/version, so concurrent readers see either
// the complete previous build or the complete new one.
function publishBuild(outputFiles) {
  const files = collectBuild(outputFiles);
  const id = calculateBuildID(files);
  writeStagedBuild(files, id);

  for (const file of staticAssets) {
    if (file !== "index.html") writeAtomic(`${outdir}/${file}`, files.get(file));
  }

  // This is the only publication pointer and is written after every dependency.
  writeAtomic(`${outdir}/build-id.txt`, `${id}\n`);

  // Immutable trees are retained by default: a live disk server may have
  // already routed an in-flight request through any prior pointer. Release/CI
  // builds opt into pruning only when no server is reading this directory.
  if (prune) {
    for (const entry of readdirSync(buildsDir, { withFileTypes: true })) {
      if (entry.isDirectory() && entry.name !== id) {
        rmSync(`${buildsDir}/${entry.name}`, { recursive: true, force: true });
      }
    }
  }
  for (const legacy of ["app.js", "app.css", "index.html", "app.js.map", "app.css.map"]) {
    rmSync(`${outdir}/${legacy}`, { force: true });
  }
}

const config = {
  entryPoints: ["src/app.jsx", "src/catalog-entry.js"],
  bundle: true,
  splitting: true,
  external: ["./catalog-entry.js"],
  outdir,
  write: false,
  format: "esm",
  minify: !watch,
  sourcemap: watch,
  jsx: "automatic",
  jsxImportSource: "preact",
  plugins: [publishFrontend],
};

if (watch) {
  const ctx = await context(config);
  await ctx.watch();
  console.log("watching...");
} else {
  await build(config);
  console.log("built to", outdir);
}
