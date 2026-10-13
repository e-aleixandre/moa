import { useEffect, useRef, useState } from "preact/hooks";
import { KeyRound, Lock, ScanFace } from "lucide-preact";
import { Field } from "../../primitives/index.js";
import {
  browserMapping, fieldAutocomplete, fieldInputName, requesterLabel, secretCopy,
} from "../../data/secret-action.js";
import { savedSecretsBridge } from "../../data/saved-secrets.js";
import { autofocusAllowed, createSecretCard } from "../../data/secret-card.js";
import "../PermissionCard/PermissionCard.css";
import "./SecretActionCard.css";

// SecretActionCard — the blocking "this action needs a password" card. It has
// the permission card's look and its slot (the conversation's blocking strip):
// the card IS the approval, so there is no separate Allow step before it.
//
// Where the value lives: in the card controller (data/secret-card.js) and in
// the POST body, and nowhere else. The card is keyed by request id, so a new
// request starts empty and a resolved one takes its state with it; the values
// are also dropped the moment they are handed to `send`, whether the server
// accepted them or not. This file is the view: SecretActionView draws a
// controller's state and holds no hooks; SecretActionCard is the thin shell
// that re-renders it when the controller changes.
//
// The native "Save on this iPhone" parts only exist when the iOS app injected
// window.moaSavedSecrets. On the web none of it renders.

function Well({ request }) {
  if (request.kind === "browser") {
    const rows = browserMapping(request);
    return (
      <div class="zl-ask-cmd zl-sx-well">
        <div class="zl-sx-origin zl-data">
          <Lock size={13} aria-hidden="true" />
          <span>{request.origin}</span>
        </div>
        <div class="zl-sx-map">
          {rows.map((row, i) => (
            <div class="zl-sx-map-row" key={i}>
              <span class="zl-sx-map-k">{row.label}</span>
              <span class="zl-sx-map-v">→ {row.text}</span>
            </div>
          ))}
          {request.submit && (
            <div class="zl-sx-map-row">
              <span class="zl-sx-map-k">Then</span>
              <span class="zl-sx-map-v">presses Enter</span>
            </div>
          )}
        </div>
      </div>
    );
  }
  return (
    <div class="zl-ask-cmd zl-sx-well">
      {request.cwd && <div class="zl-ask-cwd zl-data">{request.cwd}</div>}
      <code class="zl-ask-cmd-text zl-data">{request.command}</code>
      {request.sshHost && (
        <span class="zl-ask-chip zl-sx-host"><span aria-hidden="true">⇢</span><span>runs on {request.sshHost} via SSH</span></span>
      )}
    </div>
  );
}

function SavedRow({ field, request, onTypeAnother, disabled }) {
  return (
    <div class="zl-sx-saved" data-field={field.index}>
      <span class="zl-sx-saved-ic" aria-hidden="true"><ScanFace size={18} /></span>
      <span class="zl-sx-saved-txt">
        <b>{field.alias}</b>
        <small>Saved on this iPhone · same {request.kind === "browser" ? "page" : "command"} as last use</small>
      </span>
      <button type="button" class="zl-sx-link" disabled={disabled} onClick={onTypeAnother}>Type another</button>
    </div>
  );
}

// SecretActionView — pure drawing of one card from its controller.
export function SecretActionView({ session, request, position = 1, total = 1, card, bridge = null, firstRef }) {
  const { state } = card;
  const d = card.derive();
  const copy = secretCopy(request);
  const who = requesterLabel(request.requester, session?.subagents);
  const { busy, error, saveOn } = state;
  const primaryLabel = d.savedRows.size > 0 ? "Allow with Face ID" : copy.primary;

  return (
    <form
      class="zl-ask zl-sx"
      role="group"
      aria-label="Password requested"
      autocomplete="on"
      noValidate
      onSubmit={(event) => { event.preventDefault(); if (d.ready) card.submit(); }}
    >
      <div class="zl-ask-head">
        <span class="zl-ask-glyph" aria-hidden="true"><KeyRound size={15} /></span>
        <span class="zl-ask-t">{copy.title}</span>
        {total > 1 && <span class="zl-sx-count">{position} of {total}</span>}
        {who && <span class="zl-sx-who">{who}</span>}
      </div>
      <Well request={request} />
      <div class="zl-sx-fields">
        {request.fields.map((field) => {
          if (d.savedRows.has(field.index)) {
            return (
              <SavedRow
                key={field.index}
                field={field}
                request={request}
                disabled={busy}
                onTypeAnother={() => card.typeInstead(field.index)}
              />
            );
          }
          const password = field.kind === "password";
          const shown = !!state.revealed[field.index];
          const id = `sx-${request.id}-${field.index}`;
          return (
            <div class="zl-sx-field" key={field.index}>
              <label class="zl-sx-label" for={id}>
                {field.label}
                {bridge && field.alias && <span class="zl-sx-alias"> · {field.alias}</span>}
              </label>
              <Field
                variant="box"
                size="lg"
                class="zl-sx-input"
                inputRef={d.target && d.target.index === field.index ? firstRef : undefined}
                id={id}
                name={fieldInputName(field)}
                type={password && !shown ? "password" : "text"}
                autocomplete={fieldAutocomplete(field.kind)}
                autocapitalize="off"
                autocorrect="off"
                spellcheck="false"
                placeholder="Paste or type"
                value={state.values[field.index] || ""}
                onInput={(e) => card.setValue(field.index, e.currentTarget.value)}
                disabled={busy}
                trailing={password && (
                  <button
                    type="button"
                    class="zl-sx-show"
                    aria-pressed={shown}
                    aria-label={`${shown ? "Hide" : "Show"} ${field.label}`}
                    onClick={() => card.toggleReveal(field.index)}
                  >
                    {shown ? "Hide" : "Show"}
                  </button>
                )}
              />
              {d.differs(field) && (
                <div class="zl-sx-diff">
                  <span class="zl-sx-warn">Different from the saved one</span>
                  <button type="button" class="zl-ask-btn zl-sx-fill" disabled={busy} onClick={() => card.fillSaved(field)}>
                    Fill saved (Face ID)
                  </button>
                </div>
              )}
            </div>
          );
        })}
      </div>
      {bridge && d.saveable.length > 0 && (
        <label class="zl-sx-save">
          <button
            type="button"
            class={`zl-sw${saveOn ? " is-on" : ""}`}
            role="switch"
            aria-checked={saveOn}
            aria-label="Save on this iPhone"
            disabled={busy}
            onClick={() => card.toggleSave()}
          >
            <i aria-hidden="true" />
          </button>
          <span>Save on this iPhone <span class="zl-sx-faint">(Face ID to use)</span></span>
        </label>
      )}
      {d.savedRows.size === 0 && (
        <div class="zl-sx-note">{request.kind === "browser" ? null : <Lock size={12} aria-hidden="true" />} {copy.note}</div>
      )}
      {error && <div class="zl-ask-error" role="alert">{error}</div>}
      <div class="zl-ask-acts">
        <button type="button" class="zl-ask-btn" disabled={busy} onClick={() => card.deny()}>Deny</button>
        <button type="submit" class="zl-ask-btn is-primary" disabled={!d.ready}>
          {primaryLabel}
          <kbd class="zl-ask-key" aria-hidden="true">⏎</kbd>
        </button>
      </div>
    </form>
  );
}

export function SecretActionCard({ session, request, position, total, send }) {
  const [, setTick] = useState(0);
  const cardRef = useRef(null);
  const firstRef = useRef(null);
  const bridge = savedSecretsBridge();
  if (!cardRef.current) {
    cardRef.current = createSecretCard({ request, send, bridge, onChange: () => setTick((n) => n + 1) });
  }
  const card = cardRef.current;

  useEffect(() => {
    if (autofocusAllowed()) firstRef.current?.focus?.({ preventScroll: true });
    card.init();
    return () => card.dispose();
  }, []);

  return (
    <SecretActionView
      session={session}
      request={request}
      position={position}
      total={total}
      card={card}
      bridge={bridge}
      firstRef={firstRef}
    />
  );
}
