// ambient.js — the temporary switch for the Ambient visual direction.
//
// THIS FILE IS MEANT TO BE DELETED. It exists so the new look can be lived
// with on a real phone for a few days without changing the app for anyone
// else, and it goes away when the decision is made: either Ambient becomes
// the design (and this switch is pointless), or Ambient is dropped (and this
// switch goes with it). It is deliberately NOT a setting in the UI: a setting
// would promise two supported themes forever, which is not the promise here.
//
// Enable:  ?ambient=1   Disable:  ?ambient=0
// The choice is remembered, so the phone only needs the URL once.
//
// It sets data-ambient on <html>, which the ambient stylesheet hangs off. No
// component knows about it: nothing branches on this value, so removing the
// switch cannot break a component.

const KEY = "moa-ambient";
// Kept in sync by hand with --base (tokens.css) and --amb-canvas
// (tokens/ambient.css): a meta tag cannot read a custom property.
const DEFAULT_CANVAS = "#1e1e2e";
const AMBIENT_CANVAS = "#12121f";

export function initAmbient() {
  let on = false;
  try {
    const param = new URLSearchParams(location.search).get("ambient");
    if (param === "1" || param === "0") {
      on = param === "1";
      localStorage.setItem(KEY, on ? "1" : "0");
      // Drop the parameter so it does not ride along in shared links or in the
      // URLs the router writes: the preference is stored now.
      const url = new URL(location.href);
      url.searchParams.delete("ambient");
      history.replaceState(history.state, "", url.pathname + url.search + url.hash);
    } else {
      on = localStorage.getItem(KEY) === "1";
    }
  } catch (_) {
    // Private mode, blocked storage: fall back to off rather than break boot.
  }
  if (on) document.documentElement.dataset.ambient = "on";
  else delete document.documentElement.dataset.ambient;

  // theme-color paints the iOS status bar and the Home Screen splash in an
  // installed PWA -- it is the one surface CSS cannot reach. Left at the app's
  // --base it showed as a lighter band above an ambient canvas, which is the
  // same seam this meta exists to avoid (see the note in index.html).
  const meta = document.querySelector('meta[name="theme-color"]');
  if (meta) meta.setAttribute("content", on ? AMBIENT_CANVAS : DEFAULT_CANVAS);

  return on;
}

// ambientOn — is the switch on right now? initAmbient sets data-ambient before
// the first render and nothing ever flips it at runtime (the URL parameter is
// read once, on boot), so this is a stable read.
//
// It exists because the migration reached something CSS alone cannot gate: the
// session panel is a new SURFACE, not a restyle. With the switch off the app
// must behave exactly as it does today — no extra door on the crumb, no drawer
// mounted — so the components that host it ask here before rendering it. When
// Ambient stops being a switch this call goes away with the module.
export function ambientOn() {
  if (typeof document === "undefined") return false;
  return document.documentElement.dataset.ambient === "on";
}
