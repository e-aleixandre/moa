import { interceptSecretCommand } from "./secrets.js";

export const DRAFT_PREFIX = "moa-next-draft-";

export const MENTIONS_PREFIX = "moa-next-draft-mentions:";

export function loadDraft(id, storage = globalThis.localStorage) {
  if (!id) return "";
  try { return storage.getItem(DRAFT_PREFIX + id) || ""; } catch (_) { return ""; }
}

// A /secret command is recognized before its first input value reaches the
// durable draft. This applies to every composer writer (including history and
// queue recall), not only native input events.
export function saveDraft(id, text, storage = globalThis.localStorage) {
  if (!id) return;
  try {
    if (!text || interceptSecretCommand(String(text).trim()) !== null) {
      storage.removeItem(DRAFT_PREFIX + id);
      storage.removeItem(MENTIONS_PREFIX + id);
      return;
    }
    storage.setItem(DRAFT_PREFIX + id, text);
  } catch (_) { /* localStorage can be unavailable or full */ }
}

// The draft's @mentions (owners/sessions picked from the menu) live beside its
// text: the text alone cannot say WHICH session an "@Fix login" meant.
export function loadDraftMentions(id, storage = globalThis.localStorage) {
  if (!id) return [];
  try {
    const list = JSON.parse(storage.getItem(MENTIONS_PREFIX + id) || "[]");
    return Array.isArray(list)
      ? list.filter((m) => m && typeof m.label === "string" && typeof m.id === "string"
        && (m.type === "owner" || m.type === "session"))
      : [];
  } catch (_) { return []; }
}

export function saveDraftMentions(id, mentions, storage = globalThis.localStorage) {
  if (!id) return;
  try {
    if (!mentions || mentions.length === 0) storage.removeItem(MENTIONS_PREFIX + id);
    else storage.setItem(MENTIONS_PREFIX + id, JSON.stringify(mentions));
  } catch (_) { /* localStorage can be unavailable or full */ }
}
