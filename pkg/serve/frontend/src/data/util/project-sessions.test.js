import { test, expect } from "bun:test";
import {
  defaultProjectCollapsed, filterProjectSections, groupProjectSessions,
  projectCollapsed, projectSectionOrder, pruneDrawerCollapsed,
} from "./project-sessions.js";

const session = (id, cwd, state, updated, extra = {}) => ({ id, cwd, state, updated, title: id, ...extra });

test("No project is last regardless of where it appears in the input", () => {
  for (const position of [0, 1, 2]) {
    const input = [session("a", "/work/a", "idle", 1), session("b", "/work/b", "idle", 2)];
    input.splice(position, 0, session("none", undefined, "saved", 99));
    expect(groupProjectSessions(input).at(-1).key).toBe("");
  }
  expect(projectSectionOrder({ key: "", updated: 99 }, { key: "/work/a", updated: 1 })).toBeGreaterThan(0);
});

test("Spine saved projections use saved:true even without state", () => {
  const [group] = groupProjectSessions([session("open", "/work/a", "idle", 2), { id: "saved", cwd: "/work/a", saved: true, updated: 1, title: "saved" }]);
  expect(group.sessions.map((s) => s.id)).toEqual(["open", "saved"]);
  expect(group).toMatchObject({ openCount: 1, savedCount: 1 });
});

test("orders active, saved, and attention rows by descending recency within each class", () => {
  const [group] = groupProjectSessions([
    session("active-old", "/work/a", "idle", 10), session("active-new", "/work/a", "running", 20),
    session("saved-old", "/work/a", "saved", 30), session("saved-new", "/work/a", "saved", 40),
    session("blocked-old", "/work/a", "error", 50), session("blocked-new", "/work/a", "permission", 60),
  ]);
  expect(group.sessions.map((s) => s.id)).toEqual(["blocked-new", "blocked-old", "active-new", "active-old", "saved-new", "saved-old"]);
});

test("uses the newest session in a project, not its first row, to order projects", () => {
  const groups = groupProjectSessions([
    session("a-old", "/work/a", "idle", 1), session("a-new", "/work/a", "idle", 100), session("b", "/work/b", "idle", 90),
  ]);
  expect(groups.map((group) => group.key)).toEqual(["/work/a", "/work/b"]);
  expect(groups[0].updated).toBe(100);
});

test("permission takes precedence over error for the project dot and counter", () => {
  const [group] = groupProjectSessions([
    session("permission", "/work/a", "permission", 2), session("error-one", "/work/a", "error", 3), session("error-two", "/work/a", "error", 4),
  ]);
  // A permission prompt is actionable now, so it wins over errors in a mixed project.
  expect(group).toMatchObject({ attention: "permission", attentionCount: 1 });
});

test("canonicalizes empty, undefined, root, and repeated trailing slashes", () => {
  const groups = groupProjectSessions([
    session("empty", "", "saved", 1), session("missing", undefined, "saved", 2), session("root", "/", "idle", 3), session("slashes", "/work/a///", "idle", 4),
  ]);
  expect(groups.map((group) => group.key)).toEqual(["/work/a", "/", ""]);
  expect(groups.at(-1).sessions.map((s) => s.id)).toEqual(["missing", "empty"]);
});

test("search matches title, full cwd, non-consecutive fuzzy characters, and multiple sections", () => {
  const sections = groupProjectSessions([
    session("title", "/work/a", "idle", 1, { title: "alpha task" }),
    session("long", "/home/me/a-very-long-directory-name/needle-is-here/project", "idle", 2, { path: "…/project", title: "other" }),
    session("fuzzy", "/work/b", "idle", 3, { title: "fXoXo" }),
    session("other", "/work/c", "idle", 4, { title: "alpha second" }),
  ]);
  expect(filterProjectSections(sections, "needle-is-here")[0].sessions.map((s) => s.id)).toEqual(["long"]);
  expect(filterProjectSections(sections, "foo")[0].sessions.map((s) => s.id)).toEqual(["fuzzy"]);
  expect(filterProjectSections(sections, "alpha")).toHaveLength(2);
  expect(filterProjectSections(sections, "   ")).toBe(sections);
});

test("accordion integration preserves the user fold across search and restore", () => {
  const sections = groupProjectSessions([session("open", "/work/open", "idle", 2), session("saved", "/work/saved", "saved", 1)]);
  const [open, saved] = sections;
  const collapsed = { [open.key]: true, [saved.key]: false };
  expect(defaultProjectCollapsed(open)).toBe(false);
  expect(defaultProjectCollapsed(saved)).toBe(true);
  expect(projectCollapsed(open, collapsed, false)).toBe(true);
  expect(projectCollapsed(saved, collapsed, false)).toBe(false);
  expect(projectCollapsed(open, collapsed, true)).toBe(false);
  expect(projectCollapsed(open, collapsed, false)).toBe(true);
  expect(collapsed).toEqual({ [open.key]: true, [saved.key]: false });
});

test("prunes collapsed keys only when their project has no sessions", () => {
  expect(pruneDrawerCollapsed({ "/work/a": true, "/gone": false }, [session("a", "/work/a/", "idle", 1)])).toEqual({ "/work/a": true });
});
