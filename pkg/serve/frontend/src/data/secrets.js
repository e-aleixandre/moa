import { api } from "./api.js";

export const SECRET_ALIAS_RE = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;
export const MAX_SECRET_ROWS = 16;
// A long slash-command list is much more likely to include a pasted value
// than to be an intentional batch. The masked sheet still permits 16 rows.
export const MAX_SECRET_COMMAND_ALIASES = 3;
export const MAX_SECRET_VALUE_BYTES = 16 << 10;

export function secretRowsForAliases(aliases = []) {
  return aliases.length > 0
    ? aliases.map((name) => ({ name, value: "" }))
    : [{ name: "", value: "" }];
}

// A /secret command is deliberately only an alias carrier. Values are entered
// later in the isolated sheet and never pass through the composer.
export function parseSecretCommand(text) {
  // Inspect only the command line. Anything after its first line might be an
  // accidentally pasted credential, but must still cause interception rather
  // than falling through to the composer as a generic command or steer.
  const firstLine = String(text).split(/\r?\n|\r/, 1)[0].trim();
  const match = /^\/secret(?:\s+(.*))?\s*$/i.exec(firstLine);
  if (!match) return null;
  return (match[1] || "").trim().split(/\s+/).filter(Boolean);
}

// The only state handed back to Composer when it opens the sheet. Keeping the
// cleared draft in this pure boundary makes it testable that no sheet value has
// a route into the composer's persisted state.
export function interceptSecretCommand(text) {
  const aliases = parseSecretCommand(text);
  if (aliases === null) return null;
  if (aliases.length > MAX_SECRET_COMMAND_ALIASES) {
    return { aliases: [], composerDraft: "", error: `Enter at most ${MAX_SECRET_COMMAND_ALIASES} aliases after /secret, then add more in the masked form.` };
  }
  if (aliases.some((alias) => !SECRET_ALIAS_RE.test(alias))) {
    return { aliases: [], composerDraft: "", error: "Aliases use only letters, numbers, dots, underscores, and dashes." };
  }
  return { aliases, composerDraft: "", error: "" };
}

export function validateSecretRows(rows) {
  const errors = {};
  if (rows.length === 0) return { form: "Add at least one secret", rows: errors };
  if (rows.length > MAX_SECRET_ROWS) return { form: `At most ${MAX_SECRET_ROWS} secrets can be sent together`, rows: errors };

  const names = new Map();
  rows.forEach((row, index) => {
    const rowErrors = {};
    if (!SECRET_ALIAS_RE.test(row.name)) rowErrors.name = "Use 1–64 letters, numbers, dots, underscores, or dashes";
    if (!row.value) rowErrors.value = "Enter a secret value";
    else if (new TextEncoder().encode(row.value).length > MAX_SECRET_VALUE_BYTES) rowErrors.value = `Secret value exceeds ${MAX_SECRET_VALUE_BYTES} bytes`;
    if (row.name) {
      const key = row.name.toLowerCase();
      const first = names.get(key);
      if (first !== undefined) {
        rowErrors.name = "Alias must be unique";
        errors[first] = { ...(errors[first] || {}), name: "Alias must be unique" };
      } else {
        names.set(key, index);
      }
    }
    if (Object.keys(rowErrors).length > 0) errors[index] = { ...(errors[index] || {}), ...rowErrors };
  });
  return { form: "", rows: errors };
}

export function buildSecretBatch(rows) {
  const validation = validateSecretRows(rows);
  if (validation.form || Object.keys(validation.rows).length > 0) {
    return { secrets: null, errors: validation };
  }
  return {
    secrets: rows.map(({ name, value }) => ({ name, value })),
    errors: null,
  };
}

export function storeSecretBatch(sessionId, secrets) {
  return api("POST", `/api/sessions/${sessionId}/secrets`, { secrets });
}
