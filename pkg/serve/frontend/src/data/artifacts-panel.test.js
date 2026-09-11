import { expect, test } from "bun:test";
import { acceptsResponse, EMPTY_ARTIFACTS } from "./artifacts-model.js";

// The dossier's artifacts page asked the server for the collection and threw
// every answer away. Not a rendering bug and not a server bug: the API
// returned 200 with three artifacts while the page said "No files in this
// conversation yet".
//
// acceptsResponse refuses a response whose slice has no `view`, because that
// guard is what stops a late answer from a closed drawer landing on whatever
// is on screen now. The panel called loadArtifacts() without claiming a view,
// so it failed that check on arrival, every time.
//
// The fix is a view of its own, 'panel', that no drawer renders. These tests
// pin both halves: the response is accepted, and the guard still rejects the
// case it exists for.

const slice = (over = {}) => ({
  view: null, ownerSessionId: null, token: 0, status: "idle",
  error: null, items: EMPTY_ARTIFACTS, fileId: null, from: null,
  expanded: false, seed: null, ...over,
});

test("the dossier's own page accepts the collection it asked for", () => {
  const panel = slice({ view: "panel", ownerSessionId: "s1", token: 4 });
  expect(acceptsResponse(panel, { sessionId: "s1", token: 4 })).toBe(true);
});

test("a response with no view is still refused", () => {
  // This is the state the panel was in, and why it saw nothing.
  const viewless = slice({ view: null, ownerSessionId: "s1", token: 4 });
  expect(acceptsResponse(viewless, { sessionId: "s1", token: 4 })).toBe(false);
});

test("the guard still protects a fast switch between conversations", () => {
  const panel = slice({ view: "panel", ownerSessionId: "s2", token: 5 });
  // A's answer arriving after the panel moved to B.
  expect(acceptsResponse(panel, { sessionId: "s1", token: 5 })).toBe(false);
  // B's own stale request, superseded by a newer one.
  expect(acceptsResponse(panel, { sessionId: "s2", token: 4 })).toBe(false);
});
