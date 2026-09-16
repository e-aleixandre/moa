import { expect, test } from "bun:test";

const recovery = await Bun.file("ios/App/App/MoaBridgeViewController.swift").text();

test("iOS recovery keeps Capacitor's delegate and the paired origin as its boundary", () => {
  expect(recovery).toContain("forwardingTarget(for aSelector:");
  expect(recovery).toContain("capacitorDelegate?.webView?");
  expect(recovery).toContain("NativeServerBinding.matches(retryURL)");
  expect(recovery).toContain("webView.navigationDelegate = recovery");
});

test("iOS recovery uses failures, foreground and reachability without retrying HTTP errors", () => {
  expect(recovery).toContain("didFailProvisionalNavigation");
  expect(recovery).toContain("NWPathMonitor()");
  expect(recovery).toContain("UIApplication.didBecomeActiveNotification");
  expect(recovery).toContain("retryDelays: [TimeInterval] = [1, 2, 4, 8, 15]");
  expect(recovery).not.toContain("NSURLErrorBadServerResponse");
  expect(recovery).not.toContain("NSURLErrorUserAuthenticationRequired");
});
