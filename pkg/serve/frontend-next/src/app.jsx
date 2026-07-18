import { render } from "preact";
import "./index.css";
import { Catalog } from "./catalog/catalog.jsx";
import { LiveStatesGallery } from "./catalog/live-states-gallery.jsx";
import { MobileGallery } from "./catalog/mobile-gallery.jsx";
import { ConversationScreen, PaneGridScreen } from "./layout/index.js";

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

const VIEWS = [
  { key: null, label: "Conversation", href: "?" },
  { key: "grid", label: "Panes", href: "?view=grid" },
  { key: "live", label: "Live", href: "?view=live" },
  { key: "mobile", label: "Mobile", href: "?view=mobile" },
  { key: "catalog", label: "Catalog", href: "?view=catalog" },
];

const viewSwitchStyle = {
  position: "fixed",
  bottom: "var(--space-3)",
  right: "var(--space-3)",
  zIndex: "var(--z-overlay)",
  display: "flex",
  gap: "2px",
  background: "var(--crust)",
  border: "1px solid var(--surface0)",
  borderRadius: "var(--radius-md)",
  padding: "2px",
  boxShadow: "var(--shadow-md)",
};

function viewSwitchLinkStyle(active) {
  return {
    fontFamily: "var(--font)",
    fontSize: "var(--text-xs)",
    color: active ? "var(--crust)" : "var(--overlay1)",
    background: active ? "var(--peach)" : "transparent",
    borderRadius: "var(--radius-sm)",
    padding: "5px var(--space-3)",
    textDecoration: "none",
  };
}

// ViewSwitch — minimal floating nav between the three frontend-next desktop
// screens (conversation / grid / catalog). Link-only navigation
// (?view=…), no router state: consistent with the pattern already used
// by Welcome/CatalogScreen. Anchored bottom-right so it doesn't overlap the
// attention lamp of GridToolbar (top-right) or the ChatHead.
function ViewSwitch({ current }) {
  return (
    <nav style={viewSwitchStyle} aria-label="Switch view">
      {VIEWS.map((v) => (
        <a
          key={v.label}
          href={v.href}
          aria-current={v.key === current ? "page" : undefined}
          style={viewSwitchLinkStyle(v.key === current)}
        >
          {v.label}
        </a>
      ))}
    </nav>
  );
}

// view — switches between the desktop screens: conversation (Phase 2,
// default), pane grid (Phase 3A), live states (Phase 3B) and
// primitives catalog (Phase 0/1). `?view=grid` opens PaneGridScreen,
// `?view=live` opens LiveStatesGallery, `?view=catalog` the catalog,
// any other value (or absence) shows the conversation.
const params = new URLSearchParams(location.search);
const view = params.get("view");

function App() {
  if (view === "catalog") {
    return (
      <>
        <ViewSwitch current="catalog" />
        <CatalogScreen />
      </>
    );
  }
  if (view === "grid") {
    return (
      <>
        <ViewSwitch current="grid" />
        <PaneGridScreen />
      </>
    );
  }
  if (view === "live") {
    return (
      <>
        <ViewSwitch current="live" />
        <LiveStatesGallery />
      </>
    );
  }
  if (view === "mobile") {
    return (
      <>
        <ViewSwitch current="mobile" />
        <MobileGallery />
      </>
    );
  }
  return (
    <>
      <ViewSwitch current={null} />
      <ConversationScreen />
    </>
  );
}

render(<App />, document.getElementById("root"));
