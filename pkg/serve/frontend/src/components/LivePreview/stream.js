import { activityPhase, activityText, inFlightTool, liveVerb } from "../../data/util/activity.js";
import { toolPath } from "../../data/util/format.js";
import { mapToolToKind } from "../../data/util/tool-kind.js";

// stream.js — PURE model for the LivePreview's ephemeral stream: the run,
// projected as a list of things that can float up over the app and vanish.
//
// The preview covers the transcript, so the user still needs to know what the
// agent is doing — but every fixed place we gave that job (a panel, a foot, the
// URL row) turned into a second, worse chat. So nothing here is a place: it is
// a sequence of moments. This function says WHICH moments exist right now; the
// overlay decides when each one enters, rises and dies.
//
// Event shapes (oldest → newest, chronological):
//   { id, kind:'tool', tool, toolKind, text, running }
//       One tool call, in the ledger's grammar (verb + object, and the kind
//       that picks the ledger's icon and color).
//   { id, kind:'text', text, streaming }
//       A run of assistant prose, raw markdown. `streaming` while it is still
//       growing — the overlay keeps the same card and lets it get longer.
//   { id:'waiting', kind:'waiting', text }
//       The run is parked on a human. The one event that must NOT expire.
//
// IDS ARE THE CONTRACT. The overlay keeps a card alive across renders by id, so
// an id must not change while the thing it names is the same thing. Two cases
// matter: a tool keeps its tool_call_id from `generating` to `done`, and prose
// keeps its ORDINAL among assistant text blocks — which is why the live
// streamingText is numbered as the next block: when the reducer materializes it
// into a message (on a tool start or at message end) the id it lands on is the
// one the card already had, so the card grows instead of being reborn.

function joinText(content) {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  return content
    .filter((c) => c && c.type === "text" && c.text)
    .map((c) => c.text)
    .join("");
}

// A path is worth a card only as its leaf: on a phone
// "pkg/serve/frontend/src/…/style.css" is an ellipsis with a filename hiding in
// it. Everything that is not a path (a command, a pattern, a url) is left whole
// and clipped by CSS, exactly like a ledger row.
const PATH_TOOLS = new Set(["read", "write", "edit", "multiedit", "ls", "send_file", "apply_patch"]);

export function toolObject(name, args) {
  const value = toolPath(name, args) || "";
  if (!value) return "";
  if (!PATH_TOOLS.has((name || "").toLowerCase())) return value;
  const leaf = value.replace(/\/+$/, "").split("/").pop();
  return leaf || value;
}

// toolText — "Editing style.css". Without an object the verb alone would be a
// riddle ("Calling"), so an unmapped tool says its own name instead.
export function toolText(name, args) {
  const verb = liveVerb(name);
  const object = toolObject(name, args);
  if (object) return `${verb} ${object}`;
  return verb === "Calling" ? `Calling ${name}` : `${verb}…`;
}

export function streamEvents(session, limit = 8) {
  const messages = Array.isArray(session?.messages) ? session.messages : [];
  const out = [];
  let prose = 0;

  for (const msg of messages) {
    if (!msg) continue;
    if (msg._type === "tool_start") {
      const name = msg.tool_name || "";
      if (!name) continue;
      out.push({
        id: `tool:${msg.tool_call_id || `i${out.length}`}`,
        kind: "tool",
        tool: name,
        toolKind: mapToolToKind(name),
        text: toolText(name, msg.args),
        running: msg.status === "running" || msg.status === "generating",
      });
      continue;
    }
    if (msg.role !== "assistant") continue;
    const text = joinText(msg.content).trim();
    if (!text) continue;
    out.push({ id: `text:${prose++}`, kind: "text", text, streaming: false });
  }

  const live = (session?.streamingText || "").trim();
  if (live) out.push({ id: `text:${prose}`, kind: "text", text: live, streaming: true });

  // Waiting is appended last on purpose: it is the newest thing that happened
  // and the only one the user has to act on.
  if (activityPhase(session) === "waiting") {
    out.push({ id: "waiting", kind: "waiting", text: activityText(session) });
  }

  return limit > 0 ? out.slice(-limit) : out;
}

// admit — which events the overlay is allowed to float. Two things are muted,
// for the same reason: they are not news.
//
//   • What already existed when the preview OPENED. Replaying the last eight
//     things the agent did the moment the panel appears would be a list, which
//     is exactly what this design refuses to be.
//   • What ALREADY floated and finished its life. streamEvents() is a projection
//     of the transcript, so a tool call keeps being reported long after its card
//     died — without this the same card would rise, fade, and rise again forever.
//
// One exception, both ways: a run parked on a human. That is a question the user
// has to answer, not something that "happened", so it shows the moment they look
// and a NEW wait is never mistaken for the old one it shares an id with.
export function admit(events, muted) {
  return events.filter((e) => e.kind === "waiting" || !muted.has(e.id));
}

// gone — the ids that were on screen and no longer are. The caller mutes them
// (see admit): a card gets exactly one life.
export function gone(before, after) {
  const alive = new Set(after.map((c) => c.id));
  return before.filter((c) => !alive.has(c.id)).map((c) => c.id);
}

// ── the life of a card ──────────────────────────────────────────────────────
// How long each kind of moment is worth looking at, once it stops moving. A
// tool is a blink ("aparece y desaparece"); prose is meant to be read, so it
// gets longer and only starts counting when it stops growing; a wait is not a
// moment at all, it is a question, and questions stay.
export const TOOL_MS = 2500;
export const TEXT_MS = 6000;
export const NOTE_MS = 2500;
// The exit is animated, so a dead card lingers as DOM for exactly as long as
// the animation in LivePreview.css (keep the two numbers together).
export const EXIT_MS = 420;
export const MAX_CARDS = 3;

function ttl(event) {
  if (event.kind === "text") return TEXT_MS;
  if (event.kind === "note") return NOTE_MS;
  return TOOL_MS;
}

// A card is "alive" while the thing it names is still happening: it cannot
// start dying yet, however long it has been up.
function alive(event) {
  if (event.kind === "waiting") return true;
  if (event.kind === "tool") return !!event.running;
  if (event.kind === "text") return !!event.streaming;
  return false;
}

// reconcile — PURE step from (cards on screen, events that exist, now) to the
// next cards on screen. All the timing lives here so it can be tested without a
// clock: the component only feeds it `Date.now()` on a ticker.
//
// A card is never resurrected once it has started leaving, and identity is the
// event id — that is what lets a streaming prose card grow in place instead of
// being torn down and reborn on every delta.
export function reconcile(cards, events, now, max = MAX_CARDS) {
  const seen = new Set();
  const next = [];

  for (const card of cards) {
    seen.add(card.id);
    if (card.leaving) {
      next.push(card);
      continue;
    }
    const event = events.find((e) => e.id === card.id);
    if (event) {
      const merged = { ...card, ...event };
      merged.expiresAt = alive(event) ? null : (card.expiresAt ?? now + ttl(event));
      next.push(merged);
    } else {
      // The event is gone from the model (history trimmed, a wait answered):
      // the card still owes its own exit, so it just stops being immortal.
      next.push({ ...card, expiresAt: card.expiresAt ?? now + ttl(card) });
    }
  }
  for (const event of events) {
    if (seen.has(event.id)) continue;
    next.push({ ...event, at: now, expiresAt: alive(event) ? null : now + ttl(event) });
  }

  // Crowding: only three at a time, so a burst of tool calls floats off early
  // instead of stacking into the list this design refuses to be. The oldest go
  // first; a wait is never crowded out.
  const staying = next.filter((c) => !c.leaving);
  let excess = staying.length - max;
  const doomed = new Set();
  for (const card of staying) {
    if (excess <= 0) break;
    if (card.kind === "waiting") continue;
    doomed.add(card.id);
    excess--;
  }

  return next
    .map((card) => {
      if (card.leaving) return card;
      const dead = doomed.has(card.id) || (card.expiresAt !== null && card.expiresAt <= now);
      return dead ? { ...card, leaving: true, removeAt: now + EXIT_MS } : card;
    })
    .filter((card) => !(card.leaving && card.removeAt <= now));
}

// stageState — what the stage's own edge shows. Not a status line: two pixels
// of border the panel already spent, carrying the identity color of the tool in
// flight so the run is readable without looking away from the app.
export function stageState(session) {
  const phase = activityPhase(session);
  if (phase === "waiting") return { mode: "waiting", kind: null };
  if (phase === null) return { mode: "idle", kind: null };
  const tool = inFlightTool(session);
  return { mode: "working", kind: tool?.tool_name ? mapToolToKind(tool.tool_name) : null };
}
