// no-history-navigation.test.js — run with `bun test`.
//
// The INVERSION guard for a decision that is easy to undo by accident: nothing
// in the app may create or consume browser history entries. moa runs as an
// installed PWA that behaves like a native app, so a back/forward gesture has
// no destination inside it — overlays close with their own controls and the
// conversation ⇄ grid hop has its own affordance. An earlier overlay/history
// binding pushed a guard entry and popped it with history.back(), which left an
// entry AHEAD of the cursor and gave the PWA a forward gesture onto an inert
// state. A reviewer cannot reliably catch a re-introduction by reading a diff,
// so this walks the source instead.
//
// replaceState is allowed (and listed per file): it keeps the URL truthful for a
// RELOAD or a SHARED link without adding an entry.

import { test, expect } from 'bun:test';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';

const SRC = new URL('.', import.meta.url).pathname.replace(/\/data\/$/, '');

function sources(dir, out = []) {
  for (const name of readdirSync(dir)) {
    if (name === 'node_modules') continue;
    const full = join(dir, name);
    if (statSync(full).isDirectory()) { sources(full, out); continue; }
    if (!/\.(js|jsx)$/.test(name)) continue;
    if (/\.test\.(js|jsx)$/.test(name)) continue;
    out.push(full);
  }
  return out;
}

// Strip comments so the prose explaining WHY the machinery is gone does not
// trip the guard that enforces it.
function code(text) {
  return text.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:])\/\/.*$/gm, '$1');
}

// Files allowed to call replaceState, each with the reason.
const REPLACE_ALLOWED = new Map([
  ['data/router.js', 'keeps ?view= truthful for a reload or a shared link, without an entry'],
  ['app.jsx', 'strips a consumed ?session= / ?share= from the URL at bootstrap'],
  ['data/stale-build.js', 'drops the cache-busting query after a stale-bundle reload'],
]);

const FILES = sources(SRC);

test('the source walk found the app (guard is not vacuous)', () => {
  expect(FILES.length).toBeGreaterThan(100);
});

test('nothing pushes a history entry', () => {
  const offenders = FILES.filter((f) => /\bpushState\s*\(/.test(code(readFileSync(f, 'utf8'))));
  expect(offenders.map((f) => relative(SRC, f))).toEqual([]);
});

test('nothing navigates history (back/forward/go)', () => {
  const offenders = FILES.filter((f) => /\bhistory\s*\.\s*(back|forward|go)\s*\(/.test(code(readFileSync(f, 'utf8'))));
  expect(offenders.map((f) => relative(SRC, f))).toEqual([]);
});

test('nothing listens for popstate', () => {
  const offenders = FILES.filter((f) => /['"]popstate['"]/.test(code(readFileSync(f, 'utf8'))));
  expect(offenders.map((f) => relative(SRC, f))).toEqual([]);
});

test('replaceState is only used where it is justified', () => {
  const users = FILES
    .filter((f) => /\breplaceState\s*\(/.test(code(readFileSync(f, 'utf8'))))
    .map((f) => relative(SRC, f))
    .sort();
  expect(users).toEqual([...REPLACE_ALLOWED.keys()].sort());
});
