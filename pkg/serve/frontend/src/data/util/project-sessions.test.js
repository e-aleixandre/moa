import { test, expect } from "bun:test";
import {
  defaultProjectCollapsed, filterProjectSections, groupProjectSessions,
  hiddenProjectSavedCount, projectCollapsed, projectSectionOrder, pruneDrawerCollapsed,
  previewSavedSessions, SAVED_PREVIEW_LIMIT, sessionSearchMatch, visibleProjectSessions,
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

test("session search matches words in title, full cwd, and multiple sections", () => {
  const sections = groupProjectSessions([
    session("title", "/work/a", "idle", 1, { title: "alpha task" }),
    session("long", "/home/me/a-very-long-directory-name/needle-is-here/project", "idle", 2, { path: "…/project", title: "other" }),
    session("not-fuzzy", "/work/b", "idle", 3, { title: "fXoXo" }),
    session("other", "/work/c", "idle", 4, { title: "alpha second" }),
  ]);
  expect(filterProjectSections(sections, "needle-is-here")[0].sessions.map((s) => s.id)).toEqual(["long"]);
  expect(filterProjectSections(sections, "foo")).toEqual([]);
  expect(filterProjectSections(sections, "alpha")).toHaveLength(2);
  expect(filterProjectSections(sections, "   ")).toBe(sections);
});

test("session search avoids subsequence noise in representative long titles", () => {
  const sections = groupProjectSessions([
    session("iread-1", "/work/iread", "saved", 1, { title: "iREAD audio notes" }),
    session("iread-2", "/work/other", "saved", 2, { title: "Fix iRead transcript" }),
    session("iread-3", "/work/other", "saved", 3, { title: "iREAD review" }),
    session("pwa-1", "/work/other", "saved", 4, { title: "PWA input zoom" }),
    session("pwa-2", "/work/other", "saved", 5, { title: "Ship the pwa" }),
    session("docs-1", "/work/other", "saved", 6, { title: "Moa Docs Web" }),
    session("docs-2", "/work/other", "saved", 7, { title: "Documentation docs drift" }),
    session("memory", "/work/other", "saved", 8, { title: "Sessions and Memory" }),
  ]);
  const count = (query) => filterProjectSections(sections, query).flatMap((section) => section.sessions).length;
  expect(count("iread")).toBe(3);
  expect(count("pwa")).toBe(2);
  expect(count("docs")).toBe(2);
  expect(count("memoria")).toBe(0);
  expect(sessionSearchMatch("moa docs", { title: "Moa Docs Web" })).toBe(true);
  expect(sessionSearchMatch("sesion", { title: "Sesión guardada" })).toBe(true);
  // The palette has always matched sessions by model name; keep that reachable.
  expect(sessionSearchMatch("opus", { title: "Untitled", model: "Claude Opus 5" })).toBe(true);
});

test("project previews never hide open sessions and reveal every saved row on search or expansion", () => {
  const [section] = groupProjectSessions([
    ...Array.from({ length: 7 }, (_, i) => session(`open-${i}`, "/work/a", "idle", 100 - i)),
    ...Array.from({ length: 8 }, (_, i) => session(`saved-${i}`, "/work/a", "saved", 100 - i)),
  ]);
  expect(visibleProjectSessions(section).filter((row) => row.state !== "saved")).toHaveLength(7);
  expect(visibleProjectSessions(section)).toHaveLength(12);
  expect(hiddenProjectSavedCount(section)).toBe(3);
  expect(visibleProjectSessions(section, false, true)).toHaveLength(15);
  expect(visibleProjectSessions(section, true)).toHaveLength(15);
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

test("the recency view previews a long saved list and reports the remainder", () => {
  const sessions = Array.from({ length: 199 }, (_, i) => ({ id: `s${i}`, state: "saved" }));
  const preview = previewSavedSessions(sessions);
  expect(preview.visible).toHaveLength(SAVED_PREVIEW_LIMIT);
  expect(preview.hidden).toBe(199 - SAVED_PREVIEW_LIMIT);
  // The preview is the newest slice, not an arbitrary one.
  expect(preview.visible[0]).toBe(sessions[0]);
});

test("a saved list that already fits is not capped, and asking for all reveals them", () => {
  const sessions = Array.from({ length: SAVED_PREVIEW_LIMIT }, (_, i) => ({ id: `s${i}`, state: "saved" }));
  expect(previewSavedSessions(sessions)).toEqual({ visible: sessions, hidden: 0 });

  const many = Array.from({ length: 60 }, (_, i) => ({ id: `s${i}`, state: "saved" }));
  expect(previewSavedSessions(many, { expanded: true }).visible).toHaveLength(60);
  // Searching must reach the whole roster: a hidden match reads as data loss.
  expect(previewSavedSessions(many, { searching: true }).visible).toHaveLength(60);
});
