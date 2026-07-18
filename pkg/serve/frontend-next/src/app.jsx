import { render } from "preact";
import "./index.css";
import { Catalog } from "./catalog/catalog.jsx";

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
    </div>
  );
}

function App() {
  return (
    <>
      <Welcome />
      <Catalog />
    </>
  );
}

render(<App />, document.getElementById("root"));
