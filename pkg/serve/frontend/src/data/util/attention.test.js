import { describe, expect, it } from "bun:test";
import { attentionKind, partitionByAttention } from "./project-sessions.js";

const S = (over) => ({ id: "s", state: "idle", updated: 0, ...over });

describe("attentionKind", () => {
  it("counts a waiting permission, an error and an unread answer", () => {
    expect(attentionKind(S({ state: "permission" }))).toBe("permission");
    expect(attentionKind(S({ pendingAsk: true }))).toBe("permission");
    expect(attentionKind(S({ state: "error" }))).toBe("error");
    expect(attentionKind(S({ unseen: true }))).toBe("unseen");
  });

  it("leaves alone what needs nothing", () => {
    expect(attentionKind(S({ state: "running" }))).toBeNull();
    expect(attentionKind(S({ state: "idle" }))).toBeNull();
    expect(attentionKind(null)).toBeNull();
  });

  it("ignores unseen on a saved session: parked on purpose, asking nothing", () => {
    expect(attentionKind(S({ state: "saved", unseen: true }))).toBeNull();
    expect(attentionKind(S({ saved: true, unseen: true }))).toBeNull();
  });

  it("still reports a saved session that errored", () => {
    expect(attentionKind(S({ state: "error", saved: true }))).toBe("error");
  });
});

describe("partitionByAttention", () => {
  it("orders blocked, then broken, then unread", () => {
    const unseen = S({ id: "u", unseen: true });
    const error = S({ id: "e", state: "error" });
    const perm = S({ id: "p", state: "permission" });
    const { needs } = partitionByAttention([unseen, error, perm]);
    expect(needs.map((s) => s.id)).toEqual(["p", "e", "u"]);
  });

  it("breaks ties by recency, newest first", () => {
    const old = S({ id: "old", state: "error", updated: 10 });
    const fresh = S({ id: "fresh", state: "error", updated: 99 });
    const { needs } = partitionByAttention([old, fresh]);
    expect(needs.map((s) => s.id)).toEqual(["fresh", "old"]);
  });

  it("keeps the rest in the order it was given", () => {
    const a = S({ id: "a", state: "running" });
    const b = S({ id: "b" });
    const { needs, rest } = partitionByAttention([a, b]);
    expect(needs).toEqual([]);
    expect(rest.map((s) => s.id)).toEqual(["a", "b"]);
  });

  it("survives an empty list", () => {
    expect(partitionByAttention()).toEqual({ needs: [], rest: [] });
  });
});
