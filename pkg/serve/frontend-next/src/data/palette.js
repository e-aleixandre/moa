// palette.js — command-palette open/close controller (5H).
//
// The palette's open state lives in the STORE (see store.js: paletteOpen /
// paletteStep) rather than in a second pub/sub system, so the global mount in
// app.jsx and the per-screen Spine buttons (onSearch / onNewSession) all read
// and write the same source of truth. These are thin helpers over setState so
// callers don't poke the raw field names.

import { store, setState } from './store.js';

// openPalette opens the palette on a given step ('search' by default, 'create'
// when the caller wants the New-session flow directly — e.g. onNewSession or a
// ⌘N chord).
export function openPalette(step = 'search') {
  setState({ paletteOpen: true, paletteStep: step });
}

export function closePalette() {
  setState({ paletteOpen: false });
}

// togglePalette flips the palette open/closed (the ⌘K chord). When opening it
// always lands on the given step so the chord is a plain "search" entry point.
export function togglePalette(step = 'search') {
  if (store.get().paletteOpen) setState({ paletteOpen: false });
  else setState({ paletteOpen: true, paletteStep: step });
}
