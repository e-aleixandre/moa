import { render } from "preact";
import { useState, useEffect } from "preact/hooks";
import "./index.css";
import { Catalog } from "./catalog/catalog.jsx";
import { LiveStatesGallery } from "./catalog/live-states-gallery.jsx";
import { MobileGallery } from "./catalog/mobile-gallery.jsx";
import { ConversationScreen, PaneGridScreen } from "./layout/index.js";
import { store, setState as setStoreState } from "./data/store.js";
import {
  loadSessions, startPolling, stopPolling,
  startUsagePolling, stopUsagePolling,
} from "./data/session-actions.js";
import { getVersion, reconnectAll, syncConnections } from "./data/api.js";
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

  // Mobile breakpoint → setMobile.
  useEffect(() => {
    const mq = window.matchMedia("(max-width: 768px)");
    const handler = (e) => setMobile(e.matches);
    handler(mq);
    mq.addEventListener("change", handler);
    return () => mq.removeEventListener("change", handler);
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
    // 5x: registerServiceWorker() / refreshPushState() — PWA + push deferred.
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

  // 5x: warm-focus push (SW postMessage), command palette (⌘K), hotkeys
  // (Ctrl+1..9), Pulse pairing and service-worker registration are all wired in
  // later subphases.

  return version;
}

// App — routes to the selected screen. The conversation screen is the default
// and the only 5C-connected one; galleries stay mock. Bootstrap runs for every
// view so returning to "?" keeps a live store, but galleries just don't consume
// it.
function App() {
  const version = useBootstrap();

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
    return <PaneGridScreen version={version} />;
  }
  // Default: real, store-connected conversation screen (5C). No ViewSwitch (D5).
  return <ConversationScreen version={version} />;
}

render(<App />, document.getElementById("root"));
