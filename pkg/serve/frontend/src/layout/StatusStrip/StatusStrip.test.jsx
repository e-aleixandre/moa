import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { tokenFlowVariant } from "./status-strip-view-model.js";
import { StatusStrip } from "./StatusStrip.jsx";

const here = dirname(fileURLToPath(import.meta.url));
const lab = readFileSync(join(here, "../../catalog/zones-lab.jsx"), "utf8");

// StatusStrip is a plain function component (no hooks), so it can be called
// directly and walked as a vnode tree without a DOM or a hook shim.
function descendants(node, nodes = []) {
  if (node == null || typeof node !== "object") return nodes;
  if (Array.isArray(node)) {
    for (const child of node) descendants(child, nodes);
    return nodes;
  }
  nodes.push(node);
  descendants(node.props?.children, nodes);
  return nodes;
}

function permButton(session) {
  const nodes = descendants(StatusStrip({
    session,
    onPerm: () => {},
    permOpen: false,
  }));
  return nodes.find((node) => typeof node.props?.class === "string" && node.props.class.includes("zl-st-perm") && node.type === "button");
}

test("the permission control stays open to change mid-run: config-while-running dropped the busy lock", () => {
  const running = { permissionMode: "ask", state: "running" };
  const button = permButton(running);
  expect(button).toBeTruthy();
  expect(button.props.disabled).toBeFalsy();
  expect(button.props["aria-label"]).toBe("Permission mode: ask");
  expect(button.props["aria-label"]).not.toContain("locked");
});

test("the permission control is equally open while a permission request is pending", () => {
  const pending = { permissionMode: "auto", state: "permission" };
  const button = permButton(pending);
  expect(button.props.disabled).toBeFalsy();
});

test("compact status strips omit the token unit through TokenFlow's compact variant", () => {
  expect(tokenFlowVariant(true)).toBe("compact");
});

test("full status strips retain TokenFlow's strip variant", () => {
  expect(tokenFlowVariant(false)).toBe("strip");
});

test("the catalogue draws production's StatusStrip, not a private line", () => {
  // The move is only done when there is one implementation. A leftover
  // StatusLine/ThinkMeter in the lab is how the previous commit claimed this
  // and then didn't. Asserted on the source so this file never loads
  // StatusStrip.jsx (and with it the components barrel) into the test process.
  expect(lab).toMatch(/import \{ StatusStrip \} from ["'].*StatusStrip\/StatusStrip\.jsx["']/);
  expect(lab).not.toMatch(/function ThinkMeter\s*\(/);
  expect(lab).toMatch(/<StatusStrip[\s\S]*\/>/);
});
