import { test, expect } from "bun:test";
import { BLOCK_SELECTOR, docBlocks, resolveAnchors } from "./doc-notes.js";

// A tiny element tree: enough of the DOM for selector matching, text nodes and
// document order, so the block rules run without a layout engine.
const matchesSel = (node, selector) => selector.split(",").map((s) => s.trim()).some((s) => {
  if (s.startsWith(".")) return (node.attrs.class || "").split(" ").includes(s.slice(1));
  const role = s.match(/^\[role="(.+)"\]$/);
  if (role) return node.attrs.role === role[1];
  return node.tagName.toLowerCase() === s;
});
function el(tag, attrs, ...kids) {
  const node = {
    nodeType: 1, tagName: tag.toUpperCase(), attrs: attrs || {}, parent: null,
    childNodes: kids.map((k) => (typeof k === "string" ? { nodeType: 3, data: k } : k)),
    matches: (sel) => matchesSel(node, sel),
    closest: (sel) => { for (let n = node; n; n = n.parent) if (matchesSel(n, sel)) return n; return null; },
    get textContent() { return node.childNodes.map((c) => c.data ?? c.textContent).join(""); },
    querySelectorAll() {
      const out = [];
      const walk = (n) => n.childNodes.forEach((c) => { if (c.nodeType === 1) { out.push(c); walk(c); } });
      walk(node);
      return out;
    },
  };
  node.childNodes.forEach((c) => { if (c.nodeType === 1) c.parent = node; });
  return node;
}
// What the reader quotes: a container is its own text, not its child blocks'.
const ownText = (n) => n.childNodes.map((c) => (c.nodeType === 3 ? c.data
  : (c.matches(BLOCK_SELECTOR) ? "" : ownText(c)))).join("");
const quoteOf = (b) => (b.tagName === "TR" ? b.childNodes.map((c) => c.textContent).join(" | ") : b.matches(BLOCK_SELECTOR) ? b.textContent : ownText(b));
const texts = (blocks) => blocks.map((b) => quoteOf(b).replace(/\s+/g, " ").trim());

const doc = () => el("body", {},
  el("p", {}, "Intro"),
  el("div", { class: "callout" }, el("strong", {}, "Decision: "), " we ship on Friday."),
  el("div", {}, el("p", {}, "First"), el("p", {}, "Second"), "Loose note"),
  el("table", {}, el("tr", {}, el("td", {}, "Cell A"), el("td", {}, "Cell B"))),
  el("details", {}, el("summary", {}, "More"), "Hidden body"),
  el("span", { style: "display:block" }, "Block span"),
  el("span", {}, "inline only"),
  el("div", {}, el("button", {}, "Go"), el("a", {}, "link")),
);

const display = (e) => (e.attrs.style === "display:block" ? "block" : "inline");

test("a callout div with a strong and loose text is one block (red before the fix)", () => {
  const blocks = docBlocks(doc(), display);
  expect(texts(blocks)).toContain("Decision: we ship on Friday.");
});

test("a div with paragraphs keeps its paragraphs and gains its loose text", () => {
  const blocks = docBlocks(doc(), display);
  const tags = blocks.map((b) => b.tagName);
  expect(texts(blocks).filter((t) => ["First", "Second"].includes(t))).toEqual(["First", "Second"]);
  expect(blocks.some((b) => b.tagName === "DIV" && b.childNodes.some((c) => c.data === "Loose note"))).toBe(true);
  expect(tags.filter((t) => t === "P").length).toBe(3);
});

test("table rows stay the block, details and block spans count, inline and interactive do not", () => {
  const blocks = docBlocks(doc(), display);
  expect(blocks.some((b) => b.tagName === "TR")).toBe(true);
  expect(blocks.some((b) => b.tagName === "TD")).toBe(false);
  expect(blocks.some((b) => b.tagName === "DETAILS")).toBe(true);
  expect(blocks.some((b) => b.tagName === "SUMMARY")).toBe(false);
  expect(texts(blocks)).toContain("Block span");
  expect(texts(blocks)).not.toContain("inline only");
  expect(blocks.some((b) => b.tagName === "BUTTON" || b.tagName === "A")).toBe(false);
});

test("old anchors still resolve in a document that now has more blocks", () => {
  const body = doc();
  const old = body.querySelectorAll().filter((e) => e.matches(BLOCK_SELECTOR));
  const notes = old.map((b, i) => ({ id: `n${i}`, anchor: i, quote: quoteOf(b) }));
  const now = docBlocks(body, display);
  const resolved = resolveAnchors(texts(now), notes);
  for (const [i, b] of old.entries()) expect(now[resolved.get(`n${i}`)]).toBe(b);
});
