import { test, expect } from "bun:test";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";

// Legacy tokens still have hundreds of callers. Freeze today's debt so a
// component migration can lower it, but new work cannot expand it.
const BUDGET = {
  colour: 906,
  radius: 114,
  type: 260,
};

function sourceFiles(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const full = join(dir, name);
    if (statSync(full).isDirectory()) sourceFiles(full, out);
    else if (/\.(?:css|js|jsx)$/.test(name)) out.push(full);
  }
  return out;
}

function count(files, pattern) {
  return files.reduce((total, file) => {
    const source = readFileSync(file, "utf8");
    return total + (source.match(pattern) || []).length;
  }, 0);
}

test("legacy visual token debt cannot grow", () => {
  const root = new URL("..", import.meta.url).pathname;
  const files = sourceFiles(root).filter((file) => !file.endsWith("legacy-tokens.test.js"));
  const actual = {
    colour: count(files, /var\(--(?:base|crust|mantle|surface[0-2]|text|subtext[01]|overlay[01])\b/g),
    radius: count(files, /var\(--radius-(?:sm|md|lg)\b/g),
    type: count(files, /var\(--text-(?:nano|micro|xs|sm|base|md|lg|xl|2xl)\b/g),
  };

  expect(actual.colour).toBeLessThanOrEqual(BUDGET.colour);
  expect(actual.radius).toBeLessThanOrEqual(BUDGET.radius);
  expect(actual.type).toBeLessThanOrEqual(BUDGET.type);
});
