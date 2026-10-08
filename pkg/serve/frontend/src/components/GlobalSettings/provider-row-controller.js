// provider-row-controller.js — one Providers row's sign-in / API-key flow,
// with the row's UI state held by the caller (component-local useState).
//
// Kept out of the component so the rules that matter can be run without a DOM:
//   - the typed or pasted value lives only in `draft`, and `draft` is emptied
//     the moment it is sent (whatever the answer), on cancel and on dispose;
//   - a request is sent once: a timeout is reported and the status re-read,
//     never re-sent;
//   - success says so and touches nothing else — no session, no turn.

import {
  beginSignIn, cancelSignIn, closeWindow, completeSignIn, openBlankWindow, removeApiKey, retrySave, saveApiKey, signInProgress,
} from "./providers-flow.js";
import {
  SAVED_COPY, TERMINAL_PROGRESS, errorIntent, flowErrorCopy, progressEndCopy,
} from "./providers-model.js";

export const IDLE = Object.freeze({
  step: "idle", // idle | signin | paste | device | key
  draft: "",
  busy: false,
  attempt: null,
  opened: false,
  progress: "",
  message: null, // { text, tone: "ok" | "bad" | "muted" }
});

// createRowController — `get`/`set` read and replace the row's state; `row()`
// is the provider's current status row (for expected_generation); `onChanged`
// receives an updated owner row, or nothing when the list must be re-read.
//
// `scope` is the row's operation identity, shared by every controller built for
// the row (the component rebuilds the controller on each render): each action
// takes a new number, and Cancel, dispose or a newer action invalidates the
// older ones, so a late answer neither navigates a popup nor writes state.
export function newRowScope() {
  return { op: 0, begin: null, canceled: new Set() };
}

export function createRowController({
  api, provider, flow, row, get, set, onChanged, openWindow = openBlankWindow, scope = newRowScope(),
}) {
  const patch = (next) => set({ ...get(), ...next });
  const generation = () => row()?.credential_generation || "";
  const attemptId = () => get().attempt?.attempt_id || "";

  const begin = () => {
    const op = ++scope.op;
    return { op, live: () => scope.op === op };
  };

  const done = (updated) => {
    set({ ...IDLE, message: { text: SAVED_COPY, tone: "ok" } });
    onChanged?.(updated && typeof updated === "object" && updated.id ? updated : null);
  };

  const fail = (error, { keepStep = false } = {}) => {
    const intent = errorIntent(error);
    const message = { text: flowErrorCopy(error), tone: intent === "check_status" ? "muted" : "bad" };
    if (keepStep && intent === "paste_again") {
      patch({ busy: false, draft: "", message });
      return;
    }
    set({ ...IDLE, message });
    if (intent === "reload" || intent === "check_status") onChanged?.(null);
  };

  return {
    // Shows the instructions (and, for a replacement, what it changes) before
    // anything is opened or requested.
    startSignIn() {
      set({ ...IDLE, step: "signin" });
    },

    startApiKey() {
      set({ ...IDLE, step: "key" });
    },

    setDraft(value) {
      patch({ draft: String(value ?? "") });
    },

    // open — MUST run inside the click: the paste flows open a blank window
    // synchronously so a popup blocker lets it through.
    async open() {
      if (get().busy) return;
      const handle = flow === "device" ? null : openWindow();
      const { op, live } = begin();
      scope.begin = { op, handle };
      patch({ busy: true, message: null });
      try {
        const { attempt, opened, stale } = await beginSignIn(api, provider, generation(), handle, live);
        if (scope.begin?.op === op) scope.begin = null;
        if (stale) {
          // Cancelled while the request was out: end the attempt it started.
          // An unmounted row leaves it to its deadline instead.
          if (scope.canceled.delete(op)) cancelSignIn(api, provider, attempt.attempt_id);
          return;
        }
        if (attempt.flow === "device") {
          set({ ...IDLE, step: "device", attempt, progress: attempt.state || "waiting" });
        } else {
          set({ ...IDLE, step: "paste", attempt, opened });
        }
      } catch (error) {
        if (scope.begin?.op === op) scope.begin = null;
        scope.canceled.delete(op);
        if (live()) fail(error);
      }
    },

    async complete() {
      const state = get();
      const input = state.draft.trim();
      if (state.busy || !input || !state.attempt) return;
      // Emptied before the answer: what was pasted is not kept for a retry.
      patch({ busy: true, draft: "", message: null });
      const { live } = begin();
      try {
        const result = await completeSignIn(api, provider, state.attempt.attempt_id, input);
        if (live()) done(result?.provider);
        else onChanged?.(null);
      } catch (error) {
        if (live()) fail(error, { keepStep: true });
      }
    },

    async saveKey() {
      const state = get();
      const key = state.draft.trim();
      if (state.busy || !key) return;
      patch({ busy: true, draft: "", message: null });
      const { live } = begin();
      try {
        const updated = await saveApiKey(api, provider, key, generation());
        if (live()) done(updated);
        else onChanged?.(null);
      } catch (error) {
        if (!live()) return;
        // A key that does not look like one keeps the field open (empty) so
        // the next paste is one step; anything else ends the attempt.
        if (error?.detail?.action === "replace_key") {
          patch({ busy: false, draft: "", message: { text: flowErrorCopy(error), tone: "bad" } });
        } else {
          fail(error);
        }
      }
    },

    async removeKey() {
      if (get().busy) return;
      patch({ busy: true, message: null });
      const { live } = begin();
      try {
        const updated = await removeApiKey(api, provider, row()?.plan_api_key?.generation);
        if (live()) set({ ...IDLE, message: { text: "API key removed.", tone: "ok" } });
        onChanged?.(updated && updated.id ? updated : null);
      } catch (error) {
        if (live()) fail(error);
      }
    },

    async retrySave() {
      if (get().busy) return;
      patch({ busy: true, message: null });
      const { live } = begin();
      try {
        const updated = await retrySave(api, provider);
        if (live()) set({ ...IDLE, message: { text: "Saved.", tone: "ok" } });
        onChanged?.(updated && updated.id ? updated : null);
      } catch (error) {
        if (live()) fail(error);
      }
    },

    // poll — one progress read for a device attempt. The server polls the
    // provider on its own; this only reports where it is.
    async poll() {
      const id = attemptId();
      if (get().step !== "device" || !id) return;
      const op = scope.op;
      let progress;
      try {
        progress = await signInProgress(api, provider, id);
      } catch (error) {
        // Unknown or ended for this browser: the attempt is gone. A network
        // blip is not an answer; the next tick asks again.
        if (error?.status && scope.op === op) fail(error);
        return;
      }
      if (scope.op !== op || get().step !== "device" || attemptId() !== id) return;
      const state = progress?.state || "";
      if (state === "saved") {
        done(progress.provider_status);
      } else if (TERMINAL_PROGRESS.has(state)) {
        set({ ...IDLE, message: { text: progressEndCopy(progress), tone: "bad" } });
        onChanged?.(null);
      } else if (state) {
        patch({ progress: state });
      }
    },

    cancel() {
      const id = get().step === "paste" || get().step === "device" ? attemptId() : "";
      const pending = scope.begin;
      scope.op++;
      if (pending) {
        scope.begin = null;
        scope.canceled.add(pending.op);
        closeWindow(pending.handle);
      }
      set({ ...IDLE });
      if (id) cancelSignIn(api, provider, id);
    },

    // dispose — the row is leaving the screen. Whatever was typed goes with it;
    // the attempt itself is left to its deadline (closing Settings is not a
    // cancel, and a device sign-in may still be approved on the other screen).
    dispose() {
      scope.op++;
      if (get().draft) set({ ...get(), draft: "" });
    },
  };
}
