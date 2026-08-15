// Composition state is deliberately independent from the DOM. A submitted
// composition can have one late browser input event; only that event is
// discarded, while the next composition is a new epoch.
export function newCompositionState() {
  return { epoch: 0, composing: false, submittedEpoch: -1 };
}

export function compositionStarted(state) {
  return { ...state, epoch: state.epoch + 1, composing: true };
}

export function compositionEnded(state) {
  return { ...state, composing: false };
}

export function compositionSubmitted(state) {
  return { ...state, submittedEpoch: state.epoch };
}

export function shouldDiscardLateCompositionInput(state, event) {
  return state.submittedEpoch === state.epoch && !!(
    event?.inputType === 'insertCompositionText' || event?.isComposing
  );
}

export function compositionInputDiscarded(state) {
  return { ...state, submittedEpoch: -1 };
}

// A late composition input is delivered after the browser has already changed
// the textarea. Restore the last known value only when the event is provably a
// single insertion into it. In particular, never clear a value that has since
// been typed for the next message: retaining an unexpected stale glyph is far
// less harmful than deleting user input we cannot attribute to that glyph.
export function valueBeforeLateCompositionInput(previousValue, currentValue, data) {
  if (typeof previousValue !== "string" || typeof currentValue !== "string" || !data) return null;
  const inserted = String(data);
  const index = currentValue.indexOf(inserted);
  if (index < 0) return null;
  const restored = currentValue.slice(0, index) + currentValue.slice(index + inserted.length);
  return restored === previousValue ? previousValue : null;
}
