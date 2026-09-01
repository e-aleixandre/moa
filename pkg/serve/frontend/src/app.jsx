import { render } from "preact";
import { useState, useEffect } from "preact/hooks";
import { lazy, Suspense } from "preact/compat";
import "./index.css";
import { ConversationScreen, PaneGridScreen, MobileConversationScreen } from "./layout/index.js";
import { CommandPalette, ToastContainer, PulsePairingPanel } from "./components/index.js";
import { store, setState as setStoreState } from "./data/store.js";
import { togglePalette, closePalette } from "./data/palette.js";
import { bindRouter } from "./data/router.js";
import { isPulsePairingOpen, subscribePulsePairing, closePulsePairing } from "./data/pulse-pairing-panel.js";
import { hasBlockingOverlay } from "./data/overlays.js";
import {
  loadSessions, startPolling, stopPolling,
  startUsagePolling, stopUsagePolling,
} from "./data/session-actions.js";
import { getVersion, reconnectAll, syncConnections } from "./data/api.js";
import { adoptBuild } from "./data/stale-build.js";
import { addToast } from "./data/notifications.js";
import { refreshPushState } from "./data/push-client.js";
import { installOpenSessionNavigation } from "./data/push-navigation.js";
import {
  setMobile, autoFillTiles, autoSelectMobile, openSession, afterVisibilityChange,
} from "./data/tile-actions.js";

// Galleries are development/reference surfaces, never part of the production
// startup graph. Dynamic imports retain direct ?view=… access while keeping
// their specimens and CSS out of app.js/app.css.
const loadCatalog = () => import("./catalog-entry.js");
const Catalog = lazy(() => loadCatalog().then((m) => ({ default: m.Catalog })));
const LiveStatesGallery = lazy(() => loadCatalog().then((m) => ({ default: m.LiveStatesGallery })));
const MobileGallery = lazy(() => loadCatalog().then((m) => ({ default: m.MobileGallery })));
const SubagentGallery = lazy(() => loadCatalog().then((m) => ({ default: m.SubagentGallery })));
const DesktopLab = lazy(() => loadCatalog().then((m) => ({ default: m.DesktopLab })));

function GalleryLoad({ children }) {
  useEffect(() => {
    const link = document.createElement("link");
    link.rel = "stylesheet";
    link.href = new URL("./catalog-entry.css", import.meta.url).href;
    document.head.appendChild(link);
    return () => link.remove();
  }, []);
  return <Suspense fallback={<div class="conversation-placeholder">Loading gallery…</div>}>{children}</Suspense>;
}

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
// conversation/grid screens (no floating ViewSwitch over live UI).
const GALLERY_LINKS = [
  { key: "catalog", label: "Catalog", href: "?view=catalog" },
  { key: "live", label: "Live states", href: "?view=live" },
  { key: "subagent", label: "Subagent", href: "?view=subagent" },
  { key: "mobile", label: "Mobile", href: "?view=mobile" },
  { key: "desktop", label: "Desktop", href: "?view=desktop" },
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
// store-connected conversation screen. `?view=grid` opens the real pane
// grid. `?view=catalog|live|subagent|mobile` open the mock galleries with their
// GalleryNav. The value lives in the store (seeded from the URL) so the
// conversation ⇄ grid hop flips it in place via the router (data/router.js)
// with no full-page reload; consumers read state.view reactively.

// checkBuild reacts to a /api/version response: reload when the page is running
// a bundle the server has replaced, and fall back to telling the user when even
// a cache-busting reload came back stale (on iOS only closing the app from the
// app switcher clears its memory cache).
function checkBuild(result) {
  adoptBuild(result, {
    onStale: () => addToast({
      title: "New version available",
      detail: "Close moa from the app switcher and open it again",
      type: "info",
    }),
  });
}

// useBootstrap wires the app to the data engine: session loading, polling,
// version, mobile breakpoint, and foreground/background lifecycle. Ported from
// the old SPA's App (pkg/serve/frontend/src/app.jsx).
function useBootstrap() {
  const [version, setVersion] = useState(null);
  const [state, setState] = useState(store.get());

  useEffect(() => store.subscribe(setState), []);

  // Install the single popstate listener so the browser Back/Forward buttons
  // keep the store's `view` in sync with the URL (in-app conversation ⇄ grid
  // hops use pushState, no reload — see data/router.js).
  useEffect(() => bindRouter(), []);

  // Warm notification taps use the same openSession behavior as a cold
  // ?session= deep link, waiting for the authoritative initial session list.
  useEffect(() => installOpenSessionNavigation(), []);

  // Mobile breakpoint → setMobile. The App below decides whether the current
  // view owns document scrolling: galleries need native document scroll while
  // the mobile conversation keeps its dedicated internal scroller.
  useEffect(() => {
    const mq = window.matchMedia("(max-width: 768px)");
    const handler = (e) => {
      setMobile(e.matches);
    };
    handler(mq);
    mq.addEventListener("change", handler);
    return () => {
      mq.removeEventListener("change", handler);
      document.documentElement.classList.remove("mobile-locked");
    };
  }, []);

  // Version poll: state changes at most once per six-hour server cache window;
  // retry 60s on failure, refresh every 6h. The same response also carries the
  // build id of the bundle the server is serving, which is checked on every
  // poll — a client running superseded code should not have to wait six hours
  // (see data/stale-build.js).
  useEffect(() => {
    let retry;
    const refresh = () => getVersion().then((v) => {
      setVersion(v);
      checkBuild(v);
    }).catch(() => {
      retry = setTimeout(refresh, 60 * 1000);
    });
    refresh();
    const timer = setInterval(refresh, 6 * 60 * 60 * 1000);
    return () => { clearInterval(timer); clearTimeout(retry); };
  }, []);

  // Initial session load + selection, polling.
  useEffect(() => {
    let mounted = true;
    loadSessions()
      .then(() => {
        if (!mounted) return; // unmounted mid-flight: don't touch the store/view
        const wanted = new URLSearchParams(location.search).get("session");
        if (wanted && openSession(wanted)) {
          // Strip only ?session= (a one-shot deep-link that must not re-pin on
          // refresh) while preserving ?view= so a `?view=grid&session=X` link
          // keeps the URL in sync with the store's seeded view.
          const params = new URLSearchParams(location.search);
          params.delete("session");
          const qs = params.toString();
          history.replaceState({}, "", qs ? `${location.pathname}?${qs}` : location.pathname);
        } else if (store.get().isMobile) {
          autoSelectMobile();
        } else {
          autoFillTiles();
        }
      })
      .catch(() => {})
      .finally(() => {
        if (mounted) setStoreState({ sessionsLoaded: true });
      });
    startPolling();
    startUsagePolling();
    // Reconcile the browser's actual push state on load (/next relies on the
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
        afterVisibilityChange();
        reconnectAll();
        loadSessions();
        startPolling();
        // Also restarts the usage timer, and refreshes immediately so the
        // status line is not showing a number from before the app was hidden.
        startUsagePolling();
        // Returning to a backgrounded PWA is when a user expects to see the
        // interface that was deployed meanwhile, and on iOS it is the only
        // moment an installed app re-reads anything at all.
        getVersion().then(checkBuild).catch(() => {});
      } else {
        stopPolling();
        // Without this the usage timer kept polling every minute in the
        // background: battery and data spent on a screen nobody is looking at.
        stopUsagePolling();
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

  // ⌘K / Ctrl+K — global command-palette toggle. Active in every view.
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

  return version;
}

// GlobalPairingPanel — the Pulse pairing Sheet, mounted ONCE next to
// GlobalPalette so the ⌘K "Pair Pulse…" action can open it over any real
// screen. Open state lives in the pulse-pairing-panel controller (a small
// global pub/sub, not the session store — pairing is device-wide).
function GlobalPairingPanel() {
  const [open, setOpen] = useState(isPulsePairingOpen());
  useEffect(() => subscribePulsePairing(setOpen), []);
  return <PulsePairingPanel open={open} onClose={closePulsePairing} />;
}

// GlobalPalette — the ⌘K command palette, mounted ONCE here so it's global
// to conversation / grid / mobile (outside the view switch). It subscribes to
// the store for open state + context derivation: context is the current view
// (grid vs mobile vs conversation) and focusedPane is the grid's focused tile's
// 1-based index (null off the grid). The palette reads the session list from
// the store itself, so this only supplies open/close + chassis context.
function GlobalPalette() {
  const [state, setState] = useState(store.get());
  useEffect(() => store.subscribe(setState), []);

  const context = state.view === "grid" ? "grid" : state.isMobile ? "mobile" : "conversation";
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
// and the only store-connected one; galleries stay mock. Bootstrap runs for every
// view so returning to "?" keeps a live store, but galleries just don't consume
// it. The command palette mounts over the REAL screens only (never the mock
// galleries).
function App() {
  const version = useBootstrap();
  const [state, setState] = useState(store.get());
  useEffect(() => store.subscribe(setState), []);
  const view = state.view;

  // Lock the document only for application screens that supply their own
  // mobile scroller. Catalog and gallery routes are long reference documents.
  useEffect(() => {
    const gallery = view === "catalog" || view === "live" || view === "subagent" || view === "mobile" || view === "desktop";
    document.documentElement.classList.toggle("mobile-locked", state.isMobile && !gallery);
  }, [state.isMobile, view]);

  if (view === "catalog") {
    return (
      <>
      <GalleryLoad><CatalogScreen /></GalleryLoad>
        <GalleryNav current="catalog" />
      </>
    );
  }
  if (view === "live") {
    return (
      <>
        <GalleryLoad><LiveStatesGallery /></GalleryLoad>
        <GalleryNav current="live" />
      </>
    );
  }
  if (view === "subagent") {
    return (
      <>
        <GalleryLoad><SubagentGallery /></GalleryLoad>
        <GalleryNav current="subagent" />
      </>
    );
  }
  if (view === "mobile") {
    return (
      <>
        <GalleryLoad><MobileGallery /></GalleryLoad>
        <GalleryNav current="mobile" />
      </>
    );
  }
  if (view === "desktop") {
    // The real ConversationScreen, framed. Not a mock: whatever ChatHead and
    // the status strip do in production is what this page shows.
    return (
      <>
        <GalleryLoad>
          <DesktopLab>
            <ConversationScreen version={version} />
          </DesktopLab>
        </GalleryLoad>
        <GalleryNav current="desktop" />
        <ToastContainer />
      </>
    );
  }
  if (view === "grid") {
    // Real, store-connected pane grid — no ViewSwitch overlay.
    return (
      <>
        <PaneGridScreen version={version} />
        <GlobalPalette />
        <GlobalPairingPanel />
        <ToastContainer />
      </>
    );
  }
  // Default: real, store-connected conversation screen. On a mobile
  // viewport (state.isMobile, driven by the matchMedia breakpoint in
  // useBootstrap) mount the connected mobile screen instead of the desktop
  // ConversationScreen. Both are single-session containers over the same store;
  // the GlobalPalette mounts over either (its context derives to 'mobile'). No
  // ViewSwitch.
  if (state.isMobile) {
    return (
      <>
        <MobileConversationScreen version={version} />
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
