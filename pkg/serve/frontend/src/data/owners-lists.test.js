import { expect, test } from "bun:test";
import { isOrdinarySession, ordinarySessions, SIDEBAR_MODES } from "./util/project-sessions.js";
import { spineSessions } from "../layout/Sidebar/sessions.js";
import { drawerSessions } from "../layout/mobile/MobileConversationScreen/chrome.js";
import { aggregateAttention } from "../layout/mobile/MobileConversationScreen/attention-model.js";

// The roster holds an owner's conversation — it is opened, streamed and read
// like any session — and every list of SESSIONS filters it out. These tests
// are the ratchet on that: a projection that forgets the filter puts an owner
// among the sessions it is responsible for.

const owner = { id: "o", title: "Winerim", state: "idle", kind: "owner", cwd: "/p", updated: 400 };
const child = { id: "c", title: "imports", state: "permission", ownerId: "own_1", cwd: "/p", updated: 300 };

test("an owner conversation is not an ordinary session", () => {
  expect(isOrdinarySession(owner)).toBe(false);
  expect(isOrdinarySession(child)).toBe(true);
  expect(ordinarySessions([owner, child]).map((s) => s.id)).toEqual(["c"]);
});

test("neither session list shows an owner", () => {
  const sessions = { o: owner, c: child };
  const spine = spineSessions(sessions);
  expect([...spine.active, ...spine.saved].map((s) => s.id)).toEqual(["c"]);
  const drawer = drawerSessions(sessions, null);
  expect([...drawer.newResults, ...drawer.active, ...drawer.saved].map((s) => s.id)).toEqual(["c"]);
});

test("an owner never contributes to the phone's attention badge", () => {
  // An owner asking is shown on its own row in Owners mode, not as "another
  // session needs you" on the door to the session list.
  const asking = { ...owner, state: "permission" };
  expect(aggregateAttention({ o: asking }, null).urgent).toBe(0);
  expect(aggregateAttention({ c: child }, null).urgent).toBe(1);
});

test("the sidebar has exactly three modes, and Owners is one of them", () => {
  expect(SIDEBAR_MODES).toEqual(["recent", "project", "owners"]);
});
