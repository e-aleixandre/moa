import { expect, test } from "bun:test";
import {
  AT_BOTTOM_PX,
  bottomScrollTop,
  followsTail,
} from "./stream-scroll-policy.js";
import {
  capturePrependForSession,
  shouldLoadOlderHistory,
} from "./stream-scroll.js";
import { restorePrependAnchor } from "./stream-prepend-anchor.js";

function scroller({ scrollTop, scrollHeight, clientHeight = 100 }) {
  return { scrollTop, scrollHeight, clientHeight };
}

// The decision useStreamScroll applies on every scroll event, content resize
// and follow signal: `previousScrollTop` is where the last one left the reader.
function follows(following, previousScrollTop, el) {
  return followsTail(following, previousScrollTop, el.scrollTop, el.scrollHeight, el.clientHeight);
}

test.each([
  [20000, 5000, 5040],
  [6000, 5400, 5440],
  [3000, 4000, 4040],
  [2500, 8000, 8040],
])("a prepend restore is not read as rejoining the tail (%i + %i)", (oldHeight, inserted, restored) => {
  const el = {
    scrollTop: 40,
    scrollHeight: oldHeight,
    clientHeight: 800,
    getBoundingClientRect: () => ({ top: 0 }),
  };
  const node = {
    dataset: { streamAnchor: "reader" },
    getBoundingClientRect: () => ({ top: inserted + 40 }),
  };
  el.querySelectorAll = () => [node];
  const snapshot = { id: "reader", offset: 40, scrollTop: 40 };

  // Browser order: commit the prepend, restore its anchor, then deliver the
  // content ResizeObserver, which must leave the restored reader alone.
  el.scrollHeight += inserted;
  restorePrependAnchor(el, snapshot, false);
  expect(el.scrollTop).toBe(restored);
  expect(follows(false, 40, el)).toBe(false);
});

test("an in-flight page never captures an anchor from a newly selected session", () => {
  const el = {
    scrollTop: 32,
    getBoundingClientRect: () => ({ top: 0 }),
    querySelectorAll: () => [{
      dataset: { streamAnchor: "belongs-to-b" },
      getBoundingClientRect: () => ({ top: 32 }),
    }],
  };

  expect(capturePrependForSession("s2", "s1", el)).toBeNull();
});

test("a follower keeps following when streamed content grows", () => {
  const el = scroller({ scrollTop: 900, scrollHeight: 1200 });
  expect(follows(true, 900, el)).toBe(true);
});

test("a reader above the transcript is not pulled back by growth", () => {
  const el = scroller({ scrollTop: 400, scrollHeight: 1200 });
  expect(follows(false, 400, el)).toBe(false);
});

test("a momentum scroll that precedes its scroll event leaves the tail", () => {
  // iOS has updated scrollTop visually, but has not delivered `scroll` yet.
  const el = scroller({ scrollTop: 500, scrollHeight: 1200 });
  expect(follows(true, 900, el)).toBe(false);
});

// Measured in a live session: the transcript shrank 86px (2782 -> 2696) and
// the browser clamped scrollTop from 2326 to 2240. Judged against the old
// height that looked like a reader 86px up, and "Latest" appeared at the bottom.
test("content that shrinks under a follower keeps it following", () => {
  const el = scroller({ scrollTop: 2240, scrollHeight: 2696, clientHeight: 456 });
  expect(follows(true, 2326, el)).toBe(true);
});

test("a short gesture up leaves the tail and stays out while text streams", () => {
  const el = scroller({ scrollTop: 2296, scrollHeight: 2782, clientHeight: 456 });
  // One 30px wheel or trackpad step from the pinned bottom (2326).
  expect(follows(true, 2326, el)).toBe(false);

  // The next deltas grow the content without moving the reader.
  el.scrollHeight = 2900;
  expect(follows(false, 2296, el)).toBe(false);
});

test("only reaching the bottom rejoins the tail", () => {
  const el = scroller({ scrollTop: 2400, scrollHeight: 2900, clientHeight: 456 });
  expect(follows(false, 2296, el)).toBe(false);

  el.scrollTop = 2444;
  expect(follows(false, 2400, el)).toBe(true);
});

test("a session change forces the new transcript to its bottom", () => {
  const el = scroller({ scrollTop: 250, scrollHeight: 1200 });

  el.scrollTop = bottomScrollTop(el.scrollHeight, el.clientHeight);

  expect(el.scrollTop).toBe(1100);
});

test("a session change drops a leftover offset past the new transcript", () => {
  const el = scroller({ scrollTop: 19200, scrollHeight: 800, clientHeight: 700 });
  el.scrollTop = 0;
  el.scrollTop = bottomScrollTop(el.scrollHeight, el.clientHeight);
  expect(el.scrollTop).toBe(100);
});

test("the bottom tolerates sub-pixel scroll positions only", () => {
  expect(AT_BOTTOM_PX).toBe(4);
  expect(follows(false, 1096, scroller({ scrollTop: 1096.5, scrollHeight: 1200 }))).toBe(true);
  expect(follows(false, 1096, scroller({ scrollTop: 1096, scrollHeight: 1200 }))).toBe(false);
});

test("a completed top page cannot cascade until the reader leaves the top", () => {
  const el = scroller({ scrollTop: 0, scrollHeight: 1000 });
  const paging = { hasMore: true, loading: false };

  expect(shouldLoadOlderHistory(el, paging, true)).toBe(true);
  // useStreamScroll disarms before making the request, so a later scroll
  // event while the restored reader remains near the top cannot load again.
  expect(shouldLoadOlderHistory(el, paging, false)).toBe(false);

  el.scrollTop = 120;
  expect(shouldLoadOlderHistory(el, paging, true)).toBe(false);
  el.scrollTop = 0;
  expect(shouldLoadOlderHistory(el, paging, true)).toBe(true);
});
