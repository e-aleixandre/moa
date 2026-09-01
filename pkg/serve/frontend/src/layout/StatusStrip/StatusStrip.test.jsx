import { expect, test } from "bun:test";
import { StatusStrip } from "./StatusStrip.jsx";

function findTokenFlow(node) {
  if (!node || typeof node !== "object") return null;
  if (node.type?.name === "TokenFlow") return node;
  const children = node.props?.children;
  for (const child of Array.isArray(children) ? children : [children]) {
    const found = findTokenFlow(child);
    if (found) return found;
  }
  return null;
}

test("compact status strips omit the token unit through TokenFlow's compact variant", () => {
  const strip = StatusStrip({
    compact: true,
    tokensUp: 1200,
    tokensDown: 800,
    session: {},
    showTokens: true,
  });
  expect(findTokenFlow(strip)?.props.variant).toBe("compact");
});

test("full status strips retain TokenFlow's strip variant", () => {
  const strip = StatusStrip({
    tokensUp: 1200,
    tokensDown: 800,
    session: {},
    showTokens: true,
  });
  expect(findTokenFlow(strip)?.props.variant).toBe("strip");
});
