import { test, expect } from "bun:test";
import { ownerEditBody, sameSendTargets, sendCandidates, sendTargets } from "./owners-model.js";

const moa = { id: "own_moa", name: "moa", can_send_tasks_to: ["own_infra", "own_gone"] };
const infra = { id: "own_infra", name: "Infra" };
const web = { id: "own_web", name: "Web" };
const alpha = { id: "own_a", name: "Alpha" };
const owners = [moa, infra, web, alpha];

test("Can send tasks to lists the owners the roster still knows, once each", () => {
  expect(sendTargets(moa, owners)).toEqual([infra]);
  expect(sendTargets(moa, owners, ["own_web", "own_web", "own_moa"])).toEqual([web]);
  expect(sendTargets({ id: "x" }, owners)).toEqual([]);
});

test("+ Add offers every other owner not already there, by name", () => {
  expect(sendCandidates(moa, owners, ["own_infra"])).toEqual([alpha, web]);
  expect(sendCandidates(moa, owners, [])).toEqual([alpha, infra, web]);
});

test("the list travels only when it changed", () => {
  const avatar = { shape: "circle", color: "peach" };
  expect(ownerEditBody(moa, { name: "moa", avatar, sendTo: ["own_gone", "own_infra"] })).toEqual({ name: "moa", avatar });
  expect(ownerEditBody(moa, { name: "moa", avatar, sendTo: [] })).toEqual({ name: "moa", avatar, can_send_tasks_to: [] });
  expect(ownerEditBody(moa, { name: "moa", avatar, sendTo: ["own_infra", "own_web"] }).can_send_tasks_to).toEqual(["own_infra", "own_web"]);
  expect(sameSendTargets(["a", "b"], ["b", "a", "a"])).toBe(true);
});
