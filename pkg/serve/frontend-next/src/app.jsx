import { render } from "preact";
import "./index.css";
import { Catalog } from "./catalog/catalog.jsx";
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
        Andamiaje del nuevo frontend web (Fase 0). Sirve para verificar que los
        tokens de diseño cargan correctamente antes de construir nada más.
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

// ViewSwitch — nav flotante mínimo entre las tres pantallas de escritorio del
// frontend-next (conversation / grid / catalog). Solo navegación por enlaces
// reales (?view=…), sin estado de router: coherente con el patrón ya usado
// por Welcome/CatalogScreen. Anclado abajo-derecha para no solaparse con la
// attention lamp de GridToolbar (arriba-derecha) ni con el ChatHead.
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

// view — alterna entre las pantallas de escritorio: conversación (Fase 2,
// por defecto), grid de paneles (Fase 3A) y catálogo de primitivas (Fase
// 0/1). `?view=grid` abre PaneGridScreen, `?view=catalog` el catálogo,
// cualquier otro valor (o ausencia) muestra la conversación.
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
  return (
    <>
      <ViewSwitch current={null} />
      <ConversationScreen />
    </>
  );
}

render(<App />, document.getElementById("root"));
