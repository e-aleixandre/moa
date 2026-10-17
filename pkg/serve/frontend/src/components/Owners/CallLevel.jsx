import { useEffect, useRef, useState } from "preact/hooks";
import { api } from "../../data/api.js";
import { openProviderSettings } from "../../data/providers.js";
import { ownerCallLevel, callLevelPlan, setOwnerCallLevel, callLevelFailure, CALL_LEVELS, CALL_LEVEL_HELP } from "../../data/owner-calls.js";
import "./CallLevel.css";

/* CallLevel — whether this owner may ask to call you: Never / Ask first / Ring.

   It applies the moment a level is chosen (it is its own request, not part of
   Edit owner's Save). With no voice plan the choice first asks for the
   per-minute cost, in place — no modal over the owner's panel — and only
   Confirm sends it; Cancel leaves the level as it was. */

export function CallLevelField({ owner, status, onProviders, onRejected, label = true, hint = true }) {
  const current = ownerCallLevel(owner);
  const [pending, setPending] = useState(null);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState("");
  // The confirmation and the failure appear under the selector, which on
  // Edit owner sits at the bottom of a scrolled panel: bring them into view.
  const shownRef = useRef(null);
  useEffect(() => {
    shownRef.current?.scrollIntoView?.({ block: "nearest" });
  }, [pending, failure]);

  const apply = async (level, ack = "") => {
    setBusy(true);
    setFailure("");
    try {
      await setOwnerCallLevel(owner.id, level, ack);
      setPending(null);
    } catch (error) {
      setPending(null);
      setFailure(callLevelFailure(error));
      // A rejection can mean the paid provider changed elsewhere: reread it
      // so the next choice confirms the current cost.
      if (error?.status === 400) onRejected?.();
    } finally {
      setBusy(false);
    }
  };

  const choose = (level) => {
    if (busy || level === current) return;
    setFailure("");
    const plan = callLevelPlan(status, owner, level);
    if (plan.kind === "confirm") setPending({ level, ...plan });
    else if (plan.kind === "blocked") { setPending(null); setFailure(plan.text); }
    else { setPending(null); apply(level); }
  };

  return (
    <div class="ow-field cl-field">
      {label && <span class="ow-label" id={`cl-${owner.id}`}>Calls from this owner</span>}
      <div class="cl-seg" role="radiogroup" aria-label={`Calls from ${owner.name}`} aria-labelledby={label ? `cl-${owner.id}` : undefined}>
        {CALL_LEVELS.map((l) => (
          <button
            key={l.id}
            type="button"
            role="radio"
            aria-checked={current === l.id}
            class={`cl-opt${current === l.id ? " is-on" : ""}${pending?.level === l.id ? " is-asking" : ""}`}
            disabled={busy}
            onClick={() => choose(l.id)}
          >
            {l.label}
          </button>
        ))}
      </div>
      {hint && <p class="ow-hint">{CALL_LEVEL_HELP}</p>}
      {pending && (
        <div ref={shownRef} class="cl-confirm" role="alertdialog" aria-label="Confirm call cost">
          <p class="cl-confirm-t">{pending.text}</p>
          <div class="cl-confirm-acts">
            <button type="button" class="cl-yes" disabled={busy} onClick={() => apply(pending.level, pending.provider)}>
              {busy ? "Saving…" : "Confirm"}
            </button>
            <button type="button" class="cl-no" disabled={busy} onClick={() => setPending(null)}>Cancel</button>
          </div>
        </div>
      )}
      {failure && (
        <p ref={pending ? undefined : shownRef} class="ow-fail cl-fail" role="alert">
          {failure}
          {onProviders && failure.includes("Providers") && (
            <> <button type="button" class="cl-link" onClick={onProviders}>Open Providers</button></>
          )}
        </p>
      )}
    </div>
  );
}

// OwnerCallLevel — the field on Edit owner, which reads the voice status
// itself: it names the paid provider and its price when no plan is signed in.
export function OwnerCallLevel({ owner }) {
  const [status, setStatus] = useState(null);
  const [reads, setReads] = useState(0);
  useEffect(() => {
    let live = true;
    api("GET", "/api/voice/status").then((s) => { if (live) setStatus(s); }).catch(() => {});
    return () => { live = false; };
  }, [owner.id, reads]);
  return (
    <CallLevelField
      owner={owner}
      status={status}
      onRejected={() => setReads((n) => n + 1)}
      onProviders={() => openProviderSettings(status?.owner_calls_paid_provider || "")}
    />
  );
}
