import { test, expect } from "bun:test";
import { Toast } from "./Toast.jsx";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

function textContent(node) {
  if (node == null || node === false) return "";
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (Array.isArray(node)) return node.map(textContent).join(" ");
  return textContent(node.props?.children);
}

function find(node, cls) {
  if (node == null || typeof node !== "object") return null;
  if (Array.isArray(node)) {
    for (const child of node) {
      const hit = find(child, cls);
      if (hit) return hit;
    }
    return null;
  }
  if (String(node.props?.class || "").split(" ").includes(cls)) return node;
  return find(node.props?.children, cls);
}

test("each tone says its state in words", () => {
  const words = { info: "Note", success: "Finished", error: "Failed", attention: "Needs you" };
  for (const [tone, word] of Object.entries(words)) {
    const toast = Toast({ tone, title: "t" });
    expect(textContent(find(toast, "toast-word"))).toBe(word);
    expect(toast.props.class).toContain(`is-${tone}`);
  }
});

test("an unknown tone reads as a note", () => {
  const toast = Toast({ tone: "bogus", title: "t" });
  expect(textContent(find(toast, "toast-word"))).toBe("Note");
  expect(toast.props.role).toBe("status");
});

test("the title shares the state's line; detail and action sit under it", () => {
  const toast = Toast({ tone: "error", title: "Could not open", detail: "Gone.", action: { label: "Retry" } });
  expect(textContent(find(find(toast, "toast-row"), "toast-title"))).toBe("Could not open");
  const sub = find(toast, "toast-sub");
  expect(textContent(find(sub, "toast-detail"))).toBe("Gone.");
  expect(textContent(find(sub, "toast-act"))).toBe("Retry");
  expect(find(Toast({ title: "bare" }), "toast-sub")).toBeNull();
});

test("scrollDetail makes only the detail a keyboard-reachable scroll region", () => {
  const toast = Toast({ title: "t", detail: "long", scrollDetail: true });
  expect(toast.props.class).toContain("is-scrollable");
  expect(toast.props.scrollDetail).toBeUndefined();
  expect(find(toast, "toast-detail").props.tabIndex).toBe(0);

  const plain = Toast({ title: "t", detail: "long" });
  expect(plain.props.class).not.toContain("is-scrollable");
  expect(find(plain, "toast-detail").props.tabIndex).toBeUndefined();
});

test("the scroll bound is scoped to .is-scrollable and ordinary toasts stay unbounded", () => {
  const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "Toast.css"), "utf8");
  const rule = css.match(/\.toast\.is-scrollable \.toast-detail\s*\{([^}]*)\}/);
  expect(rule).not.toBeNull();
  expect(rule[1]).toMatch(/max-height:\s*min\(60vh,\s*600px\)/);
  expect(rule[1]).toMatch(/overflow-y:\s*auto/);
  const base = css.match(/\n\.toast-detail\s*\{([^}]*)\}/)[1];
  expect(base).not.toMatch(/max-height|overflow-y/);
});
