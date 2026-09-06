// artifacts-drawer.test.js — the drawer's modal behaviour and header identity,
// exercised through the same pure helpers the component uses. Real QA on
// 127.0.0.1:7310 found three defects; these pin the fixes.
// Run with `bun test`.

import { test, expect, beforeEach, afterEach } from 'bun:test';
import { store, setState } from '../../data/store.js';
import { ARTIFACTS_CLOSED, artifactRevision, originLabel } from '../../data/artifacts-model.js';
import { artifactsOrigin, openArtifactsList } from '../../data/artifacts.js';
import { openOverlay, __resetOverlayHistoryForTests } from '../../data/overlay-history.js';

const originalFetch = globalThis.fetch;

// openOverlay skips its stack entirely when there is no history (bun has no
// DOM), so the ordering tests install the same minimal fake the
// overlay-history tests use.
function installFakeHistory() {
  const listeners = new Set();
  const history = { state: null, pushState() {}, back() {} };
  globalThis.window = {
    history,
    addEventListener(type, fn) { if (type === 'popstate') listeners.add(fn); },
    removeEventListener(type, fn) { if (type === 'popstate') listeners.delete(fn); },
  };
  globalThis.history = history;
}

function uninstallFakeHistory() {
  delete globalThis.window;
  delete globalThis.history;
}

beforeEach(() => {
  __resetOverlayHistoryForTests();
  globalThis.fetch = () => Promise.resolve(new Response(JSON.stringify({ artifacts: [] }), { status: 200 }));
  setState({
    artifacts: ARTIFACTS_CLOSED,
    sessions: {
      A: { id: 'A', title: 'ws race fix', messages: [] },
      B: { id: 'B', title: '', messages: [] },
    },
    isMobile: false,
    activeSession: null,
  });
});

afterEach(() => {
  globalThis.fetch = originalFetch;
  // The store is a singleton for the whole bun run: leaving isMobile true here
  // would hand the phone layout to unrelated test files.
  setState({ isMobile: false, activeSession: null });
  __resetOverlayHistoryForTests();
  uninstallFakeHistory();
});

// ── origin identity ──────────────────────────────────────────────────────────

test('the origin names the OWNER conversation, not the focused one', () => {
  openArtifactsList('A');
  setState({ isMobile: true, activeSession: 'B' });
  expect(artifactsOrigin(store.get())).toBe('ws race fix');
});

test('an untitled owner still gets a name rather than an empty label', () => {
  openArtifactsList('B');
  expect(artifactsOrigin(store.get())).toBe('Untitled');
});

test('a closed drawer has no origin', () => {
  expect(artifactsOrigin(store.get())).toBe('');
});

test('the origin selector is a stable string, so a streamed token does not re-render', () => {
  openArtifactsList('A');
  const before = artifactsOrigin(store.get());
  setState({ sessions: { ...store.get().sessions, A: { ...store.get().sessions.A, streamingText: 'tokens…' } } });
  expect(artifactsOrigin(store.get())).toBe(before);
  expect(Object.is(artifactsOrigin(store.get()), before)).toBe(true);
});

test('the origin changes with the owner when the drawer switches conversation', () => {
  openArtifactsList('A');
  expect(artifactsOrigin(store.get())).toBe('ws race fix');
  openArtifactsList('B');
  expect(artifactsOrigin(store.get())).toBe('Untitled');
});

test('the accessible name always carries the origin, even where it is hidden', () => {
  expect(originLabel('ws race fix', 'Artifacts')).toBe('Artifacts — ws race fix');
  expect(originLabel('ws race fix', 'report.md')).toBe('report.md — ws race fix');
  expect(originLabel('', 'Artifacts')).toBe('Artifacts');
});

// ── Escape ownership between stacked overlays ────────────────────────────────
// The drawer listens on capture, so it also sees keys aimed at overlays ABOVE
// it (an HtmlResourceInfo Sheet opened from a row). isTopOverlay is what keeps
// those keys with their own overlay.

test('the drawer only owns Escape while it is the top overlay', async () => {
  const { isTopOverlay } = await import('../../data/overlay-history.js');
  installFakeHistory();
  openOverlay('artifacts', () => {});
  expect(isTopOverlay('artifacts')).toBe(true);

  const closeSheet = openOverlay('sheet-1', () => {});
  // A Sheet (HtmlResourceInfo) is now on top: the drawer must not act.
  expect(isTopOverlay('artifacts')).toBe(false);
  expect(isTopOverlay('sheet-1')).toBe(true);

  closeSheet();
  expect(isTopOverlay('artifacts')).toBe(true);
});

test('a reader opened from the list owns Escape through its own entry', async () => {
  const { isTopOverlay } = await import('../../data/overlay-history.js');
  installFakeHistory();
  openOverlay('artifacts', () => {});
  openOverlay('artifact-reader', () => {});
  expect(isTopOverlay('artifact-reader')).toBe(true);
  expect(isTopOverlay('artifacts')).toBe(false);
});

test('isTopOverlay is false when nothing is open', async () => {
  const { isTopOverlay } = await import('../../data/overlay-history.js');
  expect(isTopOverlay('artifacts')).toBe(false);
});

// ── an open reader after a republication ────────────────────────────────────
// A resend keeps the same id and URL, so the reader's re-read cannot be keyed
// on those; artifactRevision is what the effect depends on.

test('the same id republished changes the revision, so an open reader re-reads', () => {
  const before = { id: 'f1', url: '/api/sessions/A/files/f1', name: 'report.md', mime: 'text/markdown', updatedAt: '2026-09-02T10:00:00Z' };
  const after = { ...before, updatedAt: '2026-09-02T11:30:00Z' };
  expect(artifactRevision(after)).not.toBe(artifactRevision(before));
});

test('a refresh that changed nothing keeps the revision, so the reader does not re-fetch', () => {
  const artifact = { id: 'f1', url: '/api/sessions/A/files/f1', name: 'report.md', mime: 'text/markdown', updatedAt: '2026-09-02T10:00:00Z' };
  expect(artifactRevision({ ...artifact })).toBe(artifactRevision(artifact));
  // Size is deliberately NOT part of it: it is last-observed metadata and the
  // cap is enforced against the real response, not against this number.
  expect(artifactRevision({ ...artifact, size: 999999 })).toBe(artifactRevision(artifact));
});

test('a republication that changes the name or MIME invalidates the reader kind too', () => {
  const md = { id: 'f1', url: '/api/sessions/A/files/f1', name: 'report.md', mime: 'text/markdown', updatedAt: 't1' };
  const html = { ...md, name: 'report.html', mime: 'text/html' };
  expect(artifactRevision(html)).not.toBe(artifactRevision(md));
  // The stale kind was the second half of the defect: an image that came back
  // as markdown must not keep rendering through the old renderer.
  const png = { ...md, name: 'shot.png', mime: 'image/png' };
  expect(artifactRevision(png)).not.toBe(artifactRevision(md));
});

test('a reader with no artifact has no revision', () => {
  expect(artifactRevision(null)).toBe('');
});
