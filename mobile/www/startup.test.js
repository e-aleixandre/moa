import { describe, expect, it } from "bun:test";
import { readFileSync } from "node:fs";
import { startApp } from "./startup.js";

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((onResolve, onReject) => {
    resolve = onResolve;
    reject = onReject;
  });
  return { promise, resolve, reject };
}

function launchActions(authorizeDevice = async () => {}) {
  const calls = [];
  const scheduled = [];
  return {
    calls,
    scheduled,
    actions: {
      authorizeDevice,
      bindNativeServer: async (origin) => calls.push(["bind", origin]),
      clearNativeServer: async () => calls.push(["clear"]),
      forgetServer: () => calls.push(["forget"]),
      navigate: (origin) => calls.push(["navigate", origin]),
      showPairing: (message) => calls.push(["pairing", message]),
      showOpening: () => calls.push(["opening"]),
      showConnectionError: (message) => calls.push(["connection-error", message]),
      scheduleOpening: (callback) => {
        scheduled.push(callback);
        return callback;
      },
      cancelOpening: (callback) => calls.push(["cancel-opening", callback]),
    },
  };
}

describe("app launch", () => {
  it("keeps pairing hidden before the launch decision runs", () => {
    const html = readFileSync(new URL("./index.html", import.meta.url), "utf8");
    const pairing = html.match(/<main[^>]*id="pairing"[^>]*>/)?.[0];

    expect(pairing).toBeDefined();
    expect(pairing).toContain("hidden");
  });

  it("never presents pairing while a saved server resumes normally", async () => {
    const authorization = deferred();
    const launch = launchActions(() => authorization.promise);
    const started = startApp("https://moa.example", launch.actions);

    expect(launch.calls).toEqual([]);
    authorization.resolve();
    await started;

    expect(launch.calls.some(([name]) => name === "pairing")).toBe(false);
    expect(launch.calls).toContainEqual(["navigate", "https://moa.example"]);
  });

  it("shows a neutral opening state only when resuming takes long enough", async () => {
    const authorization = deferred();
    const launch = launchActions(() => authorization.promise);
    const started = startApp("https://moa.example", launch.actions);

    expect(launch.calls).toEqual([]);
    launch.scheduled[0]();
    expect(launch.calls).toEqual([["opening"]]);

    authorization.resolve();
    await started;
  });

  it("presents pairing immediately when no server is saved", async () => {
    const launch = launchActions();

    await startApp(null, launch.actions);

    expect(launch.calls).toEqual([["pairing", undefined]]);
    expect(launch.scheduled).toHaveLength(0);
  });

  it("returns a revoked device to usable pairing and clears its binding", async () => {
    const launch = launchActions(async () => {
      throw { code: "not_paired" };
    });

    await startApp("https://moa.example", launch.actions);

    expect(launch.calls.map(([name]) => name)).toEqual([
      "cancel-opening", "forget", "clear", "pairing",
    ]);
    expect(launch.calls.at(-1)[1]).toContain("no longer paired");
  });

  it("preserves the saved credential after a network failure", async () => {
    const launch = launchActions(async () => {
      throw { code: "unavailable" };
    });

    await startApp("https://moa.example", launch.actions);

    expect(launch.calls.some(([name]) => name === "forget")).toBe(false);
    expect(launch.calls.at(-1)[0]).toBe("connection-error");
  });
});
