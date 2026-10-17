import { expect, test } from "bun:test";
import { mentionTarget } from "./MentionedText.jsx";

const owners = [{ id: "o1", name: "Infra", session_id: "s-infra" }];

test("an owner in the roster opens its conversation; one that is gone is deleted", () => {
  expect(mentionTarget({ type: "owner", id: "o1" }, owners, new Set())).toMatchObject({ open: "s-infra" });
  expect(mentionTarget({ type: "owner", id: "o9" }, owners, new Set())).toBeNull();
});

test("a session in the store opens itself; one that is not there is deleted", () => {
  expect(mentionTarget({ type: "session", id: "s1" }, owners, new Set(["s1"]))).toEqual({ open: "s1" });
  expect(mentionTarget({ type: "session", id: "s2" }, owners, new Set(["s1"]))).toBeNull();
});
