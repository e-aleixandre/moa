import { expect, test } from "bun:test";
import {
  attentionKind, isOrdinarySession, ordinarySessions, partitionByAttention, SIDEBAR_MODES,
} from "./util/project-sessions.js";
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

test("the segmented has exactly two positions: they are ORDERS, not lists", () => {
  // Owners is a SECTION of the Recent list, not a third ordering of the
  // sessions. A third stop here would be the control answering two questions.
  expect(SIDEBAR_MODES).toEqual(["recent", "project"]);
});

test("an owner never rises into Needs attention", () => {
  // Its state is painted on its own row instead: an owner is standing, and a
  // permanent row that moves between sections is one you have to find again.
  const asking = { ...owner, state: "permission" };
  const erroring = { ...owner, id: "o2", state: "error" };
  const unread = { ...owner, id: "o3", state: "idle", unseen: true };
  expect(attentionKind(asking)).toBe(null);
  expect(attentionKind(erroring)).toBe(null);
  expect(attentionKind(unread)).toBe(null);
  expect(attentionKind(child)).toBe("permission");

  const split = partitionByAttention([asking, erroring, unread, child]);
  expect(split.needs.map((s) => s.id)).toEqual(["c"]);
  expect(split.rest.map((s) => s.id)).toEqual(["o", "o2", "o3"]);
});
