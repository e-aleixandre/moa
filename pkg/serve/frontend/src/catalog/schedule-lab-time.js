// schedule-lab-time.js — CATALOG ONLY. The clock of the scheduling lab: wall
// time in a named IANA zone (the device's, never a fixed one), a small
// natural-language reader for "When" (English and Spanish, the two languages
// the owner types in), and the words the lab prints for a time or a rule.
// It is a sketch of the behaviour the product would need, not the parser it
// would ship.

const WD_EN = ["sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"];
const WD_ES = ["domingo", "lunes", "martes", "miercoles", "jueves", "viernes", "sabado"];
const WD_SHORT = ["sun", "mon", "tue", "wed", "thu", "fri", "sat"];
const MON = ["jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"];
const MON_ES = ["ene", "feb", "mar", "abr", "may", "jun", "jul", "ago", "sep", "oct", "nov", "dic"];
export const WEEKDAY_NAMES = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];

export function deviceZone() {
  try { return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC"; } catch { return "UTC"; }
}

const fmtCache = new Map();
function fmt(tz) {
  if (!fmtCache.has(tz)) {
    fmtCache.set(tz, new Intl.DateTimeFormat("en-US", {
      timeZone: tz, hourCycle: "h23", year: "numeric", month: "numeric", day: "numeric", hour: "numeric", minute: "numeric", weekday: "short",
    }));
  }
  return fmtCache.get(tz);
}

// wall — the wall clock of `ms` in `tz`.
export function wall(ms, tz) {
  const p = Object.fromEntries(fmt(tz).formatToParts(new Date(ms)).map((x) => [x.type, x.value]));
  return { y: +p.year, mo: +p.month, d: +p.day, h: +p.hour % 24, mi: +p.minute, dow: WD_SHORT.indexOf(p.weekday.toLowerCase()) };
}

// zoned — the instant a wall time names in `tz` (a DST gap moves forward).
export function zoned(y, mo, d, h, mi, tz) {
  const want = Date.UTC(y, mo - 1, d, h, mi);
  let guess = want;
  for (let i = 0; i < 3; i++) {
    const w = wall(guess, tz);
    guess += want - Date.UTC(w.y, w.mo - 1, w.d, w.h, w.mi);
  }
  return guess;
}

function addDays(w, n) {
  const t = new Date(Date.UTC(w.y, w.mo - 1, w.d + n));
  return { y: t.getUTCFullYear(), mo: t.getUTCMonth() + 1, d: t.getUTCDate(), dow: t.getUTCDay() };
}

const pad = (n) => String(n).padStart(2, "0");
export const hhmm = (h, mi) => `${pad(h)}:${pad(mi)}`;

// ── Words for a time ───────────────────────────────────────────────────────

function dayDiff(ms, now, tz) {
  const a = wall(ms, tz); const b = wall(now, tz);
  return Math.round((Date.UTC(a.y, a.mo - 1, a.d) - Date.UTC(b.y, b.mo - 1, b.d)) / 86400000);
}

export function dateLabel(ms, tz) {
  const w = wall(ms, tz);
  return `${WEEKDAY_NAMES[w.dow].slice(0, 3)} ${w.d} ${MON[w.mo - 1][0].toUpperCase()}${MON[w.mo - 1].slice(1)}`;
}

// whenShort — "Today 21:00", "Tomorrow 03:00", "Mon 09:00", "Mon 13 Oct 09:00".
export function whenShort(ms, now, tz) {
  const w = wall(ms, tz);
  const diff = dayDiff(ms, now, tz);
  const t = hhmm(w.h, w.mi);
  if (diff === 0) return `Today ${t}`;
  if (diff === 1) return `Tomorrow ${t}`;
  if (diff === -1) return `Yesterday ${t}`;
  if (diff > 1 && diff < 7) return `${WEEKDAY_NAMES[w.dow].slice(0, 3)} ${t}`;
  return `${dateLabel(ms, tz)} ${t}`;
}

// whenLong — "Wed 1 Oct, 03:00".
export function whenLong(ms, tz) {
  const w = wall(ms, tz);
  return `${dateLabel(ms, tz)}, ${hhmm(w.h, w.mi)}`;
}

export function inWords(ms, now) {
  const m = Math.round((ms - now) / 60000);
  const ago = m < 0;
  const a = Math.abs(m);
  let s;
  if (a < 1) s = "now";
  else if (a < 60) s = `${a} min`;
  else if (a < 60 * 36) s = `${Math.round(a / 60)} h`;
  else s = `${Math.round(a / 1440)} days`;
  if (s === "now") return "now";
  return ago ? `${s} ago` : `in ${s}`;
}

// ── Rules ─────────────────────────────────────────────────────────────────
// { freq: 'daily' | 'weekdays' | 'weekly' | 'monthly', dow, dom, h, mi }

export function ruleText(rule) {
  const t = hhmm(rule.h, rule.mi);
  if (rule.freq === "daily") return `Every day, ${t}`;
  if (rule.freq === "weekdays") return `Weekdays, ${t}`;
  if (rule.freq === "weekly") return `Every ${WEEKDAY_NAMES[rule.dow]}, ${t}`;
  if (rule.freq === "monthly") return `Monthly on the ${ordinal(rule.dom)}, ${t}`;
  return t;
}

export function ruleShort(rule) {
  const t = hhmm(rule.h, rule.mi);
  if (rule.freq === "daily") return `Daily ${t}`;
  if (rule.freq === "weekdays") return `Weekdays ${t}`;
  if (rule.freq === "weekly") return `${WEEKDAY_NAMES[rule.dow].slice(0, 3)} ${t}`;
  if (rule.freq === "monthly") return `Monthly ${t}`;
  return t;
}

function ordinal(n) {
  const s = n % 100 >= 11 && n % 100 <= 13 ? "th" : ({ 1: "st", 2: "nd", 3: "rd" }[n % 10] || "th");
  return `${n}${s}`;
}

export function nextOf(rule, after, tz) {
  const base = wall(after, tz);
  for (let i = 0; i < 400; i++) {
    const day = addDays(base, i);
    const ok = rule.freq === "daily"
      || (rule.freq === "weekdays" && day.dow >= 1 && day.dow <= 5)
      || (rule.freq === "weekly" && day.dow === rule.dow)
      || (rule.freq === "monthly" && day.d === rule.dom);
    if (!ok) continue;
    const at = zoned(day.y, day.mo, day.d, rule.h, rule.mi, tz);
    if (at > after) return at;
  }
  return null;
}

// ── Reading "When" ──────────────────────────────────────────────────────────

const norm = (s) => s.toLowerCase().normalize("NFD").replace(/[\u0300-\u036f]/g, "").replace(/\s+/g, " ").trim();

function dowOf(word) {
  const w = word.replace(/s$/, "");
  let i = WD_EN.findIndex((x) => x === word || x === `${w}` || x.slice(0, 3) === word);
  if (i < 0) i = WD_ES.findIndex((x) => x === word || x === w);
  return i;
}

// readTime — "3", "3:30", "15h", "3pm", "a las 3", "at 9"; returns
// { h, mi, ambiguous } or null. An hour 1–11 with no am/pm is read as written
// (24h clock) and flagged so the preview can offer the other half of the day.
function readTime(s) {
  const tail = (m, h, mi, suffix) => {
    if (h > 23 || mi > 59) return null;
    if (suffix === "pm" && h < 12) h += 12;
    if (suffix === "am" && h === 12) h = 0;
    let ambiguous = !suffix && !m[2] && h >= 1 && h <= 11;
    if (/\b(de la tarde|por la tarde|in the afternoon|in the evening|de la noche|tonight|esta noche)\b/.test(s) && h < 12 && !suffix) { h += 12; ambiguous = false; }
    if (/\b(de la manana|in the morning|de la madrugada)\b/.test(s)) ambiguous = false;
    return { h, mi, ambiguous, span: [m.index, m.index + m[0].length] };
  };
  let m = s.match(/\b(noon|mediodia|midnight|medianoche)\b/);
  if (m) return { h: /noon|mediodia/.test(m[1]) ? 12 : 0, mi: 0, span: [m.index, m.index + m[0].length] };
  // "at 9", "a las 3:30", "@ 18h"
  m = s.match(/(?:\bat|\ba las?|@)\s*(\d{1,2})(?:[:.h](\d{2}))?\s*(am|pm)?\b/);
  if (m) return tail(m, +m[1], m[2] ? +m[2] : 0, m[3]);
  // "14:00", "3pm", "18h"
  m = s.match(/\b(\d{1,2})(?::(\d{2})\s*(am|pm)?|\s*(am|pm)|h)\b/);
  if (m) return tail(m, +m[1], m[2] ? +m[2] : 0, m[3] || m[4]);
  return null;
}

function withoutTime(s, t) {
  const cut = t?.span ? s.slice(0, t.span[0]) + " " + s.slice(t.span[1]) : s;
  return cut.replace(/\s+/g, " ").trim();
}

// parseWhen — { kind:'once', at, alt? } | { kind:'repeat', rule, next } |
// { error } | null (nothing typed).
export function parseWhen(input, now, tz) {
  const s = norm(input || "");
  if (!s) return null;
  const cur = wall(now, tz);

  // "in 20 min", "en 2 horas", "dentro de 3 dias", "in an hour", "en media hora"
  const rel = s.match(/^(?:in|en|dentro de)\s+(\d+|an?|una?|media)\s*(minutes?|mins?|minutos?|m|hours?|horas?|h|days?|dias?|d)\b/);
  if (rel) {
    const n = /^\d+$/.test(rel[1]) ? +rel[1] : rel[1] === "media" ? 0.5 : 1;
    const unit = rel[2][0];
    const ms = unit === "m" ? n * 60000 : unit === "h" ? n * 3600000 : n * 86400000;
    return { kind: "once", at: Math.round((now + ms) / 60000) * 60000 };
  }

  // Repeats: "every monday at 9", "cada lunes a las 9", "every day at 8",
  // "weekdays at 9", "todos los dias a las 8", "every month on the 1st".
  const rep = s.match(/^(?:every|each|cada|todos los|todas las)\s+(.+)$/) || s.match(/^(weekdays|laborables|entre semana)\b(.*)$/);
  if (rep) {
    const rest = rep[0].startsWith("weekdays") || rep[0].startsWith("laborables") || rep[0].startsWith("entre semana") ? `weekday ${rep[2] || ""}` : rep[1];
    const t = readTime(rest) || { h: 9, mi: 0 };
    const words = withoutTime(rest, t).split(" ");
    let rule = null;
    if (/^(day|dia|dias|morning|manana)$/.test(words[0])) rule = { freq: "daily" };
    else if (/^(weekday|weekdays|dia laborable|laborables|entre)/.test(words.join(" "))) rule = { freq: "weekdays" };
    else if (/^(month|mes)$/.test(words[0])) {
      const dom = (withoutTime(rest, t).match(/(?:the|el|dia)\s+(\d{1,2})/) || [])[1];
      rule = { freq: "monthly", dom: +(dom || 1) };
    } else if (/^(week|semana)$/.test(words[0])) rule = { freq: "weekly", dow: cur.dow };
    else {
      const d = dowOf(words[0]);
      if (d >= 0) rule = { freq: "weekly", dow: d };
    }
    if (!rule) return { error: "repeat" };
    rule = { ...rule, h: t.h, mi: t.mi };
    return { kind: "repeat", rule, next: nextOf(rule, now, tz) };
  }

  const t = readTime(s);
  const rest = withoutTime(s, t).replace(/\b(at|a las?|el|on|next|proximo|this|este)\b/g, " ").replace(/\s+/g, " ").trim();
  let day = null; // { y, mo, d }

  if (!rest || /^(today|hoy)$/.test(rest)) day = cur;
  else if (/^(tonight|esta noche)$/.test(rest)) day = cur;
  else if (/^(tomorrow|manana|tomorrow morning|manana por la manana)$/.test(rest) || rest.startsWith("tomorrow") || rest.startsWith("manana")) day = addDays(cur, 1);
  else if (/^pasado manana/.test(rest) || rest.startsWith("day after tomorrow")) day = addDays(cur, 2);
  else {
    const iso = rest.match(/^(\d{4})-(\d{2})-(\d{2})$/);
    const dm = rest.match(/^(\d{1,2})[/-](\d{1,2})$/);
    const named = rest.match(/^(\d{1,2}) (?:de )?([a-z]{3})[a-z]*$/) || rest.match(/^([a-z]{3})[a-z]* (\d{1,2})$/);
    const d = dowOf(rest.split(" ")[0]);
    if (iso) day = { y: +iso[1], mo: +iso[2], d: +iso[3] };
    else if (dm) day = { y: cur.y, mo: +dm[2], d: +dm[1] };
    else if (named) {
      const [a, b] = /^\d/.test(named[1]) ? [named[1], named[2]] : [named[2], named[1]];
      let mo = MON.indexOf(b); if (mo < 0) mo = MON_ES.indexOf(b);
      if (mo >= 0) day = { y: cur.y, mo: mo + 1, d: +a };
    } else if (d >= 0) {
      const ahead = (d - cur.dow + 7) % 7 || 7;
      day = addDays(cur, ahead);
    }
  }
  if (!day) return { error: "unknown" };
  const time = t || (/tonight|esta noche/.test(s) ? { h: 21, mi: 0 } : { h: 9, mi: 0 });
  let at = zoned(day.y, day.mo, day.d, time.h, time.mi, tz);
  // A bare time already past today means tomorrow ("at 3" at 16:40).
  if (at <= now && (!rest || /^(today|hoy)$/.test(rest)) && !/today|hoy/.test(s)) at = zoned(...Object.values(addDays(cur, 1)).slice(0, 3), time.h, time.mi, tz);
  if (at <= now) return { error: "past", at };
  const out = { kind: "once", at };
  if (time.ambiguous) {
    const sameDay = zoned(...Object.values(wall(at, tz)).slice(0, 3), time.h + 12, time.mi, tz);
    const today = zoned(cur.y, cur.mo, cur.d, time.h + 12, time.mi, tz);
    out.alt = !rest && today > now ? today : sameDay;
  }
  return out;
}

// Presets the popover offers before anything is typed.
export function presets(now, tz) {
  const cur = wall(now, tz);
  const tomorrow = addDays(cur, 1);
  const nextMon = addDays(cur, ((1 - cur.dow + 7) % 7) || 7);
  return [
    { label: "In 20 minutes", text: "in 20 min", at: now + 20 * 60000 },
    { label: "Tonight", text: "tonight at 21:00", at: zoned(cur.y, cur.mo, cur.d, 21, 0, tz) },
    { label: "Tomorrow morning", text: "tomorrow at 9", at: zoned(tomorrow.y, tomorrow.mo, tomorrow.d, 9, 0, tz) },
    { label: "Monday", text: "monday at 9", at: zoned(nextMon.y, nextMon.mo, nextMon.d, 9, 0, tz) },
  ].filter((p) => p.at > now);
}

// For the picker's fields.
export function dateInputValue(ms, tz) {
  const w = wall(ms, tz);
  return `${w.y}-${pad(w.mo)}-${pad(w.d)}`;
}
export function timeInputValue(ms, tz) {
  const w = wall(ms, tz);
  return hhmm(w.h, w.mi);
}
export function fromInputs(date, time, tz) {
  const [y, mo, d] = date.split("-").map(Number);
  const [h, mi] = time.split(":").map(Number);
  return zoned(y, mo, d, h, mi, tz);
}
export function weekdayOf(ms, tz) { return wall(ms, tz).dow; }
export function dayOfMonth(ms, tz) { return wall(ms, tz).d; }
export function wallOf(ms, tz) { return wall(ms, tz); }
