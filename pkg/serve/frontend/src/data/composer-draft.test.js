import { test, expect } from "bun:test";
import { DRAFT_PREFIX, loadDraft, saveDraft } from "./composer-draft.js";

function memoryStorage() {
  const values = new Map();
  return {
    getItem: (key) => values.get(key) || null,
    setItem: (key, value) => values.set(key, value),
    removeItem: (key) => values.delete(key),
  };
}

test("typing a /secret command clears an existing draft instead of storing trailing text", () => {
  const storage = memoryStorage();
  const sessionId = "session-1";
  storage.setItem(DRAFT_PREFIX + sessionId, "ordinary previous draft");

  saveDraft(sessionId, "/secret token\nhunter2", storage);

  expect(storage.getItem(DRAFT_PREFIX + sessionId)).toBeNull();
  expect(loadDraft(sessionId, storage)).toBe("");
});

test("pasting a CRLF /secret command clears an existing draft", () => {
  const storage = memoryStorage();
  const sessionId = "session-2";
  storage.setItem(DRAFT_PREFIX + sessionId, "ordinary previous draft");

  saveDraft(sessionId, "/secret token\r\nhunter2", storage);

  expect(storage.getItem(DRAFT_PREFIX + sessionId)).toBeNull();
});

test("ordinary input remains a persisted draft", () => {
  const storage = memoryStorage();

  saveDraft("session-3", "ordinary text\nwith a trailing line", storage);

  expect(loadDraft("session-3", storage)).toBe("ordinary text\nwith a trailing line");
});

test("draft mentions persist beside the draft and go with it", async () => {
  const { loadDraftMentions, saveDraftMentions, MENTIONS_PREFIX } = await import("./composer-draft.js");
  const storage = memoryStorage();
  const mention = { label: "Infra", type: "owner", id: "o1", meta: { root: "/x" } };
  saveDraftMentions("s1", [mention], storage);
  expect(loadDraftMentions("s1", storage)).toEqual([mention]);
  saveDraft("s1", "ask @Infra", storage);
  expect(loadDraftMentions("s1", storage)).toEqual([mention]);
  saveDraft("s1", "", storage);
  expect(storage.getItem(MENTIONS_PREFIX + "s1")).toBeNull();
  saveDraftMentions("s1", [mention], storage);
  saveDraftMentions("s1", [], storage);
  expect(loadDraftMentions("s1", storage)).toEqual([]);
});

test("a corrupt or foreign mentions entry loads as none", async () => {
  const { loadDraftMentions, MENTIONS_PREFIX } = await import("./composer-draft.js");
  const storage = memoryStorage();
  storage.setItem(MENTIONS_PREFIX + "s1", "{nope");
  expect(loadDraftMentions("s1", storage)).toEqual([]);
  storage.setItem(MENTIONS_PREFIX + "s1", JSON.stringify([{ label: "a" }, { label: "b", id: "1", type: "file" }, { label: "ok", id: "2", type: "session" }]));
  expect(loadDraftMentions("s1", storage)).toEqual([{ label: "ok", id: "2", type: "session" }]);
});
