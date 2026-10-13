// secret-card.js — the secret card's behaviour, without a view: what it holds,
// what pressing each button does, and in which order values move. The card
// component is a thin shell over this, so the rules are testable with a fake
// bridge and a fake `send`, and the values live in exactly one place.
//
// Values: `state.values` (what was typed or filled from the keychain) and the
// local `merged` copy built while sending. Both are dropped the moment the
// request is handed to `send`, whatever the server answers.

import {
  DENY_BODY, buildAllowBody, firstEmptyField, secretErrorText, valuesComplete,
} from "./secret-action.js";
import {
  bridgeErrorText, faceIdReason, loadStatuses, readSaved, recordUse,
} from "./saved-secrets.js";

// autofocusAllowed — focus the first field on desktop only: on a touch screen
// it would pop the keyboard before the owner has read what is being asked.
export function autofocusAllowed(win = typeof window !== "undefined" ? window : null) {
  return !win?.matchMedia?.("(pointer: coarse)")?.matches;
}

export function createSecretCard({ request, send, bridge = null, onChange }) {
  const state = {
    values: {},
    revealed: {},
    statuses: {},
    typeAnother: {},
    fromSaved: {},
    saveOn: false,
    busy: false,
    error: "",
  };
  let alive = true;
  let inFlight = false;
  const emit = () => { if (alive) onChange?.(); };
  const patch = (changes) => { Object.assign(state, changes); emit(); };

  const savedRowIndexes = () => new Set(
    request.fields
      .filter((f) => state.statuses[f.index]?.saved && state.statuses[f.index]?.sameAction && !state.typeAnother[f.index])
      .map((f) => f.index),
  );

  const dropValues = () => { state.values = {}; state.revealed = {}; state.fromSaved = {}; };

  const card = {
    state,

    // What the view needs that follows from the state.
    derive() {
      const savedRows = savedRowIndexes();
      const complete = valuesComplete(request.fields, state.values, savedRows);
      return {
        savedRows,
        complete,
        ready: complete && !state.busy,
        target: firstEmptyField(request.fields, state.values, savedRows),
        saveable: request.fields.filter((f) => f.alias && !savedRows.has(f.index)),
        // Saved, but for another action, and not already filled from it.
        differs: (f) => !!state.statuses[f.index]?.saved && !state.statuses[f.index]?.sameAction && !state.fromSaved[f.index],
      };
    },

    async init() {
      if (!bridge) return;
      const statuses = await loadStatuses(bridge, request);
      if (alive) patch({ statuses });
    },

    dispose() {
      alive = false;
      state.values = {};
      state.revealed = {};
      state.fromSaved = {};
    },

    setValue(index, value) {
      state.values = { ...state.values, [index]: value };
      if (state.fromSaved[index]) state.fromSaved = { ...state.fromSaved, [index]: false };
      if (state.error) state.error = "";
      emit();
    },

    toggleReveal(index) {
      patch({ revealed: { ...state.revealed, [index]: !state.revealed[index] } });
    },

    toggleSave() { patch({ saveOn: !state.saveOn }); },

    typeInstead(index) { patch({ typeAnother: { ...state.typeAnother, [index]: true } }); },

    // The saved value was for another action: fill it in, never send it. The
    // owner reads the card again and presses Run.
    async fillSaved(field) {
      if (state.busy || !bridge) return;
      patch({ error: "" });
      try {
        const value = await readSaved(bridge, field, faceIdReason(request));
        if (!alive) return;
        patch({
          values: { ...state.values, [field.index]: value },
          fromSaved: { ...state.fromSaved, [field.index]: true },
        });
      } catch (e) {
        patch({ error: bridgeErrorText(e) });
      }
    },

    async submit() {
      if (inFlight || !card.derive().complete) return;
      inFlight = true;
      patch({ busy: true, error: "" });
      const savedRows = savedRowIndexes();
      const merged = { ...state.values };
      const used = [];
      // Saved values first: this is where Face ID happens, and a refusal must
      // leave the card as it was (typed values kept, the input shown).
      for (const field of request.fields.filter((f) => savedRows.has(f.index))) {
        if (!alive) break;

        try {
          merged[field.index] = await readSaved(bridge, field, faceIdReason(request));
          used.push(field);
        } catch (e) {
          inFlight = false;
          patch({
            typeAnother: { ...state.typeAnother, [field.index]: true },
            error: bridgeErrorText(e),
            busy: false,
          });
          return;
        }
      }
      // Resolved or unmounted while Face ID was up: the answer belongs to
      // nobody now, so nothing is sent and what was read is dropped.
      if (!alive) {
        for (const key of Object.keys(merged)) delete merged[key];
        inFlight = false;
        return;
      }
      const reused = request.fields.filter((f) => state.fromSaved[f.index] && !savedRows.has(f.index));
      const saves = state.saveOn && bridge
        ? request.fields
          .filter((f) => f.alias && !savedRows.has(f.index) && !state.fromSaved[f.index])
          .map((field) => ({ field, value: merged[field.index] }))
        : [];
      let body;
      try {
        body = buildAllowBody(request.fields, merged);
      } catch {
        inFlight = false;
        patch({ busy: false });
        return;
      }
      // Handed over: nothing typed stays in the card, whatever the answer is.
      dropValues();
      emit();
      try {
        await send(request.id, body);
      } catch (e) {
        console.error("Secret request failed:", e?.status || "error");
        inFlight = false;
        patch({ error: secretErrorText(e), busy: false });
        return;
      }
      if (bridge) await recordUse(bridge, request, { touched: [...used, ...reused], saves });
    },

    async deny() {
      if (inFlight) return;
      inFlight = true;
      dropValues();
      patch({ busy: true, error: "" });
      try {
        await send(request.id, DENY_BODY);
      } catch (e) {
        console.error("Secret deny failed:", e?.status || "error");
        inFlight = false;
        patch({ error: secretErrorText(e), busy: false });
      }
    },
  };
  return card;
}
