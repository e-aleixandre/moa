// avatar-tones.test.jsx — the six added shapes, the tone axis and New owner's
// proposal of a face nobody has.
//
// Faces are rendered as plain vnode trees (no DOM), with the hooks stubbed the
// way ActivityLedger.test.jsx does it: spread the real module first, because
// bun's mock.module is process-wide.

import { expect, mock, test } from "bun:test";
import { readFileSync } from "node:fs";

const realHooks = await import("preact/hooks");
mock.module("preact/hooks", () => ({
  ...realHooks,
  useEffect() {},
  useRef(initial) { return { current: initial }; },
  useMemo(factory) { return factory(); },
}));

const {
  AVATAR_COLORS, AVATAR_SHAPES, AVATAR_TONES, DEFAULT_AVATAR_SHAPES, SHAPE_PATHS, EYE_CENTER,
  defaultAvatar, ownerAvatar, proposeAvatar, storedAvatar,
} = await import("./avatar-identity.js");
const { OwnerFace, SerenaEyes, faceBodyColor } = await import("./OwnerFace.jsx");

function nodes(node, out = []) {
  if (node == null || typeof node !== "object") return out;
  if (Array.isArray(node)) {
    for (const child of node) nodes(child, out);
    return out;
  }
  out.push(node);
  nodes(node.props?.children, out);
  return out;
}

const ADDED = ["flower", "ghost", "bean", "diamond", "shield", "bell"];

test("every added shape draws its own outline with its eyes inside it", () => {
  for (const shape of ADDED) {
    expect(SHAPE_PATHS[shape]).toMatch(/^M[\d.\s]/);
    expect(EYE_CENTER[shape]).toBeDefined();
    const tree = OwnerFace({ shape, color: "sky", state: "idle", size: 40, gaze: [0, 0] });
    const fill = nodes(tree).find((n) => n.props?.class === "of-fill");
    expect(fill.props.d).toBe(SHAPE_PATHS[shape]);
    // Not sent back to the circle.
    expect(fill.props.d).not.toBe(SHAPE_PATHS.circle);
    expect(tree.props.class).toContain("is-idle");
    expect(tree.props.class).toContain("of-serena");
    expect(tree.props.class).not.toContain("of-mirada");
  }
});

test("the tone sets the body, and pale draws the same eyes in ink", () => {
  for (const tone of AVATAR_TONES) {
    for (const state of ["idle", "working", "asks", "saved"]) {
      const tree = OwnerFace({ shape: "ghost", color: "peach", tone, state, size: 20, gaze: [0, 0] });
      expect(tree.props.class.split(" ")).toContain("of-serena");
      expect(tree.props.class).toContain(`is-tone-${tone}`);
      expect(tree.props.class).toContain(`is-${state}`);
      expect(tree.props.style["--of-body"]).toBe(faceBodyColor("peach", tone));
      // SerenaEyes is a pure function component: expand it to see its parts.
      const el = nodes(tree).find((n) => n.type === SerenaEyes);
      const all = nodes(SerenaEyes(el.props));
      // The same drawings in every tone: per state, arcs + dots at rest and
      // awake, two level lines when saved.
      const count = (cls) => all.filter((n) => n.props?.class === cls).length;
      expect(count("of-arc")).toBe(state === "saved" ? 0 : 2);
      expect(count("of-dot")).toBe(state === "saved" ? 0 : 2);
      expect(count("of-line")).toBe(state === "saved" ? 2 : 0);
    }
  }
  const css = readFileSync(new URL("./OwnerFace.css", import.meta.url), "utf8");
  // One variable carries the eye colour for arcs (ring and caps), dots and lines alike, and
  // pale turns it to ink.
  expect(css).toContain(".of-serena.is-tone-pale { --of-eye: var(--of-ink); }");
  for (const part of ["of-ring", "of-cap", "of-dot", "of-line"]) {
    expect(css).toMatch(new RegExp(`\\.of-serena \\.${part} \\{[^}]*var\\(--of-eye\\)`));
  }
  expect(css).toMatch(/\.is-tone-dark\.is-saved \{ opacity: 0\.8; \}/);
  // The rules the component relies on are Serena's, not a leftover face's.
  for (const rule of [
    ".of-serena .of-dot {",
    ".of-serena.is-idle .of-eyes { transform: none; }",
    ".of-serena.is-saved { opacity: 0.66; }",
    "@media (prefers-reduced-motion: reduce) {\n  .of-serena .of-head,",
  ]) expect(css).toContain(rule);
  expect(css).not.toContain("of-mirada");
});

test("deep is today's body exactly, and an absent or unknown tone is deep", () => {
  for (const { id, oklch } of AVATAR_COLORS) {
    expect(faceBodyColor(id)).toBe(`oklch(0.68 0.12 ${oklch.split(" ")[2]})`);
    expect(faceBodyColor(id, "deep")).toBe(faceBodyColor(id));
    expect(faceBodyColor(id, "neon")).toBe(faceBodyColor(id));
  }
  expect(faceBodyColor("peach", "dark")).toBe("oklch(0.6 0.146 56)");
  expect(faceBodyColor("peach", "pale")).toBe("oklch(0.84 0.102 56)");
  expect(ownerAvatar({ codebase_key: "g", avatar: { shape: "drop", color: "mauve" } }))
    .toEqual({ shape: "drop", color: "mauve", tone: "deep" });
  expect(ownerAvatar({ codebase_key: "g", avatar: { shape: "bell", color: "mauve", tone: "pale" } }).tone).toBe("pale");
  expect(ownerAvatar({ codebase_key: "g", avatar: { shape: "bell", color: "mauve", tone: "neon" } }).tone).toBe("deep");
  // Only a chosen tone is sent, so owner.json does not grow for the rest.
  expect(storedAvatar({ shape: "drop", color: "mauve", tone: "deep" })).toEqual({ shape: "drop", color: "mauve" });
  expect(storedAvatar({ shape: "drop", color: "mauve", tone: "dark" })).toEqual({ shape: "drop", color: "mauve", tone: "dark" });
});

// The seven owners the owner has today.
const TODAY = [
  { shape: "triangle", color: "sky" }, { shape: "drop", color: "mauve" }, { shape: "cloud", color: "azure" },
  { shape: "circle", color: "mint" }, { shape: "blob", color: "peach" }, { shape: "hexagon", color: "mauve" },
  { shape: "pill", color: "rose" },
];
const key = (a) => `${a.shape}:${a.color}:${a.tone || "deep"}`;

test("New owner proposes a face no owner wears, far from all of them, and stable", () => {
  const p = proposeAvatar(TODAY, "winerim-web");
  expect(TODAY.map(key)).not.toContain(key(p));
  expect(AVATAR_SHAPES).toContain(p.shape);
  expect(AVATAR_TONES).toContain(p.tone);
  // Far from everyone: a shape nobody has.
  expect(TODAY.map((a) => a.shape)).not.toContain(p.shape);
  expect(proposeAvatar(TODAY, "winerim-web")).toEqual(p);

  // Owner after owner, each proposal stays free.
  const all = [...TODAY];
  for (let i = 0; i < 40; i++) {
    const next = proposeAvatar(all, `p${i}`);
    expect(all.map(key)).not.toContain(key(next));
    all.push(next);
  }
});

test("with no owners, or every combination taken, it is today's default", () => {
  expect(proposeAvatar([], "facturas-api")).toEqual({ ...defaultAvatar("facturas-api"), tone: "deep" });
  const every = [];
  for (const shape of AVATAR_SHAPES) {
    for (const { id: color } of AVATAR_COLORS) for (const tone of AVATAR_TONES) every.push({ shape, color, tone });
  }
  expect(every.length).toBe(14 * 8 * 3);
  expect(proposeAvatar(every, "facturas-api")).toEqual({ ...defaultAvatar("facturas-api"), tone: "deep" });
  expect(DEFAULT_AVATAR_SHAPES).toContain(defaultAvatar("facturas-api").shape);
});

test("New owner seeds its picker from the proposal over the owners it has", () => {
  const src = readFileSync(new URL("./Owners.jsx", import.meta.url), "utf8");
  expect(src).toContain("proposeAvatar((ownerList || []).map(ownerAvatar), folderKey)");
  expect(src).toContain("avatar: storedAvatar(avatar)");
  expect(src).toContain("onTone={(tone) => setChosenAvatar({ ...avatar, tone })}");
});
