import { test, expect, afterEach } from "bun:test";
import {
  callLevelPlan, ownerCallLevel, hoursBlock, ringSecondsValue, pauseUntil, pausedNow, callsPatch,
  setOwnerCallLevel, callLevelFailure, NO_PLAN_COPY,
} from "./owner-calls.js";

const infra = { id: "own_infra", name: "Infra" };
const providers = [
  { id: "xai-api", billing: { kind: "per_minute", usd_per_minute: 0.08 } },
  { id: "openai-live-api", billing: { kind: "per_minute", usd_per_minute: 0.05 } },
];

test("an owner without call_level is Never", () => {
  expect(ownerCallLevel(infra)).toBe("never");
  expect(ownerCallLevel({ call_level: "ring" })).toBe("ring");
  expect(ownerCallLevel({ call_level: "bogus" })).toBe("never");
});

test("with a plan, choosing Ask first or Ring sends it straight away", () => {
  const status = { default: "chatgpt-plan", providers };
  expect(callLevelPlan(status, infra, "ask_first")).toEqual({ kind: "set" });
  expect(callLevelPlan(status, infra, "ring")).toEqual({ kind: "set" });
});

test("Never never asks for anything", () => {
  expect(callLevelPlan({ default: "", owner_calls_paid_provider: "xai-api", providers }, infra, "never")).toEqual({ kind: "set" });
});

test("without a plan the cost is confirmed, naming provider and price", () => {
  const plan = callLevelPlan({ default: "", owner_calls_paid_provider: "xai-api", providers }, infra, "ring");
  expect(plan.kind).toBe("confirm");
  expect(plan.provider).toBe("xai-api");
  expect(plan.text).toBe("No voice plan is signed in. Calls from Infra will use Grok API at $0.08/min, billed by the provider.");
  const openai = callLevelPlan({ default: "", owner_calls_paid_provider: "openai-live-api", providers }, infra, "ask_first");
  expect(openai.text).toContain("OpenAI API at $0.05/min");
});

test("no plan and no paid key says where to sign in", () => {
  const plan = callLevelPlan({ default: "", providers }, infra, "ring");
  expect(plan).toEqual({ kind: "blocked", text: "Sign in to a voice plan under Providers" });
  expect(callLevelFailure({ status: 400 })).toBe(NO_PLAN_COPY);
});

const realFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = realFetch; });

test("the level goes in its own PATCH, with the acknowledged provider only when given", async () => {
  const calls = [];
  globalThis.fetch = async (url, init) => {
    calls.push({ url, method: init.method, body: init.body ? JSON.parse(init.body) : null });
    return new Response(init.method === "PATCH" ? "{}" : "[]", { status: 200 });
  };
  await setOwnerCallLevel("own_infra", "ring", "xai-api");
  await setOwnerCallLevel("own_infra", "never");
  const patches = calls.filter((c) => c.method === "PATCH");
  expect(patches[0]).toEqual({ url: "/api/owners/own_infra", method: "PATCH", body: { call_level: "ring", cost_ack_provider: "xai-api" } });
  expect(patches[1].body).toEqual({ call_level: "never" });
});

test("a rejected level leaves the error to the caller", async () => {
  globalThis.fetch = async () => new Response("no plan", { status: 400 });
  await expect(setOwnerCallLevel("own_infra", "ring")).rejects.toMatchObject({ status: 400 });
});

test("hours: any time is null; both times needed; From and Until differ", () => {
  expect(hoursBlock({ from: "", until: "" })).toEqual({ hours: null });
  expect(hoursBlock({ from: "09:00", until: "" }).error).toBeTruthy();
  expect(hoursBlock({ from: "25:00", until: "10:00" }).error).toBeTruthy();
  expect(hoursBlock({ from: "09:00", until: "09:00" }).error).toBeTruthy();
  expect(hoursBlock({ from: "22:00", until: "07:00" }, "Europe/Madrid")).toEqual({ hours: { from: "22:00", until: "07:00", tz: "Europe/Madrid" } });
});

test("ring seconds are 10 to 120", () => {
  expect(ringSecondsValue("45")).toEqual({ seconds: 45 });
  expect(ringSecondsValue("9").error).toBeTruthy();
  expect(ringSecondsValue("121").error).toBeTruthy();
  expect(ringSecondsValue("").error).toBeTruthy();
  expect(ringSecondsValue("abc").error).toBeTruthy();
});

test("pause: 1 h, 4 h and tomorrow 08:00 local, as RFC3339", () => {
  const now = new Date(2026, 9, 16, 21, 30, 0);
  expect(Date.parse(pauseUntil("1h", now)) - now.getTime()).toBe(3600e3);
  expect(Date.parse(pauseUntil("4h", now)) - now.getTime()).toBe(4 * 3600e3);
  const t = new Date(pauseUntil("tomorrow", now));
  expect([t.getDate(), t.getHours(), t.getMinutes()]).toEqual([17, 8, 0]);
  expect(pauseUntil("1h", now)).toMatch(/^\d{4}-\d\d-\d\dT.*Z$/);
  expect(pausedNow({ paused_until: pauseUntil("1h", now) }, now.getTime())).toBe(true);
  expect(pausedNow({ paused_until: pauseUntil("1h", now) }, now.getTime() + 2 * 3600e3)).toBe(false);
  expect(pausedNow({}, 0)).toBe(false);
});

test("the calls block is replaced whole: untouched parts travel as they are", () => {
  const calls = { ring_seconds: 30, hours: { from: "09:00", until: "18:00", tz: "UTC" } };
  expect(callsPatch(calls, { paused_until: "2026-10-17T06:00:00.000Z" })).toEqual({
    calls: { ring_seconds: 30, hours: calls.hours, paused_until: "2026-10-17T06:00:00.000Z" },
  });
  expect(callsPatch(calls, { hours: null })).toEqual({ calls: { ring_seconds: 30 } });
  expect(callsPatch({ paused_until: "x" }, { paused_until: "" })).toEqual({ calls: {} });
});
