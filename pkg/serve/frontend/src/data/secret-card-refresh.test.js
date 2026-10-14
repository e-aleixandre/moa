import { test, expect } from "bun:test";
import { createSecretCard } from "./secret-card.js";

// A card already mounted for the same alias must notice a save that completes
// in this document (the next request of the same login can mount before the
// previous one has finished saving).
const request = { id: "first", kind: "command", command: "dummy", actionFingerprint: "same", fields: [{ index: 0, kind: "password", label: "Test", alias: "prueba-faceid" }] };

test("a mounted card refreshes its saved status when a save completes", async () => {
  let saved = false;
  const bridge = {
    status: async () => ({ saved, sameAction: saved }),
    save: async () => { saved = true; },
  };
  const second = createSecretCard({ request: { ...request, id: "second" }, bridge, send: async () => {} });
  const first = createSecretCard({ request, bridge, send: async () => { first.dispose(); await second.init(); } });
  first.setValue(0, "dummy-not-a-secret");
  first.toggleSave();
  await first.submit();
  expect(saved).toBe(true);
  expect([...second.derive().savedRows]).toEqual([0]);
  second.dispose();
});

test("a disposed card stops listening", async () => {
  let calls = 0;
  let saved = false;
  const bridge = { status: async () => { calls += 1; return { saved, sameAction: saved }; }, save: async () => { saved = true; } };
  const other = createSecretCard({ request: { ...request, id: "other" }, bridge, send: async () => {} });
  await other.init();
  other.dispose();
  const before = calls;
  const first = createSecretCard({ request, bridge, send: async () => {} });
  first.setValue(0, "x");
  first.toggleSave();
  await first.submit();
  expect(calls).toBe(before);
});

test("a refresh never replaces what the owner already typed", async () => {
  let saved = false;
  const bridge = { status: async () => ({ saved, sameAction: saved }), save: async () => { saved = true; }, use: async () => ({ value: "SAVED-DUMMY" }) };
  const posted = [];
  const second = createSecretCard({ request: { ...request, id: "second" }, bridge, send: async (_id, body) => { posted.push(body); } });
  await second.init();
  second.setValue(0, "TYPED-DUMMY");
  const first = createSecretCard({ request, bridge, send: async () => {} });
  first.setValue(0, "x");
  first.toggleSave();
  await first.submit();
  expect([...second.derive().savedRows]).toEqual([]);
  await second.submit();
  expect(JSON.stringify(posted[0])).toContain("TYPED-DUMMY");
  second.dispose();
});
