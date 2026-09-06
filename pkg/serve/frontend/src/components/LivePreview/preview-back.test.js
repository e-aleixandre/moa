import { expect, test } from "bun:test";
import { applyReport, backTitle, canGoBack, needsReturnToApp, newEpoch, press, resetBack, shouldAutoReturn, INITIAL_BACK } from "./preview-back.js";

const hello = (state) => newEpoch(state);
const report = (state, extra) => applyReport(state, { navigationEpoch: state.epoch, supported: true, ...extra });

test("a preview that has not answered yet cannot be gone back in", () => {
  expect(canGoBack(INITIAL_BACK)).toBe(false);
  expect(backTitle(INITIAL_BACK)).toBe("Back in preview — waiting for the app");
  expect(press(INITIAL_BACK).command).toBe(null);
});

test("the first page of a preview says so, and stays disabled", () => {
  const state = report(hello(INITIAL_BACK), { canGoBack: false });
  expect(canGoBack(state)).toBe(false);
  expect(backTitle(state)).toBe("Back in preview — no earlier page");
  expect(press(state).command).toBe(null);
});

test("a browser without the Navigation API fails closed and says why", () => {
  const state = applyReport(hello(INITIAL_BACK), { navigationEpoch: 1, supported: false, canGoBack: true });
  expect(canGoBack(state)).toBe(false);
  expect(backTitle(state)).toBe("Back in preview — not available in this browser");
  expect(press(state).command).toBe(null);
});

test("a page with an earlier entry enables Back and sends exactly one command", () => {
  const state = report(hello(INITIAL_BACK), { canGoBack: true });
  expect(canGoBack(state)).toBe(true);
  expect(backTitle(state)).toBe("Back in preview");

  const first = press(state);
  expect(first.command).toEqual({ type: "moa-preview-back", navigationEpoch: state.epoch });
  // The click is its own guard: while the command is in flight the control is
  // busy, and a second press produces nothing.
  expect(canGoBack(first.state)).toBe(false);
  expect(press(first.state).command).toBe(null);
});

test("a report minted by the document being replaced cannot re-enable Back", () => {
  const old = report(hello(INITIAL_BACK), { canGoBack: true });
  const loading = newEpoch(old);
  expect(canGoBack(loading)).toBe(false);

  const stale = applyReport(loading, { navigationEpoch: old.epoch, supported: true, canGoBack: true });
  expect(stale).toBe(loading);
  expect(canGoBack(stale)).toBe(false);

  // Only the new document's own answer counts.
  expect(canGoBack(report(loading, { canGoBack: true }))).toBe(true);
});

test("a cancelled traversal leaves busy for whatever the app now reports", () => {
  const available = report(hello(INITIAL_BACK), { canGoBack: true });
  const busy = press(available).state;

  const recovered = applyReport(busy, { navigationEpoch: busy.epoch, supported: true, canGoBack: false });
  expect(recovered.status).toBe("first");
  expect(canGoBack(recovered)).toBe(false);
});

test("a pending user Back SecurityError authorizes exactly one parent return", () => {
  const available = report(hello(INITIAL_BACK), { canGoBack: true });
  const busy = press(available).state;
  const failure = {
    navigationEpoch: busy.epoch,
    supported: true,
    canGoBack: true,
    backError: "SecurityError",
  };
  expect(shouldAutoReturn(busy, failure)).toBe(true);
  const blocked = applyReport(busy, failure);

  expect(blocked.status).toBe("blocked");
  expect(blocked.nativeBackBlocked).toBe(true);
  expect(canGoBack(blocked)).toBe(false);
  expect(backTitle(blocked)).toBe("Back in preview");

  // The parent reload invalidates the old epoch synchronously, so the second
  // rejection emitted by Navigation.back() cannot reload it again.
  const reloaded = resetBack(blocked);
  expect(shouldAutoReturn(reloaded, failure)).toBe(false);
  const stale = applyReport(reloaded, failure);
  expect(stale).toBe(reloaded);
});

test("a known blocked browser sends a later user Back straight to the parent return", () => {
  const blocked = { ...report(hello(INITIAL_BACK), { canGoBack: true }), nativeBackBlocked: true };
  const next = press(blocked);

  expect(next.command).toEqual({ type: "moa-preview-return", navigationEpoch: blocked.epoch });
});

test("unsolicited, stale, and completed SecurityErrors cannot return the parent", () => {
  const available = report(hello(INITIAL_BACK), { canGoBack: true });
  const error = { navigationEpoch: available.epoch, supported: true, canGoBack: true, backError: "SecurityError" };

  expect(shouldAutoReturn(available, error)).toBe(false);
  expect(applyReport(available, error)).toBe(available);

  const busy = press(available).state;
  expect(shouldAutoReturn(busy, { ...error, navigationEpoch: busy.epoch - 1 })).toBe(false);

  const completed = applyReport(busy, { navigationEpoch: busy.epoch, supported: true, canGoBack: false });
  expect(shouldAutoReturn(completed, error)).toBe(false);
});

test("a successful native Back completion never authorizes a parent return", () => {
  const busy = press(report(hello(INITIAL_BACK), { canGoBack: true })).state;
  const completed = applyReport(busy, {
    navigationEpoch: busy.epoch,
    supported: true,
    canGoBack: false,
  });

  expect(completed.status).toBe("first");
  expect(shouldAutoReturn(busy, { navigationEpoch: busy.epoch })).toBe(false);
});

test("a non-security traversal failure remains recoverable through a fresh capability report", () => {
  const busy = press(report(hello(INITIAL_BACK), { canGoBack: true })).state;
  const fresh = applyReport(busy, {
    navigationEpoch: busy.epoch,
    supported: true,
    canGoBack: true,
    backError: "AbortError",
  });

  expect(fresh.status).toBe("available");
  expect(needsReturnToApp(fresh)).toBe(false);
});

test("a stale SecurityError cannot block a newer document", () => {
  const old = report(hello(INITIAL_BACK), { canGoBack: true });
  const current = newEpoch(old);
  const stale = applyReport(current, {
    navigationEpoch: old.epoch,
    supported: true,
    canGoBack: true,
    backError: "SecurityError",
  });

  expect(stale).toBe(current);
  expect(stale.status).toBe("loading");
});

test("a reload, a target change or a closed panel disables Back and invalidates the old epoch", () => {
  const available = report(hello(INITIAL_BACK), { canGoBack: true });
  const reset = resetBack(available);

  expect(canGoBack(reset)).toBe(false);
  expect(reset.epoch).not.toBe(available.epoch);
  expect(applyReport(reset, { navigationEpoch: available.epoch, supported: true, canGoBack: true })).toBe(reset);
});

test("a packet with no epoch at all is ignored", () => {
  const state = hello(INITIAL_BACK);
  expect(applyReport(state, { supported: true, canGoBack: true })).toBe(state);
  expect(applyReport(state, null)).toBe(state);
});
