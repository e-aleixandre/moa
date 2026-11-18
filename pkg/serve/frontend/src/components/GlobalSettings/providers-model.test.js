// providers-model.test.js — what each row says and offers, owner vs device.
import { test, expect } from "bun:test";
import {
  REPLACE_NOTICE, accountBars, accountReading, accountUsageNote, removeAccountWarning, flowErrorCopy, providersValue, replaceNotice, rowActions, rowReading,
} from "./providers-model.js";

const row = (over = {}) => ({
  id: "anthropic", source: "store", kind: "api_key", credential_generation: "g", state: "ready",
  attention: false, actions: ["sign_in", "api_key"], oauth_enabled: true, api_key_enabled: true, ...over,
});

test("each state reads as the next step, owner side", () => {
  expect(rowReading(row({ state: "missing", kind: undefined }), true).text).toBe("Not connected");
  expect(rowReading(row({ state: "renew_on_use", kind: "oauth" }), true).text).toBe("Renews automatically on next use");
  expect(rowReading(row({ state: "reconnect", kind: "oauth" }), true).text).toBe("Sign-in expired. Reconnect.");
  expect(rowReading(row({ state: "key_rejected" }), true).text).toBe("API key rejected. Replace it.");
  expect(rowReading(row({ state: "save_failed", kind: undefined }), true).text).toBe("Could not save credentials.");
  expect(rowReading(row({ state: "store_unavailable", kind: undefined }), true).text)
    .toBe("The credential file can't be read. Repair it on the server.");
  expect(rowReading(row({ source: "env", state: "ready" }), true).text).toBe("Managed by environment");
});

test("a device is told who to ask", () => {
  expect(rowReading(row({ state: "reconnect", kind: "oauth" }), false).text).toBe("Ask the owner to reconnect Anthropic.");
  expect(rowReading(row({ state: "key_rejected" }), false).text).toBe("Ask the owner to replace the Anthropic API key.");
});

test("a device row never offers an action, even if the DTO had some", () => {
  expect(rowActions(row({ state: "key_rejected" }), false)).toEqual({ signIn: null, addAccount: null, apiKey: null, retrySave: false, primary: null });
});

test("environment and an unreadable store offer no buttons", () => {
  expect(rowActions(row({ source: "env", actions: [] }), true)).toEqual({ signIn: null, addAccount: null, apiKey: null, retrySave: false, primary: null });
  expect(rowActions(row({ state: "store_unavailable", actions: ["sign_in"] }), true).signIn).toBeNull();
});

test("labels follow what is there now", () => {
  expect(rowActions(row({ kind: undefined, state: "missing" }), true)).toMatchObject({ signIn: "Sign in", apiKey: "Add API key" });
  expect(rowActions(row({ kind: "api_key" }), true)).toMatchObject({ signIn: "Use subscription", apiKey: "Replace API key" });
  expect(rowActions(row({ kind: "oauth" }), true)).toMatchObject({ signIn: "Reconnect", apiKey: "Use API key" });
  expect(rowActions(row({ state: "save_failed", actions: ["sign_in", "api_key", "retry_save"] }), true).retrySave).toBe(true);
  expect(rowActions(row({ oauth_enabled: false }), true).signIn).toBeNull();
});

test("replacing names its reach, and the billing switch when there is one", () => {
  expect(replaceNotice(row({ state: "missing", kind: undefined }), "oauth")).toBeNull();
  expect(replaceNotice(row({ kind: "oauth" }), "oauth")).toEqual({ text: REPLACE_NOTICE, billing: "" });
  expect(replaceNotice(row({ kind: "api_key" }), "oauth").billing).toBe("Anthropic will bill your subscription instead of API billing.");
  expect(replaceNotice(row({ kind: "oauth" }), "api_key").billing).toBe("Anthropic will use API billing instead of your subscription.");
  expect(REPLACE_NOTICE).toBe("Applies to all sessions on the next request. Existing history and attachments may be sent using this account.");
});

test("the root row states the badge in words", () => {
  expect(providersValue({ loaded: true, attentionCount: 1, providers: [] })).toBe("1 provider needs attention");
  expect(providersValue({ loaded: true, attentionCount: 0, providers: [row(), row({ state: "missing" })] })).toBe("1 connected");
  expect(providersValue({ loaded: false })).toBe("—");
});

test("an error shows fixed copy, never the response body", () => {
  const raw = new Error('500: {"weird":"upstream said sk-live-123"}');
  raw.status = 500;
  expect(flowErrorCopy(raw)).toBe("Something went wrong. Try again.");
  const plain = new Error("403: forbidden");
  plain.status = 403;
  expect(flowErrorCopy(plain)).toBe("Only the owner can change providers here.");
  expect(flowErrorCopy(new TypeError("Failed to fetch"))).toBe("Couldn't reach moa. Try again.");
});

test("primary is the action the row's state needs", () => {
  expect(rowActions(row({ state: "key_rejected", kind: "api_key" }), true).primary).toBe("apiKey");
  expect(rowActions(row({ state: "reconnect", kind: "oauth" }), true).primary).toBe("signIn");
  expect(rowActions(row({ state: "save_failed", actions: ["sign_in", "api_key", "retry_save"] }), true).primary).toBe("retrySave");
  expect(rowActions(row({ state: "missing", kind: undefined }), true).primary).toBe("signIn");
  expect(rowActions(row({ state: "key_rejected", api_key_enabled: false }), true).primary).toBe("signIn");
});

const ACCOUNTS = [
  { generation: "g1", position: 1, label: "a@x.com", state: "available" },
  { generation: "g2", position: 2, label: "Account 2", state: "limited", limited_until: "2026-11-17T14:00:00Z" },
];

test("with plan accounts listed, the row adds an account instead of one sign-in", () => {
  const plan = row({ kind: "oauth", accounts: ACCOUNTS });
  const acts = rowActions(plan, true);
  expect(acts.signIn).toBeNull();
  expect(acts.addAccount).toBe("Add another account");
  // A device never gets the button.
  expect(rowActions(plan, false).addAccount).toBeNull();
  // Without accounts the row signs in as before.
  expect(rowActions(row({ kind: "oauth" }), true)).toMatchObject({ signIn: "Reconnect", addAccount: null });
});

test("an account reads available, exhausted until a time, or needing a reconnect", () => {
  const now = new Date("2026-11-17T09:00:00Z");
  expect(accountReading(ACCOUNTS[0], now).text).toBe("Available");
  const limited = accountReading(ACCOUNTS[1], now);
  expect(limited.text).toMatch(/^Exhausted until \d/);
  expect(limited.tone).toBe("warn");
  const later = accountReading({ ...ACCOUNTS[1], limited_until: "2026-11-20T14:00:00Z" }, now);
  expect(later.text).toMatch(/^Exhausted until [A-Za-z]+ /);
  expect(accountReading({ state: "reconnect" }, now)).toEqual({ text: "Sign-in expired. Reconnect.", tone: "bad" });
});

test("an account's bars are its 5h and weekly readings, or nothing", () => {
  const entry = { usage: { five_hour: { utilization: 91.4, resets_at: "2026-11-17T18:00:00Z" }, seven_day: { utilization: 45 } } };
  expect(accountBars(entry)).toEqual([
    { label: "5h", pct: 91, resetsAt: "2026-11-17T18:00:00Z" },
    { label: "Week", pct: 45, resetsAt: null },
  ]);
  expect(accountBars({ usage: { five_hour: { utilization: "x" } } })).toBeNull();
  expect(accountBars({ reason: "temporarily_unavailable" })).toBeNull();
  expect(accountBars(undefined)).toBeNull();
  expect(accountUsageNote({ reason: "temporarily_unavailable" })).toBe("Usage unavailable for now");
  expect(accountUsageNote({ reason: "pending" })).toBe("");
});

test("only the last account with a key beside it warns on removal", () => {
  const one = row({ kind: "oauth", accounts: [ACCOUNTS[0]], plan_api_key: { generation: "k", state: "configured" } });
  expect(removeAccountWarning(one)).toMatch(/paid with the API key/);
  expect(removeAccountWarning({ ...one, accounts: ACCOUNTS })).toBe("");
  expect(removeAccountWarning({ ...one, plan_api_key: undefined })).toBe("");
});
