// Pure model for the "Passwords saved on this iPhone" page. The list comes from
// the native bridge (window.moaSavedSecrets.list) and carries no value and no
// fingerprint: only what is needed to recognise an entry.

// savedValue — what the Settings row says on its right.
export function savedValue(count, loaded) {
  if (!loaded) return null;
  return count === 0 ? "None" : String(count);
}

function clock(ms) {
  const d = new Date(ms);
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
}

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

// when — "14:02" for today, "2 Oct" for any other day.
export function when(ms, now = Date.now()) {
  const d = new Date(ms);
  const n = new Date(now);
  if (d.getFullYear() === n.getFullYear() && d.getMonth() === n.getMonth() && d.getDate() === n.getDate()) {
    return clock(ms);
  }
  return `${d.getDate()} ${MONTHS[d.getMonth()]}`;
}

// savedLine — the row's second line: when it was last used (or saved), then
// what it is for.
export function savedLine(item, now = Date.now()) {
  const used = Number(item?.lastUsedAt) > 0;
  const stamp = used ? item.lastUsedAt : item?.savedAt;
  const parts = [];
  if (Number(stamp) > 0) parts.push(`${used ? "Last used" : "Saved"} ${when(Number(stamp), now)}`);
  if (item?.actionSummary) parts.push(item.actionSummary);
  return parts.join(" · ");
}

// sortSaved — most recently used first, never-used by their save date.
export function sortSaved(list) {
  const key = (i) => Number(i?.lastUsedAt) || Number(i?.savedAt) || 0;
  return [...list].sort((a, b) => key(b) - key(a));
}

export function withoutAlias(list, alias) {
  return list.filter((i) => i.alias !== alias);
}
