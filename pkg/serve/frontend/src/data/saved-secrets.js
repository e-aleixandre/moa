// saved-secrets.js — the card's side of the native `window.moaSavedSecrets`
// bridge (contract: iOS app only; undefined on the web). Everything here is
// best effort around the card: a bridge that fails never blocks typing the
// value by hand.

export function savedSecretsBridge(win = typeof window !== "undefined" ? window : null) {
  const bridge = win?.moaSavedSecrets;
  return bridge && typeof bridge.status === "function" ? bridge : null;
}

// The summary the app stores beside a saved value so the owner can recognise
// it later in Settings: the command or the origin, never a value.
const CONTROL = /[\u0000-\u001f\u007f]/g;
const clean = (text, max) => String(text || "").replace(CONTROL, " ").replace(/\s+/g, " ").trim().slice(0, max);

export function actionSummary(request) {
  return clean(request.kind === "browser" ? request.origin : request.command, 500);
}

// loadStatuses asks the app about each field that has an alias. A field whose
// status call fails is treated as "not saved" so typing still works, but the
// failure is logged so it does not look like a missing save forever.
export async function loadStatuses(bridge, request) {
  const entries = await Promise.all(request.fields.filter((f) => f.alias).map(async (f) => {
    try {
      const s = await bridge.status({ alias: f.alias, actionFingerprint: request.actionFingerprint });
      return [f.index, { saved: !!s?.saved, sameAction: !!s?.sameAction, savedAt: s?.savedAt || 0 }];
    } catch (e) {
      console.error("Saved password status failed:", e?.code || "error");
      return [f.index, { saved: false, sameAction: false, savedAt: 0 }];
    }
  }));
  return Object.fromEntries(entries);
}

// Cards that are already mounted hear about a completed save in this document
// (listeners may return a promise, awaited so callers see the refreshed state).
const savedListeners = new Set();
export function onSavedChange(listener) {
  savedListeners.add(listener);
  return () => savedListeners.delete(listener);
}

// readSaved reads one saved value, which is where Face ID happens. One field at
// a time so each prompt names what it is for. Rejects with the bridge's error.
export async function readSaved(bridge, field, reason) {
  const result = await bridge.use({ alias: field.alias, reason });
  const value = result?.value;
  if (typeof value !== "string" || !value) throw new Error("empty saved value");
  return value;
}

// recordUse runs after a successful POST: touch what was reused, save what the
// owner asked to keep. The action already happened, so nothing throws; it
// returns what happened to each save so the owner can be told. Touch failures
// are only logged (never a value).
export async function recordUse(bridge, request, { touched = [], saves = [] }) {
  for (const f of touched) {
    try {
      await bridge.touch({ alias: f.alias, actionFingerprint: request.actionFingerprint });
    } catch (e) {
      console.error("Saved password touch failed:", e?.code || "error");
    }
  }
  const outcomes = [];
  for (const { field, value } of saves) {
    const label = clean(field.label || field.alias, 80) || "Password";
    try {
      await bridge.save({
        alias: field.alias,
        actionFingerprint: request.actionFingerprint,
        label,
        actionSummary: actionSummary(request),
        value,
      });
      outcomes.push({ alias: field.alias, label, ok: true });
    } catch (e) {
      console.error("Saved password save failed:", e?.code || "error");
      outcomes.push({ alias: field.alias, label, ok: false, error: clean(e?.message || e?.code, 120) || "Try again." });
    }
  }
  if (outcomes.some((o) => o.ok)) await Promise.all([...savedListeners].map((fn) => fn()));
  return { saves: outcomes };
}

// saveNotice is the toast for the outcomes of one answer, or null when nothing
// was asked to be saved.
export function saveNotice({ saves = [] } = {}) {
  const failed = saves.find((o) => !o.ok);
  if (failed) return { type: "error", title: "Couldn't save on this iPhone", detail: failed.error };
  if (saves.length === 0) return null;
  return { type: "success", title: "Saved on this iPhone", detail: saves.map((o) => o.label).join(", ") };
}

export function faceIdReason(request) {
  const what = request.kind === "browser" ? `Sign in on ${request.origin}` : `Run ${request.command}`;
  return clean(what, 120);
}

export function bridgeErrorText(error) {
  if (error?.code === "cancelled") return "Face ID was cancelled.";
  if (error?.code === "not_found") return "The saved password is gone. Type it instead.";
  return "Could not read the saved password. Type it instead.";
}

// ── Settings ────────────────────────────────────────────────────────────────

export async function listSaved(bridge) {
  const list = await bridge.list();
  return Array.isArray(list) ? list : [];
}
