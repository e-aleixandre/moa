import { expect, test } from "bun:test";
import { accountLabel, anthropicAccounts, servingAccount } from "./anthropic-accounts.js";

const ACCOUNTS = [
  { generation: "g1", position: 1, label: "ana@example.com" },
  { generation: "g2", position: 2, label: "Account 2" },
  { generation: "g3", position: 3, label: "bea@example.com" },
];
const status = { providers: [{ id: "openai" }, { id: "anthropic", accounts: ACCOUNTS }] };

test("accounts come from the owner's Anthropic row, and only from there", () => {
  expect(anthropicAccounts(status)).toEqual(ACCOUNTS);
  expect(anthropicAccounts({ providers: [{ id: "anthropic" }] })).toEqual([]);
  expect(anthropicAccounts(null)).toEqual([]);
});

test("an account is named by its generation first, then by its place", () => {
  expect(accountLabel(ACCOUNTS, { generation: "g3", position: 2 })).toBe("bea@example.com");
  // A reading from before the list was loaded or reordered still gets its place's name.
  expect(accountLabel(ACCOUNTS, { generation: "gone", position: 1 })).toBe("ana@example.com");
  expect(accountLabel(ACCOUNTS, { position: 2 })).toBe("Account 2");
});

test("without the list, or without the account, it is Account N; with nothing, empty", () => {
  expect(accountLabel([], { generation: "g2", position: 2 })).toBe("Account 2");
  expect(accountLabel(ACCOUNTS, { generation: "gone", position: 9 })).toBe("Account 9");
  expect(accountLabel(ACCOUNTS, {})).toBe("");
});

test("a run is served by a later account only while it runs on the plan", () => {
  const source = { kind: "oauth", account: 2, primary_generation: "g2" };
  expect(servingAccount({ phase: "working", source })).toEqual({ generation: "g2", position: 2 });
  expect(servingAccount({ phase: "", source })).toBeNull();
  expect(servingAccount({ phase: "working", source: { ...source, account: undefined } })).toBeNull();
  expect(servingAccount({ phase: "working", source: { ...source, account: 1 } })).toBeNull();
  expect(servingAccount({ phase: "working", source: { kind: "api_backup", account: 2 } })).toBeNull();
  expect(servingAccount(null)).toBeNull();
});
