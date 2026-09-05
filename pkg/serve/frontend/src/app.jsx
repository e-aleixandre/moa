import { render } from "preact";
import { useState, useEffect } from "preact/hooks";
import "./index.css";
import { ConversationScreen, PaneGridScreen, MobileConversationScreen, DesktopShell } from "./layout/index.js";
import { CommandPalette, ToastContainer, PulsePairingPanel, ArtifactsDrawer } from "./components/index.js";
import { store, setState as setStoreState } from "./data/store.js";
import { useStore } from "./hooks/useStore.js";
import { togglePalette, closePalette } from "./data/palette.js";
import { bindRouter, navigate } from "./data/router.js";
import { isPulsePairingOpen, subscribePulsePairing, closePulsePairing } from "./data/pulse-pairing-panel.js";
import { hasBlockingOverlay } from "./data/overlays.js";
import { globalPaletteContext, isDesktopGridShortcut, shouldLockMobileDocument } from "./data/app-layout.js";
import {
  loadSessions, startPolling, stopPolling,
  startUsagePolling, stopUsagePolling,
} from "./data/session-actions.js";
import { loadEvents, openInbox } from "./data/events.js"; // wake-on-event
import { getVersion, reconnectAll, syncConnections } from "./data/api.js";
import { adoptBuild } from "./data/stale-build.js";
import { addToast } from "./data/notifications.js";
import { refreshPushState } from "./data/push-client.js";
import { installOpenSessionNavigation } from "./data/push-navigation.js";
import {
  setMobile, autoFillTiles, autoSelectMobile, openSession, afterVisibilityChange,
} from "./data/tile-actions.js";

// Design galleries live in catalog-app.jsx and are served by `npm run catalog`.
// They are not imported here so they never enter the production bundle.

// view — selects the screen. Absence (or an unknown value) shows the conversation.
// `?view=grid` opens the pane grid. The value lives in the store (seeded from
// the URL) so the conversation ⇄ grid hop flips it in place via the router.

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
  const isMobile = useStore((s) => s.isMobile);
  const sessionCount = useStore((s) => Object.keys(s.sessions).length);

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
        const params = new URLSearchParams(location.search);
        const wanted = params.get("session");
        const wantedInbox = params.get("inbox") === "1";
        let stripped = false;
        if (wanted && openSession(wanted)) {
          // Strip only ?session= (a one-shot deep-link that must not re-pin on
          // refresh) while preserving ?view= so a `?view=grid&session=X` link
          // keeps the URL in sync with the store's seeded view.
          params.delete("session");
          stripped = true;
        } else if (store.get().isMobile) {
          autoSelectMobile();
        } else {
          autoFillTiles();
        }
        if (wantedInbox) {
          openInbox();
          params.delete("inbox");
          stripped = true;
        }
        if (stripped) {
          const qs = params.toString();
          history.replaceState({}, "", qs ? `${location.pathname}?${qs}` : location.pathname);
        }
      })
      .catch(() => {})
      .finally(() => {
        if (mounted) setStoreState({ sessionsLoaded: true });
      });
    startPolling();
    startUsagePolling();
    loadEvents(); // wake-on-event: paint the inbox on first load, not one tick later
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
        loadEvents(); // wake-on-event: an event may have arrived while away
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
    if (!isMobile) autoFillTiles();
    else autoSelectMobile();
  }, [isMobile, sessionCount]);

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

  // ⌘G / Ctrl+G — the grid button advertises this desktop shortcut. Keep it
  // below transient overlays, and leave the mobile drawer's keyboard entirely
  // alone even when a hardware keyboard is attached.
  useEffect(() => {
    const onKey = (e) => {
      const state = store.get();
      if (!isDesktopGridShortcut(e, state, hasBlockingOverlay())) return;
      e.preventDefault();
      if (state.view !== "grid") navigate("grid");
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
  const open = useStore((s) => s.paletteOpen);
  const context = useStore(globalPaletteContext);
  const focusedPane = useStore((s) => {
    if (globalPaletteContext(s) !== "grid") return null;
    const ids = allTileIdsSafe(s.tileTree);
    const idx = ids.indexOf(s.focusedTile);
    return idx >= 0 ? idx + 1 : null;
  });
  const initialStep = useStore((s) => s.paletteStep);

  return (
    <CommandPalette
      open={open}
      onClose={closePalette}
      context={context}
      focusedPane={focusedPane}
      initialStep={initialStep}
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

// App — routes to the selected screen. Conversation is the default; `?view=grid`
// is the pane grid. Design galleries are a separate catalog-app, not this one.
function App() {
  const version = useBootstrap();
  const isMobile = useStore((s) => s.isMobile);
  const view = useStore((s) => s.view);

  // Every mobile surface supplies its own app scroller, irrespective of a
  // stale grid URL, so document scrolling must stay locked for all of them.
  useEffect(() => {
    document.documentElement.classList.toggle("mobile-locked", shouldLockMobileDocument({ isMobile }));
  }, [isMobile]);

  if (isMobile) {
    return (
      <>
        <MobileConversationScreen version={version} />
        <GlobalPalette />
        <GlobalPairingPanel />
        <ArtifactsDrawer />
        <ToastContainer />
      </>
    );
  }
  return (
    <>
      <DesktopShell version={version}>
        {view === "grid" ? <PaneGridScreen /> : <ConversationScreen />}
      </DesktopShell>
      <GlobalPalette />
      <GlobalPairingPanel />
      {/* Artifacts — ONE shared drawer for conversation and grid, mounted
          globally so switching screens never duplicates or loses a reader. */}
      <ArtifactsDrawer />
      <ToastContainer />
    </>
  );
}

render(<App />, document.getElementById("root"));
