import { expect, test } from "bun:test";
import { byProject, recurrenceWords } from "./scheduled-visible.js";

// Reviewer's reproductions of 50c0ce71 (tmp/scheduled-visible/REVIEW-50c0ce71.md, P2-2 and P2-3).
const ny = "America/New_York";
const rep = (rule, next, tz = "UTC") => ({ id: 1, status: "pending", place: "you", schedule_state: "scheduled", tz, when: { kind: "repeat", rule }, next: Date.parse(next) });

test("a weekly run says the viewer's day together with the viewer's time", () => {
  expect(recurrenceWords(rep({ freq: "weekly", dow: 1, h: 0, mi: 30 }, "2026-10-26T00:30:00Z"), ny)).toBe("Every Sunday at 20:30");
  expect(recurrenceWords(rep({ freq: "weekly", dow: 1, h: 9, mi: 0 }, "2026-10-26T09:00:00Z"), "Europe/Madrid")).toBe("Every Monday at 10:00");
});

test("weekdays that land on other days locally name those days", () => {
  expect(recurrenceWords(rep({ freq: "weekdays", h: 0, mi: 30 }, "2026-10-26T00:30:00Z"), ny)).toBe("Sunday to Thursday at 20:30");
  expect(recurrenceWords(rep({ freq: "weekdays", h: 9, mi: 0 }, "2026-10-26T09:00:00Z"), "Europe/Madrid")).toBe("Weekdays at 10:00");
});

test("a monthly day never pairs the template's day with the viewer's time", () => {
  const shifted = recurrenceWords(rep({ freq: "monthly", dom: 1, h: 0, mi: 30 }, "2026-11-01T00:30:00Z"), ny);
  expect(shifted).not.toBe("Every month on day 1 at 20:30");
  expect(shifted).toBe("Every month on day 1 at 00:30 UTC");
  expect(recurrenceWords(rep({ freq: "monthly", dom: 15, h: 9, mi: 0 }, "2026-11-15T09:00:00Z"), "Europe/Madrid")).toBe("Every month on day 15 at 10:00");
});

test("projects with the same folder name stay apart and are told apart", () => {
  const base = rep({ freq: "daily", h: 9, mi: 0 }, "2026-10-26T09:00:00Z");
  const groups = byProject([
    { ...base, project_key: "customer-a", project_cwd: "/projects/customer-a/api" },
    { ...base, id: 2, project_key: "customer-b", project_cwd: "/projects/customer-b/api" },
    { ...base, id: 3, project_key: "moa", project_cwd: "/dev/moa" },
  ]);
  expect(groups.map((g) => [g.name, g.rows.map((t) => t.project_key)])).toEqual([
    ["customer-a/api", ["customer-a"]], ["customer-b/api", ["customer-b"]], ["moa", ["moa"]],
  ]);
});
