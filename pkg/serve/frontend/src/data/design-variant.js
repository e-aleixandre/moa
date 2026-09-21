// design-variant — LABORATORY ONLY, and deliberately dumb.
//
// It is the switch behind three PROPOSALS for the message queue and the call
// block (A, B, C), plus the current product ("today"), so the owner can see
// them mounted in the real screens instead of in a mock-up. Nothing here is a
// decided design: the branches guarded by this module are proposal code,
// marked as such at every site.
//
// The whole laboratory hangs off two URL parameters that production never
// carries:
//
//   ?cq=today|a|b|c        which proposal to paint
//   ?cqcase=one|many|call  which situation to seed (see the fixtures below)
//
// With no parameters — i.e. the product — `designVariant()` is "today" and
// `designCase()` is null, so every guarded branch collapses to exactly what
// ships today.

import { setState, store, updateSession } from "./store.js";

const VARIANTS = new Set(["today", "a", "b", "c"]);
const CASES = new Set(["one", "many", "call"]);

// Read once: the URL does not change under the app (the lab is entered by
// navigating), and a per-render URLSearchParams in the Composer would be a
// cost paid on every keystroke in production.
let cached = null;
function read() {
  if (cached) return cached;
  let variant = "today";
  let kase = null;
  try {
    const search = typeof location === "undefined" ? "" : location.search;
    const p = new URLSearchParams(search);
    const v = (p.get("cq") || "").toLowerCase();
    const c = (p.get("cqcase") || "").toLowerCase();
    if (VARIANTS.has(v)) variant = v;
    if (CASES.has(c)) kase = c;
  } catch (_) { /* no URL: production defaults */ }
  cached = { variant, case: kase };
  return cached;
}

export function designVariant() { return read().variant; }
export function designCase() { return read().case; }

// The queue fixture, in order. Same three messages in every density and every
// proposal, so the photographs compare designs and not content.
export const DESIGN_QUEUE = [
  { id: "q1", text: "Also check the mobile drawer still opens after this", confirmed: true },
  { id: "q2", text: "/model opus", command: true, confirmed: true },
  { id: "q3", text: "Don't touch pkg/serve/static by hand — regenerate the bundle in the same commit", confirmed: true },
];

// one/call carry a single queued message; many carries the three.
export function designQueue() {
  const kase = designCase();
  if (!kase) return null;
  return kase === "many" ? DESIGN_QUEUE.map((m) => ({ ...m })) : [{ ...DESIGN_QUEUE[0] }];
}

// The simulated call. Shape-compatible with useVoiceLive's return value, so
// the Composer can use it in place of the hook WITHOUT the hook being touched.
// `costUSD` is the one field the hook does not have on this branch: the cost
// line lives in feat/voice-delegate (3ef0bf32), which is what the owner runs,
// and the photograph has to be faithful to that.
export function designCall() {
  if (designCase() !== "call") return null;
  return {
    active: true,
    phase: "live",
    endedReason: null,
    micState: "live",
    questionsUsed: 2,
    maxQuestions: 5,
    pendingAsks: 0,
    elapsed: 84,
    costUSD: 0.42,
    backendModel: "gpt-4.1-mini",
    supported: true,
    start() {},
    hangup() {},
  };
}

// The cost, printed the way feat/voice-delegate prints it.
export function formatCallCost(costUSD) {
  if (!(costUSD > 0)) return "";
  return `$${costUSD.toFixed(2)}`;
}

export const CALL_COST_TITLE = "What this call has cost so far (voice model)";

// mm:ss, the same clock VoiceLivePanel draws.
export function callClock(seconds) {
  const total = Math.max(0, Math.floor(seconds || 0));
  return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, "0")}`;
}

// --- Per-message queue actions (LAB) -------------------------------------
// A and B and C all offer "cancel this one" and "edit this one", which the
// product has no server call for today (the only endpoint cancels the WHOLE
// queue). In the lab they act on the seeded store, which is enough to show
// what the interaction feels like; see the report for what is paint and what
// is behaviour.

export function labCancelQueued(sessionId, id) {
  const session = store.get().sessions[sessionId];
  const list = (session?.pendingSteers || []).filter((s) => s.id !== id);
  updateSession(sessionId, { pendingSteers: list.length ? list : null });
}

// Edit = take this message out of the queue and drop its text in the composer,
// through the composerDrops handoff a share already uses.
export function labEditQueued(sessionId, id) {
  const session = store.get().sessions[sessionId];
  const target = (session?.pendingSteers || []).find((s) => s.id === id);
  labCancelQueued(sessionId, id);
  if (!target) return;
  setState((state) => ({
    composerDrops: {
      ...state.composerDrops,
      [sessionId]: { id: `lab-edit-${id}`, text: target.text, focus: true },
    },
  }));
}
