import { test, expect } from "bun:test";
import {
  DOC_NOTES_MARKER, QUICK_REPLIES, clip, docNotesMessage, draftKey, loadNotes,
  parseDocNotes, questionMarker, resolveAnchors, saveNotes, textInRect,
} from "./doc-notes.js";

function memoryStorage() {
  const values = new Map();
  return {
    getItem: (key) => (values.has(key) ? values.get(key) : null),
    setItem: (key, value) => values.set(key, value),
    removeItem: (key) => values.delete(key),
  };
}

const file = { name: "decisiones.md", url: "/api/sessions/S1/files/F1" };

test("drafts are per session and document, and an empty list removes the key", () => {
  const storage = memoryStorage();
  saveNotes("S1", "F1", [{ id: "a", comment: "x" }], storage);
  expect(loadNotes("S1", "F1", storage)).toHaveLength(1);
  expect(loadNotes("S1", "F2", storage)).toEqual([]);
  saveNotes("S1", "F1", [], storage);
  expect(storage.getItem(draftKey("S1", "F1"))).toBeNull();
});

test("a corrupt or foreign draft loads as no notes instead of throwing", () => {
  const storage = memoryStorage();
  storage.setItem(draftKey("S1", "F1"), "{not json");
  expect(loadNotes("S1", "F1", storage)).toEqual([]);
  storage.setItem(draftKey("S1", "F1"), '{"a":1}');
  expect(loadNotes("S1", "F1", storage)).toEqual([]);
});

test("a full storage never throws on save", () => {
  const storage = { getItem: () => null, setItem: () => { throw new Error("quota"); }, removeItem: () => {} };
  expect(() => saveNotes("S1", "F1", [{ id: "a" }], storage)).not.toThrow();
});

test("clip keeps short text and cuts long text in the middle, 70% head", () => {
  expect(clip("  a   b \n c ")).toBe("a b c");
  const long = "x".repeat(2000);
  const out = clip(long, 600);
  expect(out.length).toBeLessThanOrEqual(600);
  expect(out).toContain(" … ");
});

test("the message names the file, numbers the notes and keeps their order", () => {
  const { text, attachments } = docNotesMessage(file, [
    { kind: "text", quote: "El coche impone restricciones", where: "Contexto", comment: "Esto es lo esencial" },
    { kind: "text", quote: "P1. ¿Apagadas por defecto?", where: "Preguntas abiertas", comment: "Yes" },
  ]);
  expect(attachments).toEqual([]);
  expect(text.startsWith(`${DOC_NOTES_MARKER} · decisiones.md · 2 notes]`)).toBe(true);
  expect(text).toContain("file: /api/sessions/S1/files/F1");
  expect(text).toContain("## 1 · Contexto\n> El coche impone restricciones\nEsto es lo esencial");
  expect(text).toContain("## 2 · Preguntas abiertas\n> P1. ¿Apagadas por defecto?\nYes");
});

test("one note reads 'note', not 'notes'", () => {
  const { text } = docNotesMessage(file, [{ kind: "text", quote: "q", comment: "c" }]);
  expect(text.split("\n")[0]).toBe("[Document notes · decisiones.md · 1 note]");
});

test("parse is the inverse of the message, multi-line comments included", () => {
  const notes = [
    { kind: "text", quote: "Primera cita", where: "Contexto › Coche", comment: "línea uno\nlínea dos" },
    { kind: "text", quote: "Segunda", where: "", comment: "Your call" },
  ];
  const parsed = parseDocNotes(docNotesMessage(file, notes).text);
  expect(parsed.name).toBe("decisiones.md");
  expect(parsed.url).toBe("/api/sessions/S1/files/F1");
  expect(parsed.notes).toEqual([
    { n: 1, where: "Contexto › Coche", quote: "Primera cita", comment: "línea uno\nlínea dos" },
    { n: 2, where: "", quote: "Segunda", comment: "Your call — go with your recommendation." },
  ]);
});

test("a comment that looks like the format cannot forge a note or a quote", () => {
  const notes = [
    { kind: "text", quote: "cita", comment: "ok\n## 9 · falso\n> otra cita" },
    { kind: "text", quote: "", comment: "> empieza como cita" },
  ];
  const parsed = parseDocNotes(docNotesMessage(file, notes).text);
  expect(parsed.notes).toHaveLength(2);
  expect(parsed.notes[0].comment).toBe("ok\n## 9 · falso\n> otra cita");
  expect(parsed.notes[1].quote).toBe("");
  expect(parsed.notes[1].comment).toBe("> empieza como cita");
});

test("a message without the marker is not a notes message", () => {
  expect(parseDocNotes("hola")).toBeNull();
  expect(parseDocNotes("[Document notes · roto")).toBeNull();
  expect(parseDocNotes(null)).toBeNull();
});

test("an empty comment is said out loud rather than silently dropped", () => {
  const { text } = docNotesMessage(file, [{ kind: "text", quote: "q", comment: "  " }]);
  expect(text).toContain("(no comment)");
});

test("numbered questions are recognised only with the P<n>. convention and a question mark", () => {
  expect(questionMarker("P1.", "P1. ¿Apagadas?")).toBe("P1");
  expect(questionMarker("P12.", "P12. Do we ship?")).toBe("P12");
  expect(questionMarker("P1", "P1 ¿Sí?")).toBe("P1");
  expect(questionMarker("P1.", "P1. Esto es una afirmación.")).toBeNull();
  expect(questionMarker("Nota.", "Nota. ¿Y esto?")).toBeNull();
  expect(questionMarker("", "¿Sin marca?")).toBeNull();
});

test("quick replies are Yes, No and Your call", () => {
  expect(QUICK_REPLIES).toEqual(["Yes", "No", "Your call"]);
});

test("anchors follow the quote when the document changed under the draft", () => {
  const blocks = ["Intro", "Nuevo párrafo", "El coche impone restricciones", "Otro"];
  const notes = [
    { id: "a", anchor: 1, quote: "El coche impone restricciones" },
    { id: "b", anchor: 3, quote: "Otro" },
    { id: "c", anchor: 0, quote: "texto que ya no existe" },
    { id: "d", anchor: 2, quote: "impone" },
    { id: "e", anchor: null, quote: "" },
  ];
  const out = resolveAnchors(blocks, notes);
  expect(out.get("a")).toBe(2);
  expect(out.get("b")).toBe(3);
  expect(out.has("c")).toBe(false);
  expect(out.get("d")).toBe(2);
  expect(out.has("e")).toBe(false);
});

test("a literal ellipsis in a quote does not re-anchor to the wrong block", () => {
  const blocks = ["Shared intro … NEW ending", "Shared intro … original ending"];
  const out = resolveAnchors(blocks, [{ id: "a", anchor: 0, quote: "Shared intro … original ending" }]);
  expect(out.get("a")).toBe(1);
});

test("a selection across blocks stays at the block it started in", () => {
  const blocks = ["Primer párrafo largo que acaba aquí", "Segundo párrafo"];
  const out = resolveAnchors(blocks, [{ id: "a", anchor: 0, quote: "Primer párrafo largo que acaba aquí Segundo párrafo" }]);
  expect(out.get("a")).toBe(0);
});

test("comment strings that are Object.prototype names are plain comments", () => {
  for (const comment of ["constructor", "toString", "hasOwnProperty", "__proto__"]) {
    const { text } = docNotesMessage(file, [{ kind: "text", quote: "q", comment }]);
    expect(parseDocNotes(text).notes[0].comment).toBe(comment);
  }
});

test("literal backslashes before a reserved prefix survive the round trip", () => {
  const comments = ["\\## 2 · literal", "\\> literal", "\\\\> dos", "texto\n\\## 3 · x"];
  for (const comment of comments) {
    const { text } = docNotesMessage(file, [{ kind: "text", quote: "q", comment }]);
    expect(parseDocNotes(text).notes[0].comment).toBe(comment);
  }
});

test("a quote in a later block does not land on the block before it", () => {
  const blocks = ["Intro", "Segundo párrafo exacto", "Tercero"];
  expect(resolveAnchors(blocks, [{ id: "a", anchor: 0, quote: "Segundo párrafo exacto" }]).get("a")).toBe(1);
});

test("a prefix shared with a changed paragraph is not a match", () => {
  const blocks = ["A shared introduction long enough to reach thirty characters, DIFFERENT conclusion"];
  const quote = "A shared introduction long enough to reach thirty characters, ORIGINAL conclusion";
  expect(resolveAnchors(blocks, [{ id: "a", anchor: 0, quote }]).has("a")).toBe(false);
});

test("a note comes back when the paragraph it quotes comes back", () => {
  const note = { id: "a", anchor: 3, quote: "Mi párrafo" };
  expect(resolveAnchors(["x", "y"], [note]).has("a")).toBe(false);
  expect(resolveAnchors(["x", "Mi párrafo", "y"], [note]).get("a")).toBe(1);
});

test("area crops live in memory only: a saved draft keeps the thumbnail and the rectangle", () => {
  const storage = memoryStorage();
  saveNotes("S1", "F1", [{ id: "a", kind: "area", crop: "data:image/jpeg;base64,FULL", thumb: "data:image/jpeg;base64,T", rect: { page: 1, x: 0.1, y: 0.2, w: 0.5, h: 0.3 }, comment: "x" }], storage);
  const [note] = loadNotes("S1", "F1", storage);
  expect(note.crop).toBeUndefined();
  expect(note.thumb).toBe("data:image/jpeg;base64,T");
  expect(note.rect).toEqual({ page: 1, x: 0.1, y: 0.2, w: 0.5, h: 0.3 });
});

// A page of 600x800 user-space units, y up, shown with y down at scale 1.
const toViewport = (x, y) => [x, 800 - y];
const item = (str, x, y, width, height = 12) => ({ str, transform: [height, 0, 0, height, x, y], width, height });

test("text inside the box is returned in reading order, lines apart", () => {
  const items = [
    item("Opción B: secciones por tipo", 60, 700, 200),
    item("Opción A: una lista", 60, 720, 150),
    item("fuera del recuadro", 60, 300, 120),
    item("segunda línea", 60, 680, 100),
  ];
  // viewport box covering y 70..140 (user y 660..730) and x 40..300
  const text = textInRect(items, toViewport, { left: 40, top: 70, right: 300, bottom: 140 });
  expect(text).toBe("Opción A: una lista\nOpción B: secciones por tipo\nsegunda línea");
});

test("items on the same baseline join with a space, an empty box gives nothing", () => {
  const items = [item("Hola", 60, 700, 30), item("mundo", 100, 700, 40)];
  expect(textInRect(items, toViewport, { left: 40, top: 80, right: 300, bottom: 120 })).toBe("Hola mundo");
  expect(textInRect(items, toViewport, { left: 400, top: 0, right: 500, bottom: 50 })).toBe("");
  expect(textInRect([], toViewport, { left: 0, top: 0, right: 10, bottom: 10 })).toBe("");
});

test("an item only grazed by the box is not part of it", () => {
  const items = [item("rozado", 60, 700, 100)]; // viewport top 88..100, center 94
  expect(textInRect(items, toViewport, { left: 0, top: 95, right: 600, bottom: 200 })).toBe("");
});
