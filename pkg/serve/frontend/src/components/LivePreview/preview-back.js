// preview-back — what the toolbar's Back control is allowed to say, and when.
//
// The only thing that can enable it is a report from the CURRENT document of
// the preview iframe, at the CURRENT handshake epoch, saying that its own
// `window.navigation` has a previous entry. Nothing else: not a URL, not a
// history length (which is joint across the shell and the frame and therefore
// says nothing about the frame), not the mere existence of a bridge.
//
// Keeping it here, as a pure reducer, is what makes the raced cases testable:
// a packet minted by the document that is being replaced, a packet from a
// window that is not the frame, a second click while a command is in flight.

// The six things the control can be; only AVAILABLE permits native traversal.
export const LOADING = "loading";
export const UNSUPPORTED = "unsupported";
export const FIRST = "first";
export const AVAILABLE = "available";
export const BUSY = "busy";
export const BLOCKED = "blocked";

export const INITIAL_BACK = { status: LOADING, epoch: 0, nativeBackBlocked: false };

// newEpoch — a new document (or a new frame) is a new frame of reference: the
// control goes back to disabled and every packet minted before it is stale.
export function newEpoch(state) {
  return { status: LOADING, epoch: state.epoch + 1, nativeBackBlocked: state.nativeBackBlocked === true };
}

// reset — no frame at all: closed panel, target change, keyed reload.
export function resetBack(state) {
  return { status: LOADING, epoch: state.epoch + 1, nativeBackBlocked: state.nativeBackBlocked === true };
}

// shouldAutoReturn — only the SecurityError answering an actual current Back
// click may make the parent reload. Reports are otherwise observational.
export function shouldAutoReturn(state, data) {
  return data?.navigationEpoch === state.epoch && state.status === BUSY && data.backError === "SecurityError";
}

// report — a `moa-preview-navigation` packet the shell has already
// authenticated (origin and source checked by the caller).
export function applyReport(state, data) {
  if (!data || data.navigationEpoch !== state.epoch) return state;
  // WebKit can reject a native child traversal while leaving canGoBack true.
  // That capability is not actionable in this document, so never let the
  // following fresh-status report turn the same failed action back on.
  if (data.backError === "SecurityError") {
    if (state.status !== BUSY) return state;
    return { ...state, status: BLOCKED, nativeBackBlocked: true };
  }
  if (state.status === BLOCKED) return state;
  if (data.supported !== true) return { ...state, status: UNSUPPORTED };
  return { ...state, status: data.canGoBack === true ? AVAILABLE : FIRST };
}

export function canGoBack(state) {
  return state.status === AVAILABLE;
}

export function needsReturnToApp(state) {
  return state.status === BLOCKED || state.status === UNSUPPORTED;
}

// press — the click. It answers with the command to post, or null when there is
// nothing to send: that is the busy guard (a second press while one is in
// flight) and the disabled guard in one place.
export function press(state) {
  if (state.status !== AVAILABLE) return { state, command: null };
  return {
    state: { ...state, status: BUSY },
    command: state.nativeBackBlocked
      ? { type: "moa-preview-return", navigationEpoch: state.epoch }
      : { type: "moa-preview-back", navigationEpoch: state.epoch },
  };
}

const TITLES = {
  [LOADING]: "Back in preview — waiting for the app",
  [UNSUPPORTED]: "Back in preview — not available in this browser",
  [FIRST]: "Back in preview — no earlier page",
  [AVAILABLE]: "Back in preview",
  [BUSY]: "Back in preview",
  [BLOCKED]: "Back in preview",
};

export function backTitle(state) {
  return TITLES[state.status] || TITLES[LOADING];
}
