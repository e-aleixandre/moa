// status-strip-model — pure classifier that splits a session's telemetry into
// the TWO levels of the redesigned StatusStrip (TELEMETRY-SETTINGS-REDESIGN
// spec). It replaces the flat 11-pill dump ported in 5O with a hierarchy of
// attention:
//
//   • Level 1 (the line, always in view): pulse (activity/ctx/cost, rendered by
//     the strip itself) + the permission control + the MODES that are currently
//     active (plan/goal/tasks) + ALERTS/PROMOTIONS (on-extra, and 5h/wk meters
//     that have climbed to the promotion threshold — the "about to bite you"
//     valve).
//   • Level 2 (the Usage panel, one tap away): the full accounting — cost
//     breakdown, tokens, detailed context, and the plan 5h/weekly/extra windows.
//
// This module owns only the DECISIONS (what is level 1 vs level 2, and which
// meters promote). Rendering, popovers and gestures live in the components.
// It builds on usageForSession (the dual Anthropic/OpenAI source selector) so
// it never re-derives provider logic.

import { usageForSession, usageLevel } from "./usage-pills.js";

// Plan meters auto-promote to the line at 80% — the same threshold usageLevel
// already flips to 'high' (red/peach). Below it the accounting stays in the
// panel; at/above it a colored chip surfaces on the line, because that is the
// only moment the number is actually actionable (a rate-limit is imminent).
export const PROMOTE_THRESHOLD = 80;

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

// promotedMeters returns the plan windows (5h / week) that have reached the
// promotion threshold, as compact chips for the line. Each carries its severity
// level (always 'high' here, but computed so the color stays sourced from the
// single usageLevel authority) and the reset info for the tooltip.
function promotedMeters(usage, threshold) {
  const out = [];
  const push = (kind, label, m) => {
    if (m && m.pct >= threshold) {
      out.push({ kind, label, pct: m.pct, level: usageLevel(m.pct), resetsAt: m.resetsAt });
    }
  };
  push("5h", "Session (5h)", usage.fiveHour);
  push("wk", "Week", usage.week);
  return out;
}

// statusStripModel(session, globalUsage, opts) → the two-level model.
//
//   {
//     perm: { mode },                       // always present; the tappable control
//     modes: {                              // only the ones currently active
//       planMode?: string,
//       goal?:  { verifying, iteration, objective },
//       tasks?: { done, total, complete },
//     },
//     alerts: {
//       onExtra: bool,                      // 🔥 pay-as-you-go, only when active
//       promoted: [{ kind, label, pct, level, resetsAt }],  // 5h/wk ≥ threshold
//     },
//     usage: <usageForSession shape>,       // full accounting for the Usage panel
//   }
//
// Pure: reads only `session` and `globalUsage` (the /api/usage snapshot, or null
// before the first poll). `opts.promoteThreshold` overrides the default 80.
export function statusStripModel(session, globalUsage, opts) {
  const s = session || {};
  const threshold = opts && Number.isFinite(opts.promoteThreshold)
    ? opts.promoteThreshold
    : PROMOTE_THRESHOLD;

  const usage = usageForSession(s, globalUsage);

  return {
    perm: { mode: s.permissionMode || "yolo" },
    modes: activeModes(s),
    alerts: {
      onExtra: !!usage.onOverage,
      promoted: promotedMeters(usage, threshold),
    },
    usage,
  };
}
