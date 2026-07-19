import { render } from "preact";
import { useState, useEffect } from "preact/hooks";
import "./index.css";
import { Catalog } from "./catalog/catalog.jsx";
import { LiveStatesGallery } from "./catalog/live-states-gallery.jsx";
import { MobileGallery } from "./catalog/mobile-gallery.jsx";
import { SubagentGallery } from "./catalog/subagent-gallery.jsx";
import { ConversationScreen, PaneGridScreen, MobileConversationScreen } from "./layout/index.js";
import { CommandPalette, ToastContainer, PulsePairingPanel } from "./components/index.js";
import { store, setState as setStoreState } from "./data/store.js";
import { togglePalette, closePalette } from "./data/palette.js";
import { isPulsePairingOpen, subscribePulsePairing, closePulsePairing } from "./data/pulse-pairing-panel.js";
import { hasBlockingOverlay } from "./data/overlays.js";
import {
  loadSessions, startPolling, stopPolling,
  startUsagePolling, stopUsagePolling,
} from "./data/session-actions.js";
import { getVersion, reconnectAll, syncConnections } from "./data/api.js";
import { refreshPushState } from "./data/push-client.js";
import {
  setMobile, autoFillTiles, autoSelectMobile, openSession,
} from "./data/tile-actions.js";

const welcomeStyle = {
  maxWidth: "640px",
  margin: "0 auto",
  padding: "var(--space-12) var(--space-6) var(--space-6)",
  textAlign: "center",
};

function Welcome() {
  return (
    <div style={welcomeStyle}>
      <h1
        style={{
          fontSize: "var(--text-2xl)",
          fontWeight: "var(--weight-semibold)",
          letterSpacing: "var(--tracking-tight)",
          color: "var(--peach)",
        }}
      >
        moa · next
      </h1>
      <p
        style={{
          fontSize: "var(--text-md)",
          color: "var(--subtext0)",
          lineHeight: "var(--leading-relaxed)",
          marginTop: "var(--space-3)",
        }}
      >
        Scaffold for the new web frontend (Phase 0). Used to verify that the
        design tokens load correctly before building anything else.
      </p>
      <a
        href="?view=catalog"
        style={{
          display: "inline-block",
          marginTop: "var(--space-5)",
          fontSize: "var(--text-sm)",
          color: "var(--lavender)",
        }}
      >
        View primitives catalog →
      </a>
    </div>
  );
}

function CatalogScreen() {
  return (
    <>
      <div style={{ textAlign: "center", padding: "var(--space-3) 0 0" }}>
        <a
          href="?"
          style={{ fontSize: "var(--text-sm)", color: "var(--lavender)" }}
        >
          ← Back to conversation screen
        </a>
      </div>
      <Welcome />
      <Catalog />
    </>
  );
}

// GALLERIES — the mock-driven design galleries (catalog / grid / live / mobile).
// Reachable by direct URL only (?view=…); see GALLERY_LINKS below for the
// discreet footer nav rendered ONLY on the galleries, never on the real
// conversation/grid screens (decision D5: no floating ViewSwitch over live UI).
const GALLERY_LINKS = [
  { key: "catalog", label: "Catalog", href: "?view=catalog" },
  { key: "live", label: "Live states", href: "?view=live" },
  { key: "subagent", label: "Subagent", href: "?view=subagent" },
  { key: "mobile", label: "Mobile", href: "?view=mobile" },
];

const galleryNavStyle = {
  display: "flex",
  justifyContent: "center",
  gap: "var(--space-4)",
  padding: "var(--space-4)",
  borderTop: "1px solid var(--surface0)",
  fontSize: "var(--text-sm)",
};

// GalleryNav — the discreet, non-intrusive way to move between galleries. It
// is a static footer strip (not a floating overlay), so it never covers the
// design being reviewed and never appears over the real product screens.
function GalleryNav({ current }) {
  return (
    <nav style={galleryNavStyle} aria-label="Galleries">
      <a href="?" style={{ color: "var(--overlay1)" }}>← Conversation</a>
      {GALLERY_LINKS.map((v) => (
        <a
          key={v.key}
          href={v.href}
          aria-current={v.key === current ? "page" : undefined}
          style={{ color: v.key === current ? "var(--peach)" : "var(--lavender)" }}
        >
          {v.label}
        </a>
      ))}
    </nav>
  );
}

// view — selects the screen. Absence (or an unknown value) shows the REAL,
// store-connected conversation screen (5C). `?view=grid` opens the real pane
// grid (still presentational until 5G). `?view=catalog|live|mobile` open the
// mock galleries with their GalleryNav.
const params = new URLSearchParams(location.search);
const view = params.get("view");

// useBootstrap wires the app to the data engine: session loading, polling,
// version, mobile breakpoint, and foreground/background lifecycle. Ported from
// the old SPA's App (pkg/serve/frontend/src/app.jsx), sessions-only — PWA,
// push, palette, pairing, hotkeys are deferred (see the // 5x notes).
function useBootstrap() {
  const [version, setVersion] = useState(null);
  const [state, setState] = useState(store.get());

  useEffect(() => store.subscribe(setState), []);

  // Mobile breakpoint → setMobile. Also lock the document to the viewport on
  // mobile (adds .mobile-locked to <html>): the mobile shell owns its own
  // internal scroll (.mconv-stream), so the page itself must not scroll — on
  // iOS a scrollable document lets Safari pan the whole page up to reveal a
  // focused input and never pans it back when the keyboard closes, leaving a
  // gap and pushing the header out of view (reset.css min-height:100vh made the
  // document taller than the visual viewport). Locking html/body/#root to the
  // dynamic viewport height keeps the header pinned.
  useEffect(() => {
    const mq = window.matchMedia("(max-width: 768px)");
    const handler = (e) => {
      setMobile(e.matches);
      document.documentElement.classList.toggle("mobile-locked", e.matches);
    };
    handler(mq);
    mq.addEventListener("change", handler);
    return () => {
      mq.removeEventListener("change", handler);
      document.documentElement.classList.remove("mobile-locked");
    };
  }, []);

  // Version poll: state changes at most once per six-hour server cache window;
  // retry 60s on failure, refresh every 6h.
  useEffect(() => {
    let retry;
    const refresh = () => getVersion().then(setVersion).catch(() => {
      retry = setTimeout(refresh, 60 * 1000);
    });
    refresh();
    const timer = setInterval(refresh, 6 * 60 * 60 * 1000);
    return () => { clearInterval(timer); clearTimeout(retry); };
  }, []);

  // Initial session load + selection, polling.
  useEffect(() => {
    let mounted = true;
    loadSessions().then(() => {
      if (!mounted) return; // unmounted mid-flight: don't touch the store/view
      setStoreState({ sessionsLoaded: true });
      const wanted = new URLSearchParams(location.search).get("session");
      if (wanted && openSession(wanted)) {
        history.replaceState({}, "", location.pathname); // don't re-pin on refresh
      } else if (store.get().isMobile) {
        autoSelectMobile();
      } else {
        autoFillTiles();
      }
    });
    startPolling();
    startUsagePolling();
    // Reconcile the browser's actual push state on load (D4: /next relies on the
    // root /sw.js, no SW registration here). Guarded internally for unsupported.
    refreshPushState();
    return () => {
      mounted = false;
      stopPolling();
      stopUsagePolling();
      syncConnections([]); // tear down every live WS + pending reconnect
    };
  }, []);

  // Foreground/background + online lifecycle: reconnect + refresh on return,
  // pause polling while hidden.
  useEffect(() => {
    const onVisibility = () => {
      if (document.visibilityState === "visible") {
        reconnectAll();
        loadSessions();
        startPolling();
      } else {
        stopPolling();
      }
    };
    const onOnline = () => {
      if (document.visibilityState !== "visible") return;
      reconnectAll();
      loadSessions();
    };
    document.addEventListener("visibilitychange", onVisibility);
    window.addEventListener("online", onOnline);
    return () => {
      document.removeEventListener("visibilitychange", onVisibility);
      window.removeEventListener("online", onOnline);
    };
  }, []);

  // Re-fill tiles / re-select mobile when the layout or session count changes,
  // so a newly-loaded session lands in the focused tile automatically.
  useEffect(() => {
    if (!state.isMobile) autoFillTiles();
    else autoSelectMobile();
  }, [state.isMobile, Object.keys(state.sessions).length]);

  // ⌘K / Ctrl+K — global command-palette toggle (5H). Active in every view.
  // The chord always works, even inside the composer textarea (spec §6): we
  // never gate on the focus target, so ⌘K opens/closes from anywhere. esc is
  // handled inside the palette itself (this only owns the open chord).
  useEffect(() => {
    const onKey = (e) => {
      if ((e.metaKey || e.ctrlKey) && (e.key === "k" || e.key === "K")) {
        // Defer to a higher-layer overlay (model/settings popover, etc.): don't
        // open the palette underneath it (spec §6). The palette closing itself
        // still works because it owns esc; ⌘K when the palette is the top layer
        // toggles it (it never registers as a blocking overlay).
        if (hasBlockingOverlay() && !store.get().paletteOpen) return;
        e.preventDefault();
        togglePalette("search");
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  // 5x: warm-focus push (SW postMessage), hotkeys (Ctrl+1..9), and
  // service-worker registration for /next are wired in later subphases (D4: no
  // /next SW in phase 5). Pulse pairing + push subscription land in 5N.

  return version;
}

// GlobalPairingPanel — the Pulse pairing Sheet (5N), mounted ONCE next to
// GlobalPalette so the ⌘K "Pair Pulse…" action can open it over any real
// screen. Open state lives in the pulse-pairing-panel controller (a small
// global pub/sub, not the session store — pairing is device-wide).
function GlobalPairingPanel() {
  const [open, setOpen] = useState(isPulsePairingOpen());
  useEffect(() => subscribePulsePairing(setOpen), []);
  return <PulsePairingPanel open={open} onClose={closePulsePairing} />;
}

// GlobalPalette — the ⌘K command palette (5H), mounted ONCE here so it's global
// to conversation / grid / mobile (outside the view switch). It subscribes to
// the store for open state + context derivation: context is the current view
// (grid vs mobile vs conversation) and focusedPane is the grid's focused tile's
// 1-based index (null off the grid). The palette reads the session list from
// the store itself, so this only supplies open/close + chassis context.
function GlobalPalette() {
  const [state, setState] = useState(store.get());
  useEffect(() => store.subscribe(setState), []);

  const context = view === "grid" ? "grid" : state.isMobile ? "mobile" : "conversation";
  let focusedPane = null;
  if (context === "grid") {
    const ids = allTileIdsSafe(state.tileTree);
    const idx = ids.indexOf(state.focusedTile);
    focusedPane = idx >= 0 ? idx + 1 : null;
  }

  return (
    <CommandPalette
      open={state.paletteOpen}
      onClose={closePalette}
      context={context}
      focusedPane={focusedPane}
      initialStep={state.paletteStep}
    />
  );
}

// allTileIdsSafe — DFS tile order without importing tileTree's helper twice
// (findTile is already imported for other derivations); a tiny local walk keeps
// the focusedPane derivation self-contained.
function allTileIdsSafe(tree) {
  if (!tree) return [];
  if (tree.type === "tile") return [tree.id];
  return tree.children.flatMap(allTileIdsSafe);
}

// App — routes to the selected screen. The conversation screen is the default
// and the only 5C-connected one; galleries stay mock. Bootstrap runs for every
// view so returning to "?" keeps a live store, but galleries just don't consume
// it. The command palette mounts over the REAL screens only (never the mock
// galleries).
function App() {
  const version = useBootstrap();
  const [state, setState] = useState(store.get());
  useEffect(() => store.subscribe(setState), []);

  if (view === "catalog") {
    return (
      <>
        <CatalogScreen />
        <GalleryNav current="catalog" />
      </>
    );
  }
  if (view === "live") {
    return (
      <>
        <LiveStatesGallery />
        <GalleryNav current="live" />
      </>
    );
  }
  if (view === "subagent") {
    return (
      <>
        <SubagentGallery />
        <GalleryNav current="subagent" />
      </>
    );
  }
  if (view === "mobile") {
    return (
      <>
        <MobileGallery />
        <GalleryNav current="mobile" />
      </>
    );
  }
  if (view === "grid") {
    // Real, store-connected pane grid (5G) — no ViewSwitch overlay (D5).
    return (
      <>
        <PaneGridScreen version={version} />
        <GlobalPalette />
        <GlobalPairingPanel />
        <ToastContainer />
      </>
    );
  }
  // Default: real, store-connected conversation screen (5C). On a mobile
  // viewport (state.isMobile, driven by the matchMedia breakpoint in
  // useBootstrap) mount the connected mobile screen (5I) instead of the desktop
  // ConversationScreen. Both are single-session containers over the same store;
  // the GlobalPalette mounts over either (its context derives to 'mobile'). No
  // ViewSwitch (D5).
  if (state.isMobile) {
    return (
      <>
        <MobileConversationScreen />
        <GlobalPalette />
        <GlobalPairingPanel />
        <ToastContainer />
      </>
    );
  }
  return (
    <>
      <ConversationScreen version={version} />
      <GlobalPalette />
      <GlobalPairingPanel />
      <ToastContainer />
    </>
  );
}

render(<App />, document.getElementById("root"));
