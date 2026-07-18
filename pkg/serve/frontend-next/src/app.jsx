import { render } from "preact";
import "./index.css";
import { Catalog } from "./catalog/catalog.jsx";
import { ConversationScreen } from "./layout/index.js";

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

// view — alterna entre la pantalla de conversación (Fase 2, lo que queremos
// probar por defecto) y el catálogo de primitivas (Fase 0/1). `?view=catalog`
// vuelve al catálogo; cualquier otro valor (o ausencia) muestra la
// conversación.
const params = new URLSearchParams(location.search);
const view = params.get("view");

function App() {
  if (view === "catalog") return <CatalogScreen />;
  return <ConversationScreen />;
}

render(<App />, document.getElementById("root"));
