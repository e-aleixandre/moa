import { test, expect } from "bun:test";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { SidebarFoot } from "./Sidebar.jsx";
import { AboutVersion } from "../../components/GlobalSettings/GlobalSettings.jsx";

// SidebarFoot and AboutVersion are plain function components (no hooks), so
// they are called directly and walked as vnode trees.
function walk(node, out = []) {
  if (node == null || typeof node !== "object") { if (node != null) out.push(node); return out; }
  if (Array.isArray(node)) { node.forEach((n) => walk(n, out)); return out; }
  out.push(node);
  walk(node.props?.children, out);
  return out;
}
const texts = (tree) => walk(tree).filter((n) => typeof n === "string" || typeof n === "number").join(" ");
const elements = (tree) => walk(tree).filter((n) => typeof n === "object" && n.props);
const cls = (n) => String(n.props.class || "");

const withUpdate = { current: "v0.42.0-19-gfc45cedc", latest: "v0.44.0", update_available: true };
const noUpdate = { current: "v0.44.0", latest: "v0.44.0", update_available: false };
const foot = (version) => SidebarFoot({ inboxVisible: true, onInbox() {}, onTasks() {}, onSettings() {}, version });
const gear = (tree) => elements(tree).find((n) => cls(n).includes("zl-gear"));

test("the foot holds Inbox, Tasks and Settings and never the version text", () => {
  const tree = foot(withUpdate);
  const labels = elements(tree).filter((n) => n.type === "button").map((n) => n.props["aria-label"]);
  expect(labels).toEqual(["Inbox", "Tasks", "Settings, update available: v0.44.0"]);
  const all = texts(tree);
  expect(all).not.toContain("v0.42.0");
  expect(all).not.toContain("v0.44.0");
});

test("with an update the gear wears a dot and says so in words", () => {
  const g = gear(foot(withUpdate));
  expect(g.props["aria-label"]).toBe("Settings, update available: v0.44.0");
  expect(g.props.title).toBe(g.props["aria-label"]);
  expect(elements(g).some((n) => cls(n).includes("tk-gear-dot"))).toBe(true);
});

test("without an update the gear has no dot and a plain name", () => {
  for (const v of [noUpdate, null]) {
    const g = gear(foot(v));
    expect(g.props["aria-label"]).toBe("Settings");
    expect(g.props.title).toBe("Settings");
    expect(elements(g).some((n) => cls(n).includes("tk-gear-dot"))).toBe(false);
  }
});

test("the phone drawer reuses the same foot and prints no version of its own", () => {
  const dir = dirname(fileURLToPath(import.meta.url));
  const drawer = readFileSync(join(dir, "../mobile/SessionDrawer/SessionDrawer.jsx"), "utf8");
  expect(drawer).toContain('<Sidebar\n            density="phone"');
  expect(drawer).not.toMatch(/version\.(current|latest)|version\?\.(current|latest)/);
  const sidebar = readFileSync(join(dir, "Sidebar.jsx"), "utf8");
  expect(sidebar).toContain("<SidebarFoot");
  expect(sidebar).not.toMatch(/version\??\.current/);
});

test("Settings › About prints the full version and the update link", () => {
  const tree = AboutVersion({ version: withUpdate });
  const all = texts(tree);
  expect(all).toContain("About");
  expect(all).toContain("v0.42.0-19-gfc45cedc");
  expect(all).toContain("Update available");
  const link = elements(tree).find((n) => n.type === "a");
  expect(link.props.href).toContain("github.com");
  expect(texts(link)).toContain("v0.44.0");
});

test("About without an update shows the version and no update row", () => {
  const tree = AboutVersion({ version: noUpdate });
  expect(texts(tree)).toContain("v0.44.0");
  expect(texts(tree)).not.toContain("Update available");
  expect(elements(tree).some((n) => n.type === "a")).toBe(false);
});
