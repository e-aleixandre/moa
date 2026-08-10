import { expect, test } from "bun:test";
import {
  AT_BOTTOM_PX,
  bottomScrollTop,
  scrollTopAfterContentResize,
} from "./stream-scroll-policy.js";

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

test("the 80 pixel following threshold is evaluated before content grows", () => {
  expect(AT_BOTTOM_PX).toBe(80);

  const withinThreshold = scroller({ scrollTop: 821, scrollHeight: 1000 });
  observeContentResize(Object.assign(withinThreshold, { scrollHeight: 1200 }), 1000);
  expect(withinThreshold.scrollTop).toBe(1100);

  const outsideThreshold = scroller({ scrollTop: 820, scrollHeight: 1000 });
  observeContentResize(Object.assign(outsideThreshold, { scrollHeight: 1200 }), 1000);
  expect(outsideThreshold.scrollTop).toBe(820);
});
