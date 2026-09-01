import { render } from "preact";
import { useState, useEffect } from "preact/hooks";
import "./index.css";
import { ConversationScreen, PaneGridScreen, MobileConversationScreen } from "./layout/index.js";
import { ToastContainer, CommandPalette } from "./components/index.js";
import { store } from "./data/store.js";
import { bindRouter } from "./data/router.js";
import { closePalette } from "./data/palette.js";
import { setMobile } from "./data/tile-actions.js";
import { Catalog } from "./catalog/catalog.jsx";
import { LiveStatesGallery } from "./catalog/live-states-gallery.jsx";
import { MobileGallery } from "./catalog/mobile-gallery.jsx";
import { SubagentGallery } from "./catalog/subagent-gallery.jsx";
import { DesktopLab, PhoneLab } from "./catalog/desktop-lab.jsx";
import { seedCatalogStore } from "./catalog/specimen.js";
import { installCatalogBackend } from "./catalog/catalog-backend.js";

// catalog-app — design lab. Not shipped in the production binary. Served by
// `npm run catalog`: the real screens in frames, plus the token/live galleries.
// A change to ChatHead is a change here because there is no second chrome.
// The Go server is replaced by catalog-backend (fixtures + one init frame).

installCatalogBackend({ getSessions: () => store.get().sessions });
seedCatalogStore();

const LINKS = [
  { key: "desktop", label: "Desktop", href: "?view=desktop" },
  { key: "mobile", label: "Phone", href: "?view=mobile" },
  { key: "grid", label: "Grid", href: "?view=grid" },
  { key: "catalog", label: "Tokens", href: "?view=catalog" },
  { key: "live", label: "Live", href: "?view=live" },
  { key: "subagent", label: "Subagent", href: "?view=subagent" },
  { key: "pieces", label: "Mobile pieces", href: "?view=pieces" },
];

function Nav({ current }) {
  return (
    <nav class="catalog-nav" aria-label="Design lab">
      {LINKS.map((v) => (
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

function useCatalogBootstrap() {
  const [state, setState] = useState(store.get());
  useEffect(() => store.subscribe(setState), []);
  useEffect(() => bindRouter(), []);
  useEffect(() => {
    const mq = window.matchMedia("(max-width: 768px)");
    const handler = (e) => setMobile(e.matches);
    handler(mq);
    mq.addEventListener("change", handler);
    return () => mq.removeEventListener("change", handler);
  }, []);
  return state;
}

function LabPalette() {
  const [state, setState] = useState(store.get());
  useEffect(() => store.subscribe(setState), []);
  return (
    <CommandPalette
      open={state.paletteOpen}
      onClose={closePalette}
      context="conversation"
      focusedPane={null}
      initialStep={state.paletteStep}
    />
  );
}

function CatalogApp() {
  const state = useCatalogBootstrap();
  const view = state.view || "desktop";

  useEffect(() => {
    document.documentElement.classList.remove("mobile-locked");
  }, [view]);

  let body = null;
  if (view === "catalog") body = <Catalog />;
  else if (view === "live") body = <LiveStatesGallery />;
  else if (view === "subagent") body = <SubagentGallery />;
  else if (view === "pieces") body = <MobileGallery />;
  else if (view === "mobile") {
    body = (
      <PhoneLab>
        <MobileConversationScreen />
      </PhoneLab>
    );
  } else if (view === "grid") {
    body = (
      <DesktopLab>
        <PaneGridScreen />
      </DesktopLab>
    );
  } else {
    body = (
      <DesktopLab>
        <ConversationScreen />
      </DesktopLab>
    );
  }

  return (
    <>
      {body}
      <Nav current={view} />
      <LabPalette />
      <ToastContainer />
    </>
  );
}

render(<CatalogApp />, document.getElementById("root"));
