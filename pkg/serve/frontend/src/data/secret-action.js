// secret-action.js — pure model of the "secret for an action" card: what a
// pending request says, what the card prints for it, and what it may send.
// No store, no DOM: the card and the tests share these rules.
//
// Values live only in the card's local state and in the POST body built here.
// Nothing in this module (or in a request object) ever carries one.

const FIELD_KINDS = new Set(["password", "username", "text"]);

// normalizeSecretRequest copies the metadata the card reads and nothing else,
// so a stray property on the wire cannot end up rendered. Empty strings stand
// for the contract's omitted fields.
export function normalizeSecretRequest(data) {
  if (!data || typeof data.id !== "string" || !data.id) return null;
  const fields = Array.isArray(data.fields) ? data.fields : [];
  return {
    id: data.id,
    requester: typeof data.requester === "string" ? data.requester : "main",
    kind: data.kind === "browser" ? "browser" : "command",
    command: data.command || "",
    cwd: data.cwd || "",
    sshHost: data.sshHost || "",
    origin: data.origin || "",
    submit: !!data.submit,
    actionFingerprint: data.actionFingerprint || "",
    requestedAt: data.requestedAt || "",
    fields: fields.map((f, i) => ({
      index: Number.isInteger(f?.index) ? f.index : i,
      label: f?.label || "",
      kind: FIELD_KINDS.has(f?.kind) ? f.kind : "password",
      alias: f?.alias || "",
      element: f?.element || "",
      realLabel: f?.realLabel || "",
      realType: f?.realType || "",
    })),
  };
}

export function normalizeSecretRequests(list) {
  if (!Array.isArray(list)) return [];
  return list.map(normalizeSecretRequest).filter(Boolean);
}

export function pendingSecrets(session) {
  return Array.isArray(session?.pendingSecrets) ? session.pendingSecrets : [];
}

// requesterLabel names who asked. The main agent is the default and stays
// silent; a subagent is shown by its title when the session knows it, else by
// the word, never by its job id.
export function requesterLabel(requester, subagents) {
  if (!requester || requester === "main") return "";
  const match = /^subagent\s*\u00b7\s*(.+)$/.exec(requester);
  if (!match) return "";
  const title = subagents?.[match[1].trim()]?.title;
  return title ? `subagent · ${title}` : "subagent";
}

// secretCopy — every string the card says that depends on the kind.
export function secretCopy(request) {
  if (request?.kind === "browser") {
    return {
      title: "Sign in on this page",
      note: "Sent only to this page, not to the chat",
      primary: "Fill in",
    };
  }
  return {
    title: "Password for this command",
    note: "Sent only to this command, not to the chat",
    primary: "Run",
  };
}

// fieldAutocomplete — what a password manager is told about a field.
export function fieldAutocomplete(kind) {
  if (kind === "password") return "current-password";
  if (kind === "username") return "username";
  return "off";
}

export function fieldInputName(field) {
  if (field.kind === "password") return "password";
  if (field.kind === "username") return "username";
  return `field-${field.index}`;
}

// browserMapping — one line per field for the browser card: which real form
// control on the page the value will go to.
export function browserMapping(request) {
  return request.fields.map((f) => {
    const target = f.realLabel || f.element;
    const where = target ? `field “${target}”` : "a field on the page";
    return { label: f.label, text: f.realType ? `${where} (${f.realType})` : where };
  });
}

// firstEmptyIndex — the position the card focuses on desktop. `skip` holds the
// indexes already satisfied by something other than typing.
export function firstEmptyField(fields, values, skip = new Set()) {
  return fields.find((f) => !skip.has(f.index) && !values[f.index]) || null;
}

export function valuesComplete(fields, values, skip = new Set()) {
  return fields.every((f) => skip.has(f.index) || !!values[f.index]);
}

// buildAllowBody — the exact POST body for allow. Throws when something is
// missing so the card can never send a request the server would refuse.
export function buildAllowBody(fields, values) {
  const out = fields.map((f) => {
    const value = values[f.index];
    if (!value) throw new Error("missing value");
    return { index: f.index, value };
  });
  return { action: "allow", values: out };
}

export const DENY_BODY = Object.freeze({ action: "deny" });

// secretErrorText — what the inline error line says. The server's body is
// never reflected: a misbehaving intermediary could echo the request.
export function secretErrorText(error) {
  const status = error?.status;
  if (status === 404) return "This request is no longer pending.";
  if (status === 400) return "The server refused these values. Check them and try again.";
  if (status === 413) return "A value is too long to send.";
  return "Could not send. Try again.";
}

// withoutSecret / withSecret — pure list updates for the store, oldest first,
// one entry per id.
export function withSecret(list, request) {
  const current = Array.isArray(list) ? list : [];
  if (current.some((r) => r.id === request.id)) return current;
  return [...current, request];
}

export function withoutSecret(list, id) {
  const current = Array.isArray(list) ? list : [];
  const next = current.filter((r) => r.id !== id);
  return next.length === current.length ? current : next;
}
