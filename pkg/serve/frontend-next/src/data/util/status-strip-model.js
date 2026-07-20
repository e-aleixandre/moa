// status-strip-model — pure classifier that splits a session's telemetry into
// the TWO levels of the redesigned StatusStrip (TELEMETRY-SETTINGS-REDESIGN
// spec). It replaces the flat 11-pill dump ported in 5O with a hierarchy of
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

  const planMode = s.planMode;
  if (planMode && planMode !== "off") modes.planMode = planMode;

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
//       planMode?: string,
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
