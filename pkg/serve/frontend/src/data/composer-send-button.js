// composer-send-button.js — exactly-once routing for Composer's Send action.
//
// Voice gestures own the button only for an empty composer. Content uses the
// browser click as its normal activation, with an iOS pointercancel fallback.
// The fallback must not turn a cancelled touch plus a trailing click into two
// sends, especially while an attachment is still in the handler closure.

export const SEND_BUTTON_INITIAL = Object.freeze({
  activePointerId: null,
  terminalActivation: null,
  sendInFlight: false,
});

export const sendButtonEvent = {
  pointerDown: (pointerId) => ({ type: "POINTER_DOWN", pointerId }),
  pointerUp: (pointerId) => ({ type: "POINTER_UP", pointerId }),
  pointerCancel: (pointerId) => ({ type: "POINTER_CANCEL", pointerId }),
  click: () => ({ type: "CLICK" }),
  keyActivate: () => ({ type: "KEY_ACTIVATE" }),
  sendFinished: () => ({ type: "SEND_FINISHED" }),
};

export function usesVoiceSendButton({ canVoice, hasText, attachmentCount }) {
  return canVoice && !hasText && attachmentCount === 0;
}

function send(state, terminalActivation) {
  if (state.sendInFlight || state.terminalActivation) {
    return { state, actions: [] };
  }
  return {
    state: { activePointerId: null, terminalActivation, sendInFlight: true },
    actions: [{ type: "send" }],
  };
}

// reduceContentSendActivation models one browser activation at a time.
//
// A pointer is identified at POINTER_DOWN and only that pointer may use the
// pointercancel fallback. Its first terminal event latches the activation and
// marks the send in flight before the caller invokes its async send handler.
// A later click is therefore a no-op. New pointer/key activations are explicit
// identities; they cannot bypass an in-flight send.
export function reduceContentSendActivation(state = SEND_BUTTON_INITIAL, event) {
  switch (event.type) {
    case "POINTER_DOWN":
      if (state.sendInFlight || state.activePointerId != null) {
        return { state, actions: [] };
      }
      // This is a new physical activation, so a completed activation's latch
      // cannot belong to it.
      return {
        state: { activePointerId: event.pointerId, terminalActivation: null, sendInFlight: false },
        actions: [],
      };

    case "POINTER_UP":
      // No-op: keep the matching identity until the browser delivers its
      // normal click. This also prevents a second concurrent pointer from
      // claiming the send.
      return { state, actions: [] };

    case "POINTER_CANCEL":
      if (state.activePointerId !== event.pointerId) {
        return { state, actions: [] };
      }
      return send(state, { kind: "pointer", pointerId: event.pointerId });

    case "KEY_ACTIVATE":
      if (state.sendInFlight) return { state, actions: [] };
      // Keyboard has its own physical activation identity. It deliberately
      // replaces an old completed latch; the native click it produces is then
      // swallowed by that new latch.
      return send({ ...state, activePointerId: null, terminalActivation: null }, { kind: "keyboard" });

    case "CLICK":
      if (state.terminalActivation) {
        // Consume the synthetic click associated with a pointercancel or a
        // keyboard activation. Keeping sendInFlight intact still blocks every
        // subsequent event until the handler has finished.
        return { state: { ...state, terminalActivation: null }, actions: [] };
      }
      return send(state, { kind: "click", pointerId: state.activePointerId });

    case "SEND_FINISHED":
      if (!state.sendInFlight) return { state, actions: [] };
      return { state: { ...state, sendInFlight: false }, actions: [] };

    default:
      return { state, actions: [] };
  }
}
