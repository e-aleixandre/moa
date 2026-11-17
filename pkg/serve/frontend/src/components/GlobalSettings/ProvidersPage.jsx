import { useEffect, useRef, useState } from "preact/hooks";
import { ArrowDown, ArrowUp, Copy } from "lucide-preact";
import { api } from "../../data/api.js";
import { Field } from "../../primitives/index.js";
import { applyProviderStatus, loadProviderStatus, useProviderStatus } from "../../data/providers.js";
import { createRowController, IDLE, newRowScope } from "./provider-row-controller.js";
import { PROGRESS_INTERVAL_MS, accountsUsage, copyText, listProviders } from "./providers-flow.js";
import { usageLevel } from "../../data/util/usage-pills.js";
import {
  PROVIDER_ROWS, accountBars, accountList, accountReading, accountUsageNote, removeAccountWarning, deadlineLabel, flowCopy, planKeyNote, replaceNotice, rowActions, rowKind, rowReading,
} from "./providers-model.js";
import { providerHue } from "./settings-rows.js";
import "./ProvidersPage.css";

// ProvidersPage — Settings → Providers. One row per provider, its state and
// the actions the server allows, and the sign-in or API-key flow opening
// INSIDE the row: nothing floats over the sheet, same markup in both
// densities.
//
// The owner reads GET /api/providers (rows with `actions`); a paired device
// reads the shared /status and gets words only. Anything typed or pasted
// lives in the row's own state and nowhere else.

export function ProvidersPage({ focusProvider = "", returnSessionId = "", onReturn }) {
  const status = useProviderStatus();
  const [list, setList] = useState(null);
  const [readOnly, setReadOnly] = useState(false);
  const [failed, setFailed] = useState(false);

  const reload = () => {
    if (status.canAdmin === false) {
      setReadOnly(true);
      return loadProviderStatus();
    }
    return listProviders(api)
      .then((body) => {
        setList(body);
		setReadOnly(body.can_admin === false);
        setFailed(false);
        applyProviderStatus(body);
      })
      .catch((error) => {
        // A device is refused BY DESIGN: it sees the shared status instead.
        if (error?.status === 401 || error?.status === 403) setReadOnly(true);
        else setFailed(true);
        loadProviderStatus();
      });
  };

  useEffect(() => { reload(); }, [status.canAdmin === false]);

  // A row's flow changed a credential: adopt the row the server returned, or
  // re-read everything when it did not return one.
  const onChanged = (updated) => {
    if (updated && list) {
      setList({ ...list, providers: list.providers.map((p) => (p.id === updated.id ? updated : p)) });
    }
    reload();
    loadProviderStatus();
  };

  const owner = !readOnly && status.canAdmin !== false;
  const rows = owner && list ? list.providers : status.providers;
  const loading = owner ? !list && !failed : !status.loaded;

  return (
    <>
      <p class="zl-set-sum">
        {owner
          ? "Sign in or add an API key. Changes apply to every session on its next request."
          : "Only the owner can change these. Ask them if one needs attention."}
      </p>
      {failed && !list && (
        <p class="zl-set-note is-warn" role="status">Couldn't load providers. Close Settings and open it again.</p>
      )}
      <div class="zl-prov-list" role="list" aria-label="Providers" aria-busy={loading}>
        {PROVIDER_ROWS.map((def) => (
          <ProviderRow
            key={def.id}
            def={def}
            row={(rows || []).find((p) => p.id === def.id) || null}
            canAdmin={owner}
            loading={loading}
            focused={focusProvider === def.id}
            returnSessionId={returnSessionId}
            onReturn={onReturn}
            onChanged={onChanged}
          />
        ))}
      </div>
    </>
  );
}

export function ProviderRow({ def, row, canAdmin, loading, focused, returnSessionId, onReturn, onChanged }) {
  const [state, setState] = useState(IDLE);
  const stateRef = useRef(state);
  stateRef.current = state;
  const rowRef = useRef(row);
  rowRef.current = row;
  const liveRef = useRef(true);
  const scopeRef = useRef(null);
  if (!scopeRef.current) scopeRef.current = newRowScope();
  const inputRef = useRef(null);
  const hostRef = useRef(null);

  const set = (next) => {
    stateRef.current = next;
    if (liveRef.current) setState(next);
  };
  const ctl = createRowController({
    api,
    provider: def.id,
    flow: def.flow,
    row: () => rowRef.current,
    get: () => stateRef.current,
    set,
    onChanged,
    scope: scopeRef.current,
  });

  // Leaving clears what was typed — in state and in the DOM node itself.
  useEffect(() => {
    liveRef.current = true;
    return () => {
      ctl.dispose();
      liveRef.current = false;
      if (inputRef.current) inputRef.current.value = "";
    };
  }, []);

  // Opened from a session's error action: bring this row into view and mark
  // it, without taking focus from the page title the sheet just focused.
  useEffect(() => {
    if (!focused) return;
    hostRef.current?.scrollIntoView?.({ block: "nearest" });
  }, [focused]);

  // Device sign-in: ask where it is every couple of seconds while the page is
  // on screen, at once when it comes back, and never after it ends.
  const attemptId = state.step === "device" ? state.attempt?.attempt_id : "";
  useEffect(() => {
    if (!attemptId) return undefined;
    let timer = null;
    const tick = () => {
      clearTimeout(timer);
      if (document.visibilityState === "visible") ctl.poll();
      timer = setTimeout(tick, PROGRESS_INTERVAL_MS);
    };
    const onVisible = () => { if (document.visibilityState === "visible") tick(); };
    timer = setTimeout(tick, PROGRESS_INTERVAL_MS);
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [attemptId]);

  const reading = loading ? { text: "…", tone: "muted" } : row ? rowReading(row, canAdmin) : { text: "Unavailable", tone: "muted" };
  const actions = row ? rowActions(row, canAdmin) : { signIn: null, addAccount: null, apiKey: null, removeKey: false, retrySave: false };
  const accounts = canAdmin ? accountList(row) : [];
  const keyNote = canAdmin ? planKeyNote(row) : "";
  const kind = row && row.source !== "env" ? rowKind(row) : "";
  const attention = !!row?.attention;
  const idle = state.step === "idle";

  return (
    <div
      class={`zl-prov${focused ? " is-focus" : ""}${idle ? "" : " is-open"}`}
      role="listitem"
      data-provider={def.id}
      ref={hostRef}
    >
      <div class="zl-prov-head">
        <span class="zl-set-prov-mark" style={`--h:${providerHue(def.id)}`} aria-hidden="true">
          {def.name.slice(0, 1)}
        </span>
        <span class="zl-prov-txt">
          <span class="zl-prov-n">
            {def.name}
            {attention && <span class="zl-prov-attn" aria-label="Needs attention" />}
          </span>
          <span class={`zl-prov-d is-${reading.tone}`}>
            {kind && <span class="zl-prov-kind">{kind} · </span>}
            {reading.text}
          </span>
        </span>
      </div>

      {idle && state.message && (
        <p class={`zl-prov-msg is-${state.message.tone}`} role="status">{state.message.text}</p>
      )}
      {idle && state.message?.tone === "ok" && returnSessionId && onReturn && (
        <button type="button" class="zl-prov-btn" onClick={() => onReturn(returnSessionId)}>Return to session</button>
      )}

      {idle && keyNote && <p class="zl-prov-msg is-warn" role="status">{keyNote}</p>}

      {(idle || state.step === "remove") && accounts.length > 0 && (
        <AccountList accounts={accounts} state={state} ctl={ctl} />
      )}
      {state.step === "remove" && <RemoveAccount state={state} ctl={ctl} warning={removeAccountWarning(row)} />}

      {idle && (actions.signIn || actions.addAccount || actions.apiKey || actions.retrySave) && (
        <div class="zl-prov-acts">
          {actions.retrySave && (
            <button type="button" class={`zl-prov-btn${actions.primary === "retrySave" ? " is-primary" : ""}`} disabled={state.busy} onClick={() => ctl.retrySave()}>
              Retry saving
            </button>
          )}
          {actions.signIn && (
            <button type="button" class={`zl-prov-btn${actions.primary === "signIn" ? " is-primary" : ""}`} onClick={() => ctl.startSignIn()}>
              {actions.signIn}
            </button>
          )}
          {actions.addAccount && (
            <button type="button" class="zl-prov-btn" onClick={() => ctl.startSignIn({ target: "new_account" })}>
              {actions.addAccount}
            </button>
          )}
          {actions.apiKey && (
            <button type="button" class={`zl-prov-btn${actions.primary === "apiKey" ? " is-primary" : ""}`} onClick={() => ctl.startApiKey()}>
              {actions.apiKey}
            </button>
          )}
          {actions.removeKey && (
            <button type="button" class="zl-prov-btn" disabled={state.busy} onClick={() => ctl.removeKey()}>
              Remove API key
            </button>
          )}
        </div>
      )}

      {state.step === "signin" && <SignInStart def={def} row={row} state={state} ctl={ctl} />}
      {state.step === "paste" && <PasteStep def={def} state={state} ctl={ctl} inputRef={inputRef} />}
      {state.step === "device" && <DeviceStep state={state} ctl={ctl} />}
      {state.step === "key" && <KeyStep def={def} row={row} state={state} ctl={ctl} inputRef={inputRef} />}
    </div>
  );
}

// Anthropic's plan accounts in priority order: the first that is not exhausted
// serves, the API key comes after them all. Arrows, not dragging: it is a phone.
// Each account shows its own 5h and weekly bars, read once when the list opens
// (and again when the accounts change), never on a timer.
function AccountList({ accounts, state, ctl }) {
  const now = new Date();
  const [usage, setUsage] = useState({});
  const identity = accounts.map((a) => a.generation).join(",");
  useEffect(() => {
    let live = true;
    accountsUsage(api)
      .then((body) => {
        if (!live) return;
        const next = {};
        for (const entry of body?.accounts || []) next[entry.generation] = entry;
        setUsage(next);
      })
      .catch(() => { if (live) setUsage({}); });
    return () => { live = false; };
  }, [identity]);
  return (
    <ul class="zl-prov-accts" aria-label="Anthropic accounts">
      {accounts.map((account, index) => {
        const reading = accountReading(account, now);
        const bars = accountBars(usage[account.generation]);
        const note = bars ? "" : accountUsageNote(usage[account.generation]);
        return (
          <li class="zl-prov-acct" key={account.generation} data-generation={account.generation}>
            <span class="zl-prov-acct-txt">
              <span class="zl-prov-acct-n" title={account.label}>{account.label}</span>
              <span class={`zl-prov-d is-${reading.tone}`}>{reading.text}</span>
              {bars && (
                <span class="zl-prov-bars">
                  {bars.map((bar) => (
                    <span class="zl-prov-bar" key={bar.label}>
                      <span class="zl-prov-bar-k">{bar.label}</span>
                      <span class="zl-prov-bar-track" role="meter" aria-label={`${account.label} ${bar.label}`} aria-valuemin="0" aria-valuemax="100" aria-valuenow={bar.pct}>
                        <i class={`is-${usageLevel(bar.pct)}`} style={{ width: `${Math.min(100, bar.pct)}%` }} />
                      </span>
                      <span class="zl-prov-bar-v">{bar.pct}%</span>
                    </span>
                  ))}
                </span>
              )}
              {note && <span class="zl-prov-d is-muted">{note}</span>}
            </span>
            <span class="zl-prov-acct-acts">
              {account.state === "save_failed" && (
                <button type="button" class="zl-prov-btn is-primary" disabled={state.busy} onClick={() => ctl.retrySave()}>
                  Retry saving
                </button>
              )}
              <button type="button" class={`zl-prov-btn${account.state === "reconnect" ? " is-primary" : ""}`} disabled={state.busy}
                aria-label={`Reconnect ${account.label}`}
                onClick={() => ctl.startSignIn({ target: "account", generation: account.generation })}>
                Reconnect
              </button>
              <button type="button" class="zl-prov-btn is-icon" aria-label={`Move ${account.label} up`}
                disabled={state.busy || index === 0} onClick={() => ctl.moveAccount(account.generation, -1)}>
                <ArrowUp size={14} strokeWidth={1.8} aria-hidden="true" />
              </button>
              <button type="button" class="zl-prov-btn is-icon" aria-label={`Move ${account.label} down`}
                disabled={state.busy || index === accounts.length - 1} onClick={() => ctl.moveAccount(account.generation, 1)}>
                <ArrowDown size={14} strokeWidth={1.8} aria-hidden="true" />
              </button>
              <button type="button" class="zl-prov-btn" aria-label={`Remove ${account.label}`}
                disabled={state.busy} onClick={() => ctl.startRemoveAccount(account)}>
                Remove
              </button>
            </span>
          </li>
        );
      })}
    </ul>
  );
}

function RemoveAccount({ state, ctl, warning }) {
  return (
    <div class="zl-prov-flow">
      <p class="zl-prov-step">Remove {state.account?.label}?</p>
      {warning && <p class="zl-prov-msg is-warn" role="status">{warning}</p>}
      <div class="zl-prov-acts">
        <button type="button" class="zl-prov-btn is-primary" disabled={state.busy} onClick={() => ctl.removeAccount()}>
          {state.busy ? "Removing…" : "Remove account"}
        </button>
        <button type="button" class="zl-prov-btn" disabled={state.busy} onClick={() => ctl.cancel()}>Cancel</button>
      </div>
      <FlowError state={state} />
    </div>
  );
}

function Notice({ notice }) {
  if (!notice) return null;
  return (
    <div class="zl-prov-confirm" role="note">
      <p>{notice.text}</p>
      {notice.billing && <p class="zl-prov-billing">{notice.billing}</p>}
    </div>
  );
}

function FlowError({ state }) {
  if (!state.message) return null;
  return <p class={`zl-prov-msg is-${state.message.tone}`} role="alert">{state.message.text}</p>;
}

function SignInStart({ def, row, state, ctl }) {
  const copy = flowCopy(def.id);
  return (
    <div class="zl-prov-flow">
      {/* Adding or signing an account in again replaces no other credential. */}
      <Notice notice={state.target ? null : replaceNotice(row, "oauth")} />
      <p class="zl-prov-step">{copy.before}</p>
      <div class="zl-prov-acts">
        <button type="button" class="zl-prov-btn is-primary" disabled={state.busy} onClick={() => ctl.open()}>
          {state.busy ? "Starting…" : copy.open}
        </button>
        <button type="button" class="zl-prov-btn" onClick={() => ctl.cancel()}>Cancel</button>
      </div>
      <FlowError state={state} />
    </div>
  );
}

// Paste flows (Anthropic, OpenAI): the authorize page opened in the click; if
// the browser refused the window, a plain link that shares nothing with it.
function PasteStep({ def, state, ctl, inputRef }) {
  const copy = flowCopy(def.id);
  const url = state.attempt?.authorize_url || "";
  const fieldId = `zl-prov-paste-${def.id}`;
  return (
    <form
      class="zl-prov-flow"
      noValidate
      onSubmit={(event) => { event.preventDefault(); ctl.complete(); }}
    >
      {!state.opened && url && (
        <a class="zl-prov-link" href={url} target="_blank" rel="noopener noreferrer" referrerPolicy="no-referrer">
          {copy.open}
        </a>
      )}
      <label class="zl-prov-label" for={fieldId}>{copy.field}</label>
      <Field
        as="textarea"
        id={fieldId}
        class="zl-prov-field is-area"
        rows={3}
        inputRef={inputRef}
        value={state.draft}
        placeholder={copy.placeholder}
        autocomplete="off"
        autoCorrect="off"
        autocapitalize="off"
        spellcheck={false}
        disabled={state.busy}
        onInput={(event) => ctl.setDraft(event.currentTarget.value)}
      />
      <div class="zl-prov-acts">
        <button type="submit" class="zl-prov-btn is-primary" disabled={state.busy || !state.draft.trim()}>
          {state.busy ? "Saving…" : "Complete"}
        </button>
        <button type="button" class="zl-prov-btn" onClick={() => ctl.cancel()}>Cancel</button>
      </div>
      <FlowError state={state} />
    </form>
  );
}

const PROGRESS_COPY = {
  waiting: "Waiting for authorization…",
  exchanging: "Saving…",
};

function DeviceStep({ state, ctl }) {
  const attempt = state.attempt || {};
  const link = attempt.verification_uri_complete || attempt.verification_uri || "";
  const [copied, setCopied] = useState(false);
  return (
    <div class="zl-prov-flow">
      <span class="zl-prov-label">Your code</span>
      <div class="zl-prov-code">
        <span class="zl-prov-code-v">{attempt.user_code}</span>
        <button
          type="button"
          class="zl-prov-btn"
          onClick={() => copyText(attempt.user_code || "").then((ok) => setCopied(ok))}
        >
          <Copy size={13} strokeWidth={1.8} aria-hidden="true" /> {copied ? "Copied" : "Copy code"}
        </button>
      </div>
      {link && (
        <a class="zl-prov-link" href={link} target="_blank" rel="noopener noreferrer" referrerPolicy="no-referrer">
          Open x.ai to approve
        </a>
      )}
      <p class="zl-prov-step is-warn">Authorize only the code you just requested here.</p>
      <p class="zl-prov-step" role="status">
        {PROGRESS_COPY[state.progress] || PROGRESS_COPY.waiting}
        {deadlineLabel(attempt.expires_at) && <span class="zl-prov-deadline"> · {deadlineLabel(attempt.expires_at)}</span>}
      </p>
      <div class="zl-prov-acts">
        <button type="button" class="zl-prov-btn" onClick={() => ctl.cancel()}>Cancel</button>
      </div>
    </div>
  );
}

// The API key: write-only. Empty every time it opens, no reveal, no copy, no
// autofill, cleared the moment it is sent.
function KeyStep({ def, row, state, ctl, inputRef }) {
  const fieldId = `zl-prov-key-${def.id}`;
  const notice = replaceNotice(row, "api_key");
  return (
    <form
      class="zl-prov-flow"
      noValidate
      autocomplete="off"
      onSubmit={(event) => { event.preventDefault(); ctl.saveKey(); }}
    >
      <Notice notice={notice} />
      <label class="zl-prov-label" for={fieldId}>{def.name} API key</label>
      <Field
        id={fieldId}
        type="password"
        class="zl-prov-field"
        size="lg"
        mono
        inputRef={inputRef}
        value={state.draft}
        autocomplete="off"
        autoCorrect="off"
        autocapitalize="off"
        spellcheck={false}
        data-1p-ignore="true"
        data-lpignore="true"
        disabled={state.busy}
        onInput={(event) => ctl.setDraft(event.currentTarget.value)}
      />
      <div class="zl-prov-acts">
        <button type="submit" class="zl-prov-btn is-primary" disabled={state.busy || !state.draft.trim()}>
          {state.busy ? "Saving…" : notice ? "Replace" : "Save"}
        </button>
        <button type="button" class="zl-prov-btn" onClick={() => ctl.cancel()}>Cancel</button>
      </div>
      <FlowError state={state} />
    </form>
  );
}
