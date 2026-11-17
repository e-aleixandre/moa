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

const API = { generation: 1, epoch: 3, phase: "working", source: { kind: "api_backup" } };
const apiItem = (props) => descendants(StatusStrip(props)).find((node) => typeof node.props?.class === "string" && node.props.class.includes("zl-st-api"));

test("the line says api while the session runs on the API key, and only then", () => {
  const item = apiItem({ session: { providerExecution: API } });
  expect(item).toBeTruthy();
  expect(item.props.class).toContain("zl-p2");
  expect(apiItem({ session: { providerExecution: { ...API, source: { kind: "oauth" } } } })).toBeFalsy();
  // The run ended: nothing is being paid for now.
  expect(apiItem({ session: { providerExecution: { ...API, phase: "" } } })).toBeFalsy();
});

test("a running child on the API key lights the parent's line; a finished one does not", () => {
  expect(apiItem({ session: { subagents: { j: { status: "running", providerExecution: API } } } })).toBeTruthy();
  expect(apiItem({ session: { subagents: { j: { status: "completed", providerExecution: API } } } })).toBeFalsy();
});

test("a child's own strip takes its execution", () => {
  expect(apiItem({ onAPI: true })).toBeTruthy();
  expect(apiItem({ session: { providerExecution: API }, onAPI: false })).toBeFalsy();
});

const OAUTH2 = { generation: 1, epoch: 3, phase: "working", source: { kind: "oauth", account: 2, primary_generation: "g2" } };
const acctItem = (props) => descendants(StatusStrip(props)).find((node) => typeof node.props?.class === "string" && node.props.class.includes("zl-st-acct"));

test("the line names the plan account only when it is not the first", () => {
  const item = acctItem({ session: { providerExecution: OAUTH2 } });
  expect(item).toBeTruthy();
  expect(item.props.class).toContain("zl-p2");
  const name = descendants(item).find((n) => n.props?.account);
  expect(name.props.account).toEqual({ generation: "g2", position: 2 });
  expect(acctItem({ session: { providerExecution: { ...OAUTH2, source: { kind: "oauth", primary_generation: "g1" } } } })).toBeFalsy();
  expect(acctItem({ session: { providerExecution: { ...OAUTH2, phase: "" } } })).toBeFalsy();
  // A child's own strip takes its own account, including none.
  expect(acctItem({ account: { generation: "g3", position: 3 } })).toBeTruthy();
  expect(acctItem({ session: { providerExecution: OAUTH2 }, account: null })).toBeFalsy();
});

test("the account's name is cut by CSS and keeps its word down to the dock", () => {
  const css = readFileSync(join(here, "StatusStrip.css"), "utf8");
  expect(css).toMatch(/\.zl-st-acct \.acct-name \{ max-width: 160px; \}/);
  expect(css).toMatch(/\.zl-st-acct \.zl-st-word \{ display: inline-block; \}/);
  expect(css).toMatch(/@container \(max-width: 400px\) \{\s*\.zl-st-acct \.acct-name \{ max-width: 84px; \}/);
  const name = readFileSync(join(here, "../../components/AccountName/AccountName.css"), "utf8");
  expect(name).toMatch(/text-overflow: ellipsis/);
});
