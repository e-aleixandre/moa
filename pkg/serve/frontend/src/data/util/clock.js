// clock.js — the one place a transcript timestamp becomes a wall clock.
//
// The transcript reaches the client in two shapes, and which one arrives
// depends on the route rather than on the message:
//
//   · the WebSocket (`/api/sessions/{id}/ws`) and the history page send
//     core.Message, whose `timestamp` is `time.Now().Unix()` — epoch
//     SECONDS (pkg/core/message.go:164). Measured on a live session: every
//     row carries `timestamp`, and no row carries `ts`.
//   · the REST conversation DTO used for persisted subagents sends
//     ConversationMessage, whose `timestamp` is a `time.Time` marshalled as
//     an RFC3339 string (pkg/serve/conversation.go:36).
//
// Both are normalized here rather than at each call site, so a component
// receives a time and never a transport detail. Anything else — an absent
// field, a zero, an unparseable string — yields "", and the caller draws
// nothing: a missing hour is a missing hour, never an invented one.

// Epoch seconds and epoch milliseconds are both plain numbers, so they are
// told apart by magnitude. 1e11 seconds is the year 5138 and 1e11 ms is 1973,
// which puts the boundary far from any timestamp this product can hold.
const MS_THRESHOLD = 1e11;

// clockMs returns the epoch milliseconds a transcript timestamp denotes, or
// null when there is no usable time.
export function clockMs(value) {
  if (typeof value === 'number') {
    if (!Number.isFinite(value) || value <= 0) return null;
    return value < MS_THRESHOLD ? value * 1000 : value;
  }
  if (typeof value === 'string' && value !== '') {
    // A numeric string travels the same road as the number it spells.
    if (/^\d+$/.test(value)) return clockMs(Number(value));
    const parsed = Date.parse(value);
    if (!Number.isFinite(parsed)) return null;
    // Go marshals a zero time.Time as year 1; `omitempty` does not elide it
    // because a struct is never empty, so it arrives as a real string that
    // means "no timestamp".
    return parsed <= 0 ? null : parsed;
  }
  return null;
}

// clockHHMM is the hour of day, in 24-hour form.
//
// `hour12: false` is forced rather than left to the locale, and that is a
// requirement of the design, not a preference: the gutter's promise is that
// 09:14 and 11:08 occupy the same box, which is why it is mono with
// tabular-nums and why 44px was measured as enough on the phone. A 12-hour
// locale breaks all three -- measured in the browser, "10:18 PM" renders 56px
// against a narrower "9:14 AM", so the column breathes on every message and
// overflows the gutter it was sized for.
export function clockHHMM(value) {
  const ms = clockMs(value);
  if (ms === null) return '';
  return new Date(ms).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hour12: false });
}

const DAY_MS = 86400000;

function startOfDay(ms) {
  const date = new Date(ms);
  date.setHours(0, 0, 0, 0);
  return date.getTime();
}

// clockDayLabel names the day a message belongs to, but ONLY when that day is
// not today: a transcript is read in the present, so "today" is the assumption
// and saying it on every message would be noise.
//
// The label rides ABOVE the hour in a 44px gutter, so it has to FIT one, and
// no English word for yesterday does: "Yesterday" measured 68.7px in the
// gutter's own micro mono, 25px outside a phone gutter and past the 64px
// desktop one too. So every past day -- yesterday included -- wears the short
// date instead ("Mar 3", 38.2px), which fits, needs no translation, and keeps
// one shape for the whole column rather than a word for one day and a date
// for the rest.
export function clockDayLabel(value, now = Date.now()) {
  const ms = clockMs(value);
  if (ms === null) return '';
  const day = startOfDay(ms);
  const today = startOfDay(now);
  if (day >= today) return '';
  const date = new Date(ms);
  const sameYear = date.getFullYear() === new Date(now).getFullYear();
  return date.toLocaleDateString([], sameYear
    ? { day: 'numeric', month: 'short' }
    : { day: 'numeric', month: 'short', year: '2-digit' });
}
