import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";

const read = (rel) => readFileSync(new URL(rel, import.meta.url), "utf8");

// The header's right slot is this conversation's For you; the + is gone.
test("the mobile header has For you of the conversation, always, and no +", () => {
  const chrome = read("../../layout/mobile/MobileChrome/MobileChrome.jsx");
  expect(chrome).not.toContain('aria-label="New session"');
  expect(chrome).not.toContain("onNew");
  expect(chrome).toContain("onForYou && (");
  expect(chrome).not.toContain("forYouCount > 0 || forYouNews > 0");
  const screen = read("../../layout/mobile/MobileConversationScreen/MobileConversationScreen.jsx");
  expect(screen).toContain("useSessionForYou(chrome.activeId)");
  expect(screen).not.toContain("useStore(forYouCount)");
});

test("the desktop head carries the same button, before the other actions", () => {
  const head = read("../../layout/ChatHead/ChatHead.jsx");
  expect(head).toMatch(/\{forYou && \([\s\S]*SessionForYouButton[\s\S]*\{headExtra\}/);
  expect(read("../../layout/ConversationScreen/ConversationScreen.jsx")).toContain("useSessionForYou(session?.id)");
});

test("the sheet filters the shared list instead of copying it", () => {
  const src = read("./ForYou.jsx");
  expect(src).toContain("export function SessionForYouList");
  expect(src).toContain("scope.has(it.session_id)");
  expect(src).toContain("scope.has(n.session_id)");
});

test("the general For you stays in the sidebar and the drawer", () => {
  expect(read("../../layout/Sidebar/Sidebar.jsx")).toContain("onForYou");
  expect(read("../../layout/mobile/MobileConversationScreen/MobileConversationScreen.jsx")).toContain("onForYou={onForYouFromDrawer}");
});

test("the amber, the violet dot and the quiet state are styled", () => {
  const css = read("../../layout/mobile/MobileChrome/MobileChrome.css");
  expect(css).toContain(".zl-cap-foryou-dot");
  expect(css).toMatch(/\.zl-cap-foryou:not\(:has\(\.zl-cap-foryou-n\)\)\s*\{\s*color:\s*var\(--zl-t2\)/);
  expect(read("./ForYou.css")).toContain(".zl-desk-act.fy-hd.has-items");
});
