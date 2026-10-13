// secret-card.test.js — run with `bun test`
import { expect, test } from "bun:test";
import { createSecretCard } from "./secret-card.js";

const request = {
  id: "sec_1",
  kind: "command",
  command: "sudo true",
  actionFingerprint: "fp",
  fields: [{ index: 0, label: "Password", kind: "password", alias: "sudo" }],
};

test("a card disposed while Face ID is pending never POSTs the value it then receives", async () => {
  const sent = [];
  let release;
  const bridge = {
    status: async () => ({ saved: true, sameAction: true, savedAt: 1 }),
    use: () => new Promise((resolve) => { release = () => resolve({ value: "hunter2" }); }),
    touch: async () => ({}),
    save: async () => ({}),
  };
  const card = createSecretCard({ request, send: async (id, body) => { sent.push([id, body]); }, bridge });
  await card.init();
  const pending = card.submit();
  card.dispose();
  release();
  await pending;
  expect(sent).toEqual([]);
  expect(JSON.stringify(card.state)).not.toContain("hunter2");
});

test("dispose during the first Face ID starts no further native use() for remaining fields", async () => {
  const two = {
    ...request,
    fields: [
      { index: 0, label: "Password", kind: "password", alias: "pw" },
      { index: 1, label: "Second", kind: "password", alias: "second" },
    ],
  };
  const calls = [];
  const sent = [];
  let release;
  const bridge = {
    status: async () => ({ saved: true, sameAction: true, savedAt: 1 }),
    use: ({ alias }) => { calls.push(alias); return new Promise((resolve) => { release = () => resolve({ value: "hunter2" }); }); },
    touch: async () => ({}),
    save: async () => ({}),
  };
  const card = createSecretCard({ request: two, send: async (id, body) => { sent.push([id, body]); }, bridge });
  await card.init();
  const pending = card.submit();
  card.dispose();
  release();
  await pending;
  expect(calls).toEqual(["pw"]);
  expect(sent).toEqual([]);
});
