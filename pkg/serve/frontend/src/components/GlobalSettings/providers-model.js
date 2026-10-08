// providers-model.js — what the Providers page says and offers for each row,
// from the server's status DTO alone. Pure, so the copy and the gating can be
// pinned without a DOM.
//
// The rows never draw an account, a key hint or a token: the DTO does not carry
// them, and nothing here reaches for anything beyond state/kind/source/actions.

import { attentionLabel } from "../../data/providers.js";

export const PROVIDER_ROWS = [
  { id: "anthropic", name: "Anthropic", flow: "paste_code_state" },
  { id: "openai", name: "OpenAI", flow: "paste_url" },
  { id: "xai", name: "Grok (xAI)", flow: "device" },
];

export function providerName(id) {
  return PROVIDER_ROWS.find((row) => row.id === id)?.name || id;
}

const KIND = { api_key: "API key", oauth: "Subscription" };

export function kindLabel(kind) {
  return KIND[kind] || "";
}

// planKey — the Anthropic API key stored next to a plan sign-in, or null. The
// plan comes first; the key takes over at a 5h or weekly limit.
export function planKey(row) {
  const key = row?.plan_api_key;
  return key && key.generation ? key : null;
}

// rowKind — the kind word of a row: a plan with a key beside it says both.
export function rowKind(row) {
  const kind = kindLabel(row?.kind);
  return kind && planKey(row) ? `${kind} + API key` : kind;
}

// planKeyNote — what the key beside the plan needs, or "" when nothing.
export function planKeyNote(row) {
  switch (planKey(row)?.state) {
    case "api_error":
      return "The API key failed on its last use. Replace it.";
    case "save_failed":
      return "The API key isn't used until the change is saved. Remove it again.";
    default:
      return "";
  }
}

// rowReading — the second line of a row: what state the credential is in and,
// when it needs something, what to do. `tone` drives colour only.
export function rowReading(row, canAdmin) {
  const name = providerName(row?.id);
  const owner = canAdmin !== false;
  if (!row) return { text: "", tone: "muted" };
  if (row.source === "env") {
    const rejected = row.state === "reconnect" || row.state === "key_rejected" || row.state === "missing";
    return rejected
      ? { text: "Managed by environment. Update the environment variable on the server.", tone: "warn" }
      : { text: "Managed by environment", tone: "muted" };
  }
  switch (row.state) {
    case "missing":
      return { text: "Not connected", tone: "muted" };
    case "saved":
      return { text: "Saved", tone: "muted" };
    case "ready":
      return { text: "Ready", tone: "ok" };
    case "renew_on_use":
      return { text: "Renews automatically on next use", tone: "muted" };
    case "reconnect":
      return owner
        ? { text: "Sign-in expired. Reconnect.", tone: "bad" }
        : { text: `Ask the owner to reconnect ${name}.`, tone: "bad" };
    case "key_rejected":
      return owner
        ? { text: "API key rejected. Replace it.", tone: "bad" }
        : { text: `Ask the owner to replace the ${name} API key.`, tone: "bad" };
    case "temporary":
      return { text: "The provider didn't answer. Send again.", tone: "warn" };
    case "save_failed":
      return owner
        ? { text: "Could not save credentials.", tone: "bad" }
        : { text: `Ask the owner to retry saving ${name} credentials.`, tone: "bad" };
    case "store_unavailable":
      return owner
        ? { text: "The credential file can't be read. Repair it on the server.", tone: "bad" }
        : { text: "Ask the owner to repair the credential file on the server.", tone: "bad" };
    default:
      return { text: "", tone: "muted" };
  }
}

// rowActions — the buttons a row offers. Only the owner list (GET
// /api/providers) carries `actions`; a device reading /status gets none, so a
// device row is words only. env and store_unavailable come with `[]` too.
export function rowActions(row, canAdmin) {
  const none = { signIn: null, apiKey: null, retrySave: false, primary: null };
  if (!row || canAdmin === false || row.source === "env" || row.state === "store_unavailable") return none;
  const actions = Array.isArray(row.actions) ? row.actions : [];
  const signIn = actions.includes("sign_in") && row.oauth_enabled !== false
    ? (row.kind === "api_key" ? "Use subscription" : row.kind === "oauth" ? "Reconnect" : "Sign in")
    : null;
  // Beside a plan sign-in the key is added, never swapped for the sign-in.
  const besidePlan = !!row.plan_api_key;
  const apiKey = actions.includes("api_key") && row.api_key_enabled !== false
    ? (row.kind === "api_key" || planKey(row) ? "Replace API key" : row.kind === "oauth" && !besidePlan ? "Use API key" : "Add API key")
    : null;
  const removeKey = !!apiKey && !!planKey(row);
  const retrySave = actions.includes("retry_save");
  // The primary is what the row's state needs: a rejected key wants a new key,
  // an expired sign-in wants a sign-in, a failed save wants a retry.
  const wanted = row.state === "key_rejected" ? ["apiKey", "signIn"] : ["retrySave", "signIn", "apiKey"];
  const have = { signIn, apiKey, retrySave };
  const primary = wanted.find((k) => have[k]) || null;
  return { signIn, apiKey, removeKey, retrySave, primary };
}

export const REPLACE_NOTICE =
  "Applies to all sessions on the next request. Existing history and attachments may be sent using this account.";

// replaceNotice — the confirmation shown before a credential is replaced, or
// null when there is nothing to replace. `method` is what the user is about to
// use: "oauth" or "api_key". Switching between them changes who bills.
export function replaceNotice(row, method) {
  if (!row || row.state === "missing") return null;
  // A key saved beside a plan sign-in replaces nothing.
  if (method === "api_key" && row.plan_api_key) return null;
  const name = providerName(row.id);
  let billing = "";
  if (row.kind === "api_key" && method === "oauth") {
    billing = `${name} will bill your subscription instead of API billing.`;
  } else if (row.kind === "oauth" && method === "api_key") {
    billing = `${name} will use API billing instead of your subscription.`;
  }
  return { text: REPLACE_NOTICE, billing };
}

// providersValue — the Providers row's reading on the root page. The row is
// never disabled: Settings must reach Providers even when nothing could be
// read, which is exactly when it is needed.
export function providersValue(status) {
  if (!status?.loaded) return "—";
  if (status.attentionCount > 0) return attentionLabel(status.attentionCount);
  const connected = (status.providers || []).filter((row) => row.state !== "missing" && row.state !== "store_unavailable").length;
  return connected ? `${connected} connected` : "None connected";
}

// Copy for an attempt that ended on the server (xAI progress). The progress
// body carries a class, not the server's copy, so the page states it here in
// the same words the action endpoints use.
const ENDED = {
  denied: "Sign-in was not approved. Start again.",
  expired: "This sign-in has ended. Start again.",
  canceled: "This sign-in has ended. Start again.",
  superseded: "A newer sign-in replaced this one. Start again.",
  save_failed: "Could not save credentials. Start again.",
};

export function progressEndCopy(progress) {
  return ENDED[progress?.state] || "Something went wrong. Start again.";
}

export const TERMINAL_PROGRESS = new Set(["saved", "save_failed", "denied", "expired", "canceled", "superseded", "failed"]);

export const SAVED_COPY = "Saved. Return to your session and send again.";
export const TIMEOUT_COPY = "No answer yet. Check the status above before trying again.";

// flowErrorCopy — what a failed provider request says. The server's JSON
// error carries fixed copy (`userMessage`); anything else gets moa's own
// words, never the raw response body.
export function flowErrorCopy(error) {
  if (!error) return "";
  if (error.name === "TimeoutError") return TIMEOUT_COPY;
  if (typeof error.userMessage === "string" && error.userMessage) return error.userMessage;
  if (error.status === 401 || error.status === 403) return "Only the owner can change providers here.";
  if (!error.status) return "Couldn't reach moa. Try again.";
  return "Something went wrong. Try again.";
}

// errorIntent — what the page does after an error, from the server's action.
//   "paste_again": keep the attempt, clear the field
//   "reload":      re-read the list (stale generation, origin)
//   anything else: the attempt is over, back to the row
export function errorIntent(error) {
  if (error?.name === "TimeoutError") return "check_status";
  const action = error?.detail?.action || "";
  if (action === "paste_again") return "paste_again";
  if (action === "reload") return "reload";
  return "start_again";
}

// deadlineLabel — "Expires at 14:05" for the device code's deadline.
export function deadlineLabel(expiresAt, locale) {
  const ms = Date.parse(expiresAt || "");
  if (!Number.isFinite(ms)) return "";
  const time = new Date(ms).toLocaleTimeString(locale, { hour: "2-digit", minute: "2-digit" });
  return `Code expires at ${time}`;
}

export function flowCopy(id) {
  switch (id) {
    case "openai":
      return {
        open: "Open OpenAI",
        before: "Open OpenAI. After signing in, the localhost page won't load. Copy its full address from the address bar, come back here and paste it.",
        field: "Paste the full address",
        placeholder: "http://localhost:1455/auth/callback?code=…",
      };
    case "anthropic":
      return {
        open: "Open Anthropic",
        before: "Open Anthropic and approve access. Copy the code it shows, come back here and paste it.",
        field: "Paste the code#state value or the callback address",
        placeholder: "code#state",
      };
    default:
      return {
        open: "Get a code",
        before: "Get a code, then approve it on x.ai.",
        field: "",
        placeholder: "",
      };
  }
}
