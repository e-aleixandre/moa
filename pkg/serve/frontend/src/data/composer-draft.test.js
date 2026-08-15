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
