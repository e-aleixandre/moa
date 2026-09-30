import { test, expect } from "bun:test";
import { moveIndex, moveSearch, projectLabels, RECENT_MAX, SEARCH_MAX } from "./tasks-move.js";

const now = 1_000_000_000_000;
const H = 3600000;
const projects = [
  { key: "moa", cwd: "/d/moa/main", cwds: ["/d/moa/main", "/d/moa/fix-x"] },
  { key: "win", cwd: "/d/winerim-backend" },
  { key: "win-main", cwd: "/d/winerim-backend/main" },
];
const owners = [{ id: "o1", name: "Winerim", session_id: "own", session_state: "saved" }];

function sessions() {
  const out = {
    own: { id: "own", title: "Winerim", state: "saved", cwd: "/d/winerim-backend", updated: now },
    w1: { id: "w1", title: "Fix the tariffs", state: "running", cwd: "/d/moa/fix-x", updated: now - 5 * H },
    w2: { id: "w2", title: "Stock report", state: "saved", cwd: "/d/winerim-backend", updated: now - 2 * H },
  };
  for (let i = 0; i < 300; i++) out[`s${i}`] = { id: `s${i}`, title: `Old session ${i}`, state: "saved", cwd: "/d/moa/main", updated: now - (48 + i) * H };
  return out;
}

test("owners come first as owners, never again as sessions", () => {
  const idx = moveIndex({ sessions: sessions(), owners, projects, now });
  expect(idx.owners.map((o) => o.name)).toEqual(["Winerim"]);
  expect(idx.sessions.some((s) => s.id === "own")).toBe(false);
  expect(idx.recent.some((s) => s.id === "own")).toBe(false);
});

test("recent is short: working first, then the last day, never the old tail", () => {
  const idx = moveIndex({ sessions: sessions(), owners, projects, now });
  expect(idx.recent.length).toBeLessThanOrEqual(RECENT_MAX);
  expect(idx.recent.map((s) => s.id)).toEqual(["w1", "w2"]);
});

test("a project holds every session of its folders, worktrees included", () => {
  const idx = moveIndex({ sessions: sessions(), owners, projects, now });
  const moa = idx.projects.find((p) => p.key === "moa");
  expect(moa.sessions.length).toBe(301);
  expect(moa.sessions[0].id).toBe("w1");
  expect(moa.working).toBe(1);
});

test("the search reaches owners, backlogs and sessions, capped with a remainder", () => {
  const idx = moveIndex({ sessions: sessions(), owners, projects, now });
  const win = moveSearch(idx, "winerim");
  expect(win.owners.map((o) => o.name)).toEqual(["Winerim"]);
  expect(win.projects.map((p) => p.key).sort()).toEqual(["win", "win-main"]);
  expect(win.sessions.map((s) => s.id)).toEqual(["w2"]);
  const old = moveSearch(idx, "old session");
  expect(old.sessions.length).toBe(SEARCH_MAX);
  expect(old.more).toBe(300 - SEARCH_MAX);
  expect(moveSearch(idx, "tariffs", "moa").sessions.map((s) => s.id)).toEqual(["w1"]);
});

test("two projects with one name are told apart by their folder", () => {
  const labels = projectLabels(projects);
  expect(labels.get("moa")).toBe("moa");
  expect(labels.get("win")).toBe("winerim-backend");
  expect(labels.get("win-main")).toBe("winerim-backend · main");
});
