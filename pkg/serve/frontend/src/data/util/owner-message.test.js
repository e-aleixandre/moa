import { test, expect } from "bun:test";
import { ownerMessageSummary, ownerMessageFolds, OWNER_MESSAGE_FOLD_CHARS } from "./owner-message.js";

test("the summary is the first line, without its markdown", () => {
  expect(ownerMessageSummary("## Encargo nuevo en `design/ambient-migration`\n\nmore")).toBe(
    "Encargo nuevo en design/ambient-migration",
  );
  expect(ownerMessageSummary("- **Pushea la rama.** y luego\nmás")).toBe("Pushea la rama. y luego");
});

test("a link keeps its text and loses its target", () => {
  expect(ownerMessageSummary("Mira [la captura](https://example.com/a.png) del móvil")).toBe(
    "Mira la captura del móvil",
  );
});

test("a first line that only announces what follows pulls the next one up", () => {
  expect(ownerMessageSummary("Contexto:\nel bloque del owner ocupa toda la pantalla")).toBe(
    "Contexto: el bloque del owner ocupa toda la pantalla",
  );
});

test("a long first line is left alone even when it ends in a colon", () => {
  const long = `${"x".repeat(60)}:`;
  expect(ownerMessageSummary(`${long}\nsegunda`)).toBe(long);
});

test("leading blank lines and whitespace are not the summary", () => {
  expect(ownerMessageSummary("\n\n   \nEl encargo de verdad")).toBe("El encargo de verdad");
  expect(ownerMessageSummary("")).toBe("");
  expect(ownerMessageSummary(undefined)).toBe("");
});

test("only messages long enough to bury the conversation fold", () => {
  expect(ownerMessageFolds("Pushea la rama y deja el árbol limpio")).toBe(false);
  expect(ownerMessageFolds("x".repeat(OWNER_MESSAGE_FOLD_CHARS + 1))).toBe(true);
  expect(ownerMessageFolds(`  ${"x".repeat(OWNER_MESSAGE_FOLD_CHARS)}  `)).toBe(false);
});

test("the subject wins over the preamble: a real encargo summarises by its bold ask", () => {
  const real = [
    "Trabajas en `/home/ealeixandre/dev/moa/design-visual`, rama `design/ambient-migration` (ahora limpia, último commit `1412fd71`). NO mergear, NO desplegar.",
    "",
    'Encargo: **la skill `book-init`**, fase 3 del plan del "libro" de los owners.',
    "",
    "## Contexto que ya existe (no lo reinventes, léelo)",
  ].join("\n");
  expect(ownerMessageSummary(real)).toBe(
    'Encargo: la skill book-init, fase 3 del plan del "libro" de los owners.',
  );
});

test("bold in the first line changes nothing: that line was the summary anyway", () => {
  expect(ownerMessageSummary("**Hazlo colapsable.** Y nada más\nresto")).toBe(
    "Hazlo colapsable. Y nada más",
  );
});

test("bold buried in the criteria does not become the subject", () => {
  const lines = ["El encargo de verdad", ...Array(14).fill("relleno"), "**móvil primero**"];
  expect(ownerMessageSummary(lines.join("\n"))).toBe("El encargo de verdad");
});

test("empty emphasis is not bold", () => {
  expect(ownerMessageSummary("Primera línea\nsegunda con ** ** vacío")).toBe("Primera línea");
});

// These assignments are made of paths and identifiers: a summary that eats the
// underscores out of the very file it names is worse than no summary.
test("paths and identifiers survive the stripping", () => {
  expect(ownerMessageSummary("Toca `data/foo_bar.js` y `__init__.py`")).toBe(
    "Toca data/foo_bar.js y __init__.py",
  );
  expect(ownerMessageSummary("**Arregla `pkg/serve/util_test.go`**")).toBe(
    "Arregla pkg/serve/util_test.go",
  );
  expect(ownerMessageSummary("Mira src/**/*.jsx antes")).toBe("Mira src/**/*.jsx antes");
});

test("emphasis around whole words still comes off", () => {
  expect(ownerMessageSummary("Esto es *importante* y _urgente_")).toBe(
    "Esto es importante y urgente",
  );
});
