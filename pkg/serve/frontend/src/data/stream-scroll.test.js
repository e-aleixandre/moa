import { expect, test } from "bun:test";
import {
  AT_BOTTOM_PX,
  bottomScrollTop,
  isAtBottom,
  scrollTopAfterContentResize,
} from "./stream-scroll-policy.js";
import {
  capturePrependForSession,
  restorePrependLayout,
  shouldLoadOlderHistory,
} from "./stream-scroll.js";

class FakeResizeObserver {
  constructor(callback) {
    this.callback = callback;
  }

  observe() {}

  resize() {
    this.callback();
  }
}

function scroller({ scrollTop, scrollHeight, clientHeight = 100 }) {
  return { scrollTop, scrollHeight, clientHeight };
}

// This is the ResizeObserver decision consumed by useStreamScroll: preserve
// the height from before the resize, then apply its returned scroll position.
function observeContentResize(el, previousScrollHeight) {
  el.scrollTop = scrollTopAfterContentResize(
    el.scrollTop,
    previousScrollHeight,
    el.scrollHeight,
    el.clientHeight
  );
}

function deliverGeneralResizeObserver(el, observedScrollHeight) {
  const previousScrollHeight = observedScrollHeight.current;
  observedScrollHeight.current = el.scrollHeight;
  const nextScrollTop = scrollTopAfterContentResize(
    el.scrollTop,
    previousScrollHeight,
    el.scrollHeight,
    el.clientHeight
  );
  const following = nextScrollTop !== el.scrollTop || isAtBottom(
    el.scrollTop,
    previousScrollHeight,
    el.clientHeight
  );
  if (following) el.scrollTop = bottomScrollTop(el.scrollHeight, el.clientHeight);
}

test.each([
  [20000, 5000, 5040],
  [6000, 5400, 5440],
  [3000, 4000, 4040],
  [2500, 8000, 8040],
])("a prepend absorbs its resize baseline (%i + %i)", (oldHeight, inserted, restored) => {
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
  const observedScrollHeight = { current: oldHeight };

  // This is the browser order: commit the prepend, restore its anchor, then
  // deliver the general content ResizeObserver.
  el.scrollHeight += inserted;
  restorePrependLayout(el, snapshot, false, observedScrollHeight);
  expect(el.scrollTop).toBe(restored);
  deliverGeneralResizeObserver(el, observedScrollHeight);

  expect(el.scrollTop).toBe(restored);
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

test("a follower is repinned when streamed content grows", () => {
  const el = scroller({ scrollTop: 900, scrollHeight: 1000 });
  const observer = new FakeResizeObserver(() => observeContentResize(el, 1000));

  el.scrollHeight = 1200;
  observer.resize();

  expect(el.scrollTop).toBe(1100);
});

test("a reader above the transcript keeps their scroll position on resize", () => {
  const el = scroller({ scrollTop: 400, scrollHeight: 1000 });
  const observer = new FakeResizeObserver(() => observeContentResize(el, 1000));

  el.scrollHeight = 1200;
  observer.resize();

  expect(el.scrollTop).toBe(400);
});

test("a momentum scroll that precedes its scroll event is not repinned by resize", () => {
  const el = scroller({ scrollTop: 900, scrollHeight: 1000 });
  const observer = new FakeResizeObserver(() => observeContentResize(el, 1000));

  // iOS has updated scrollTop visually, but has not delivered `scroll` yet.
  el.scrollTop = 500;
  el.scrollHeight = 1200;
  observer.resize();

  expect(el.scrollTop).toBe(500);
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

test("the 80 pixel following threshold is evaluated before content grows", () => {
  expect(AT_BOTTOM_PX).toBe(80);

  const withinThreshold = scroller({ scrollTop: 821, scrollHeight: 1000 });
  observeContentResize(Object.assign(withinThreshold, { scrollHeight: 1200 }), 1000);
  expect(withinThreshold.scrollTop).toBe(1100);

  const outsideThreshold = scroller({ scrollTop: 820, scrollHeight: 1000 });
  observeContentResize(Object.assign(outsideThreshold, { scrollHeight: 1200 }), 1000);
  expect(outsideThreshold.scrollTop).toBe(820);
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
