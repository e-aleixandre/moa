import { useEffect, useRef, useState } from "preact/hooks";
import { Copy } from "lucide-preact";
import { api } from "../../data/api.js";
import { Field } from "../../primitives/index.js";
import { applyProviderStatus, loadProviderStatus, useProviderStatus } from "../../data/providers.js";
import { createRowController, IDLE, newRowScope } from "./provider-row-controller.js";
import { PROGRESS_INTERVAL_MS, copyText, listProviders } from "./providers-flow.js";
import {
  PROVIDER_ROWS, deadlineLabel, flowCopy, kindLabel, replaceNotice, rowActions, rowReading,
} from "./providers-model.js";
import { providerHue } from "./settings-rows.js";
import { AnthropicBackup } from "./AnthropicBackup.jsx";
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
  const actions = row ? rowActions(row, canAdmin) : { signIn: null, apiKey: null, retrySave: false };
  const kind = row && row.source !== "env" ? kindLabel(row.kind) : "";
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

      {idle && (actions.signIn || actions.apiKey || actions.retrySave) && (
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
          {actions.apiKey && (
            <button type="button" class={`zl-prov-btn${actions.primary === "apiKey" ? " is-primary" : ""}`} onClick={() => ctl.startApiKey()}>
              {actions.apiKey}
            </button>
          )}
        </div>
      )}

      {state.step === "signin" && <SignInStart def={def} row={row} state={state} ctl={ctl} />}
      {state.step === "paste" && <PasteStep def={def} state={state} ctl={ctl} inputRef={inputRef} />}
      {state.step === "device" && <DeviceStep state={state} ctl={ctl} />}
      {state.step === "key" && <KeyStep def={def} row={row} state={state} ctl={ctl} inputRef={inputRef} />}
      {idle && canAdmin && row?.actions?.includes("backup") && row.backup && <AnthropicBackup backup={row.backup} onChanged={onChanged} />}
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
      <Notice notice={replaceNotice(row, "oauth")} />
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
