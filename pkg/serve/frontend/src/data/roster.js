// roster.js — a stable identity for the session roster.
//
// A streamed token replaces `state.sessions` (a new map holding a new object
// for the one streaming session), yet nothing a list paints changes with it.
// The selectors that read the whole roster (sidebar, drawer, owners, inbox,
// tally, task directory) used to run again on every frame; with ~1500 sessions
// that was most of the main thread on a phone. rosterSessions hands them the
// previous map whenever the new one differs only in what no row or tally
// reads, so they can memoize on identity and cost nothing until a roster
// change reaches them.
//
// Only what the roster consumers never read is ignored, so a field added
// later still counts as a change. A consumer of the roster must not read the
// ignored fields from it: the map it gets can hold an older object of the
// same session.

// The conversation of a session: the text being streamed and the messages it
// lands in (tool output streams into them). The roster reads none of it.
const CONVERSATION = new Set(['streamingText', 'thinkingText', 'messages']);

// The jobs of a session count for the roster only through their status (the
// tally counts an idle session whose subagents are still alive as working),
// not through the text a running job streams.
function sameJobs(a, b) {
  if (a === b) return true;
  let count = 0;
  for (const id in a) {
    count++;
    if (a[id]?.status !== b?.[id]?.status) return false;
  }
  for (const id in b) count--;
  return count === 0;
}

function sameSession(a, b) {
  if (a === b) return true;
  if (!a || !b) return false;
  // A key may be absent on one side (the first token adds `streamingText`),
  // so the ignored fields stay out of the key count.
  if (!sameJobs(a.subagents, b.subagents)) return false;
  let count = 0;
  for (const key in a) {
    if (CONVERSATION.has(key) || key === 'subagents') continue;
    count++;
    if (a[key] !== b[key]) return false;
  }
  for (const key in b) if (!CONVERSATION.has(key) && key !== 'subagents') count--;
  return count === 0;
}

function sameRoster(a, b) {
  let count = 0;
  for (const id in a) {
    count++;
    if (!(id in b) || !sameSession(a[id], b[id])) return false;
  }
  for (const id in b) count--;
  return count === 0;
}

let slot = { input: null, canonical: null };

export function rosterSessions(sessions) {
  if (!sessions || typeof sessions !== 'object') return sessions;
  if (sessions === slot.input) return slot.canonical;
  if (slot.canonical && sameRoster(slot.canonical, sessions)) {
    slot = { input: sessions, canonical: slot.canonical };
    return slot.canonical;
  }
  slot = { input: sessions, canonical: sessions };
  return sessions;
}

// memoRefs remembers the last result of `fn` and returns it while every
// argument is the same reference.
export function memoRefs(fn) {
  let last = null;
  let result;
  const memo = (...refs) => {
    if (last && last.length === refs.length && refs.every((ref, i) => Object.is(ref, last[i]))) return result;
    last = refs;
    result = fn(...refs);
    return result;
  };
  memo.reset = () => { last = null; result = undefined; };
  return memo;
}

// clockBucket moves on every few seconds. Lists print ages in minutes
// ("5m", "Running · 2m") from the wall clock, so a memoized list must be
// recomputed now and then even when nothing it reads has changed; seconds
// are far finer than anything they show.
export function clockBucket(now = Date.now()) {
  return Math.floor(now / 5000);
}

// sameBy turns a signature into an equality for the parts of a chrome object.
// The two lists a selector compares are the same two for as long as nothing
// new arrives (the previous chrome is kept while it still looks the same), so
// the verdict of the last pair is kept too: without it every stream token
// would rebuild both signatures of a list to learn what it already knew.
export function sameBy(sig) {
  let lastA;
  let lastB;
  let verdict = false;
  return (a, b) => {
    if (a === b) return true;
    if (a !== lastA || b !== lastB) {
      lastA = a;
      lastB = b;
      verdict = sig(a) === sig(b);
    }
    return verdict;
  };
}
