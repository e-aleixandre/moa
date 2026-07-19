// pulse-pairing-panel.js — open/close controller for the Pulse pairing panel
// (5N). Mirrors palette.js, but the pairing panel's open state is GLOBAL (not
// per-session) and doesn't belong in the session store, so it stands up its own
// tiny pub/sub. The panel mounts ONCE in app.jsx (next to GlobalPalette); the
// CommandPalette's "Pair Pulse…" action calls openPulsePairing().

let open = false;
const listeners = new Set();

export function isPulsePairingOpen() { return open; }

export function subscribePulsePairing(fn) {
  listeners.add(fn);
  fn(open);
  return () => listeners.delete(fn);
}

function emit() {
  listeners.forEach((fn) => fn(open));
}

export function openPulsePairing() {
  if (open) return;
  open = true;
  emit();
}

export function closePulsePairing() {
  if (!open) return;
  open = false;
  emit();
}
