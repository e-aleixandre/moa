// scenes.js — the fidelity matrix. Plain data, no imports: the browser bundle
// and scripts/fidelity.mjs both read this file, so it is the single list of
// what gets captured. Adding a scene is adding a line here.
//
// A scene is ONE thing, at a fixed size, with no lab chrome around it:
//   host     which component the scene mounts (see scene.jsx HOSTS)
//   live     id of a LIVE_STATES preset (zones-lab.jsx:926)
//   surface  which surface is open (SURFACES, zones-lab.jsx:1982)
//   view     list ordering, for the sidebar host
//   width    viewport width. Kept clear of the 1400px breakpoint that shrinks
//            .zl-desk to 720px (zones-lab.css:1349), so a desktop scene is
//            always the canonical 860.
//   height   viewport height. Only has to be large enough; the capture is
//            clipped to `target`, not to the viewport.
//   target   CSS selector of what is actually captured. Default: the device
//            frame of the host.
//   piece    the migration unit this scene belongs to (--piece filters on it).
//
// The matrix is deliberately not the full product of density × live × surface
// (126 cells). It is the cells that say something different: every live state
// once per density, every surface once, and the two studies. More cells are a
// line each.

export const SCENES = [
  // ── Phone shell, 390×780 (zones-lab.css:137) ─────────────────────────────
  { name: "phone-idle", host: "phone", live: "idle", surface: "none", piece: "shell-phone" },
  { name: "phone-working", host: "phone", live: "working", surface: "none", piece: "shell-phone" },
  { name: "phone-waiting", host: "phone", live: "waiting", surface: "none", piece: "shell-phone" },
  { name: "phone-background", host: "phone", live: "background", surface: "none", piece: "live-zone" },
  { name: "phone-all", host: "phone", live: "all", surface: "none", piece: "live-zone" },
  { name: "phone-live-open", host: "phone", live: "open", surface: "none", piece: "live-zone" },
  { name: "phone-panel", host: "phone", live: "working", surface: "panel", piece: "session-panel" },
  { name: "phone-usage", host: "phone", live: "working", surface: "usage", piece: "session-panel" },
  { name: "phone-mcp", host: "phone", live: "working", surface: "mcp", piece: "session-panel" },
  { name: "phone-artifacts", host: "phone", live: "working", surface: "artifacts", piece: "session-panel" },
  { name: "phone-model-sheet", host: "phone", live: "working", surface: "model", piece: "pickers" },
  { name: "phone-perm-sheet", host: "phone", live: "working", surface: "perm", piece: "pickers" },
  { name: "phone-settings", host: "phone", live: "working", surface: "settings", piece: "global-settings" },
  { name: "phone-settings-page", host: "phone", live: "working", surface: "settings-page", piece: "global-settings" },

  // ── Desktop shell, 860×780 (zones-lab.css:144) ───────────────────────────
  { name: "desktop-idle", host: "desktop", live: "idle", surface: "none", width: 1500, piece: "shell-desktop" },
  { name: "desktop-working", host: "desktop", live: "working", surface: "none", width: 1500, piece: "shell-desktop" },
  { name: "desktop-waiting", host: "desktop", live: "waiting", surface: "none", width: 1500, piece: "shell-desktop" },
  { name: "desktop-all", host: "desktop", live: "all", surface: "none", width: 1500, piece: "live-zone" },
  { name: "desktop-panel", host: "desktop", live: "working", surface: "panel", width: 1500, piece: "session-panel" },
  { name: "desktop-usage", host: "desktop", live: "working", surface: "usage", width: 1500, piece: "session-panel" },
  { name: "desktop-mcp", host: "desktop", live: "working", surface: "mcp", width: 1500, piece: "session-panel" },
  { name: "desktop-artifacts", host: "desktop", live: "working", surface: "artifacts", width: 1500, piece: "session-panel" },
  { name: "desktop-model-popover", host: "desktop", live: "working", surface: "model", width: 1500, piece: "pickers" },
  { name: "desktop-perm-popover", host: "desktop", live: "working", surface: "perm", width: 1500, piece: "pickers" },
  { name: "desktop-settings", host: "desktop", live: "working", surface: "settings", width: 1500, piece: "global-settings" },
  { name: "desktop-settings-page", host: "desktop", live: "working", surface: "settings-page", width: 1500, piece: "global-settings" },

  // ── Grid, 1298×820 (zones-lab.css:1210) ──────────────────────────────────
  { name: "grid-working", host: "grid", live: "working", surface: "none", width: 1500, height: 1000, piece: "shell-grid" },
  { name: "grid-all", host: "grid", live: "all", surface: "none", width: 1500, height: 1000, piece: "shell-grid" },

  // ── The list on its own. It is migration step 1 (METODO §4), and on the
  //    phone it only exists behind a swipe, so it gets its own host rather
  //    than being read out of the desktop column. ────────────────────────────
  { name: "sidebar-recent", host: "sidebar", view: "recent", piece: "session-list" },
  { name: "sidebar-project", host: "sidebar", view: "project", piece: "session-list" },
  { name: "sidebar-desktop", host: "sidebar", view: "recent", desktop: true, piece: "session-list" },

  // ── The two width studies. Same components, every width they meet. ───────
  {
    name: "study-statusline", host: "statusline", width: 1500, height: 1000,
    target: ".zl-study", piece: "status-line",
  },
  {
    name: "study-livezone", host: "livezone", width: 1500, height: 1000,
    target: ".zl-study", piece: "live-zone",
  },
];

// Default capture target: the device frame the host draws. Everything the
// prototype puts around it (density label, hint prose) is lab chrome and is
// hidden in scene.css, but clipping to the frame also keeps the diff honest —
// a percentage measured over acres of empty canvas would hide a real shift.
export const DEFAULT_TARGET = ".fx-scene :is(.zl-phone, .zl-desk, .fx-frame)";

export const DEFAULT_WIDTH = 520;
export const DEFAULT_HEIGHT = 900;

export function sceneByName(name) {
  return SCENES.find((s) => s.name === name) || null;
}

export function sceneViewport(scene) {
  return {
    width: scene.width || DEFAULT_WIDTH,
    height: scene.height || DEFAULT_HEIGHT,
  };
}

export function sceneTarget(scene) {
  return scene.target || DEFAULT_TARGET;
}
