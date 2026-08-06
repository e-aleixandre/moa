import { interceptSecretCommand } from "./secrets.js";

export const DRAFT_PREFIX = "moa-next-draft-";

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
      return;
    }
    storage.setItem(DRAFT_PREFIX + id, text);
  } catch (_) { /* localStorage can be unavailable or full */ }
}
