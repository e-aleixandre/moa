// status-strip-model — pure classifier that splits a session's telemetry into
// the TWO levels of the redesigned StatusStrip (TELEMETRY-SETTINGS-REDESIGN
// spec). It replaces a flat 11-pill dump with a hierarchy of
// attention:
//
//   • Level 1 (the line, always in view): pulse (activity/ctx/cost, rendered by
//     the strip itself) + the permission control + the MODES that are currently
//     active (plan/goal/tasks) + the on-extra alert.
//   • Level 2 (the Usage panel, one tap away): the full accounting — cost
//     breakdown, tokens, detailed context, and the plan 5h/weekly/extra windows.
//
// This module owns only the DECISIONS (what is level 1 vs level 2, and the
// cost severity). Rendering, popovers and gestures live in the components.
// It builds on usageForSession (the dual Anthropic/OpenAI source selector) so
// it never re-derives provider logic.

import { usageForSession, usageLevel } from "./usage-pills.js";

// activeModes derives the mode segments that only exist when they're on. A mode
// that is off produces no segment at all (house rule: a missing value hides its
// segment rather than showing an invented/off one).
function activeModes(session) {
  const s = session || {};
  const modes = {};

  if (s.goalActive) {
    modes.goal = {
      verifying: !!s.goalVerifying,
      iteration: s.goalIteration || 0,
      objective: s.goalObjective || "",
    };
  }

  const tasks = s.tasks || [];
  if (tasks.length > 0) {
    const total = tasks.length;
    const done = tasks.filter((t) => t.status === "done").length;
    modes.tasks = { done, total, complete: done === total && total > 0 };
  }

  return modes;
}

// spendLevel colors the estimated session cost by the most-used available plan
// window. Without either window there is no severity color to imply.
export function spendLevel(usage) {
  const worstPct = Math.max(usage?.fiveHour?.pct ?? -1, usage?.week?.pct ?? -1);
  if (worstPct < 0) return null;
  const level = usageLevel(worstPct);
  return level === "low" ? "normal" : level;
}

// statusStripModel(session, globalUsage) → the two-level model.
//
//   {
//     perm: { mode },                       // always present; the tappable control
//     modes: {                              // only the ones currently active
//       goal?:  { verifying, iteration, objective },
//       tasks?: { done, total, complete },
//     },
//     alerts: { onExtra: bool },            // 🔥 pay-as-you-go, only when active
//     spendLevel: 'normal'|'med'|'high'|null,
//     usage: <usageForSession shape>,       // full accounting for the Usage panel
//   }
//
// Pure: reads only `session` and `globalUsage` (the /api/usage snapshot, or null
// before the first poll).
export function statusStripModel(session, globalUsage) {
  const s = session || {};
  const usage = usageForSession(s, globalUsage);

  return {
    perm: { mode: s.permissionMode || "yolo" },
    modes: activeModes(s),
    alerts: {
      onExtra: !!usage.onOverage,
    },
    spendLevel: spendLevel(usage),
    usage,
  };
}

/* ── Priority ──────────────────────────────────────────────────────────────
   The status line sheds items low-first as the dock narrows, and everything it
   sheds is reachable in the session panel. Declaring the order here, next to
   the model both densities already share, is what stops it from drifting into
   per-element breakpoints in two different stylesheets.

   The order is the owner's: model, permissions and context never drop -- they
   are what you glance at while typing and what changes the answer. Real alarms
   outrank plain numbers because they only exist while something is wrong.
   Tokens and spend are read after the fact. Fast, goal, tasks and a healthy
   MCP go first.

   Note that MCP takes its priority from its STATE, not its type: healthy it
   drops early, unhealthy it stays. A rule keyed on the kind of datum alone
   could not express that.

   THE OWNER'S FACE IS p1, and it is the one item here that is not a reading.
   It arrived with no case of its own and fell into the p4 default, which the
   sheet hides below 640px -- that is every phone dock and most grid panes, so
   the mark existed in the DOM and nobody on a phone had ever seen it. It
   belongs at the top instead: "whose session is this, and where do I go to
   ask it" is not read after the fact, it is what tells you where you are, and
   it is on a phone -- one session filling the screen, no column beside it to
   give context -- that the question is hardest to answer. The line can afford
   it: a 14px mark with no word costs ~24px, against the ~90px of the model
   pill that never drops. */
export const STATUS_PRIORITY = { p1: 1, p2: 2, p3: 3, p4: 4 };

export function statusItemPriority(kind, state) {
  switch (kind) {
    case "model":
    case "perm":
    case "context":
    case "owner":
      return "p1";
    case "mcp":
      return state === "unhealthy" ? "p2" : "p4";
    case "extra":
      return "p2";
    case "tokens":
    case "spend":
      return "p3";
    default:
      return "p4";
  }
}
