// VoiceCall — the call's two surfaces: the flat row in the conversation's
// composer and, on the phone, the full call screen. Both read the one call in
// the store (data/voice-call-session.js); each has exactly one way to hang
// up — the composer's call button turns into it, the screen has End.
import { useEffect, useState } from "preact/hooks";
import { createPortal } from "preact/compat";
import { ChevronDown, Mic, MicOff, PhoneOff, AudioLines, X, KeyRound } from "lucide-preact";
import { useStore } from "../../hooks/useStore.js";
import {
  startVoiceCall, hangupVoiceCall, setVoiceCallMuted, showVoiceCallScreen, dismissVoiceCall,
} from "../../data/voice-call-session.js";
import { callStatus, callMeta, paidAction, inCall } from "../../data/voice-call-view.js";
import { pendingSecrets } from "../../data/secret-action.js";
import { sessionTitle } from "../../data/util/format.js";
import { OwnerAvatarFor } from "../Owners/OwnerAvatar.jsx";
import { AskUserPrompt } from "../AskUserCard/AskUserPrompt.jsx";
import { PermissionPrompt } from "../PermissionCard/PermissionPrompt.jsx";
import "./VoiceCall.css";

export function useCallElapsed(call) {
  const startedAt = call?.startedAt || 0;
  const live = inCall(call) && startedAt > 0;
  const [, tick] = useState(0);
  useEffect(() => {
    if (!live) return undefined;
    const timer = setInterval(() => tick((n) => n + 1), 1000);
    return () => clearInterval(timer);
  }, [live, startedAt]);
  return live ? Math.max(0, Math.floor((Date.now() - startedAt) / 1000)) : 0;
}

// callParty is who is on the other end: the owner whose conversation it is,
// or the session itself.
export function callParty(owners, sessions, sessionId) {
  const owner = (owners || []).find((o) => o.session_id === sessionId) || null;
  const session = sessions?.[sessionId];
  return { owner, name: owner?.name || (session ? sessionTitle(session) : "this session"), session };
}

function PaidButton({ call }) {
  const paid = paidAction(call);
  if (!paid) return null;
  return (
    <button
      type="button"
      class="vcall-paid"
      onClick={() => startVoiceCall(call.sessionId, { provider: paid.provider })}
    >
      {paid.label}
    </button>
  );
}

// VoiceCallBar — the composer's row while this conversation has a call (or
// just had one that could not start). One line when healthy.
export function VoiceCallBar({ call, name, onOpenScreen }) {
  const elapsed = useCallElapsed(call);
  if (!call) return null;
  const active = inCall(call);
  const failed = call.phase === "failed";
  const ended = call.phase === "ended";
  if (ended && !call.message) return null;
  return (
    <div class={`vcall-bar is-${call.phase}`} role="status" aria-live="polite">
      <div class="vcall-bar-head">
        <span class={`vcall-dot is-${call.phase}`} aria-hidden="true" />
        {active && onOpenScreen ? (
          <button type="button" class="vcall-bar-phase is-link" onClick={onOpenScreen}>
            {call.phase === "listening" || call.phase === "speaking" ? "On a call" : callStatus(call, name)}
          </button>
        ) : (
          <span class="vcall-bar-phase">
            {active && (call.phase === "listening" || call.phase === "speaking") ? "On a call" : callStatus(call, name)}
          </span>
        )}
        {active && <span class="vcall-bar-meta">{callMeta(call, elapsed)}</span>}
        <span class="vcall-spring" />
        {active && <span class="vcall-bar-meta" title="Questions the call may ask this conversation">{call.asks}/{call.maxAsks}</span>}
        {active && call.phase !== "starting" && call.phase !== "ending" && (
          <button
            type="button"
            class={`vcall-icon${call.muted ? " is-on" : ""}`}
            aria-pressed={call.muted}
            aria-label={call.muted ? "Unmute" : "Mute"}
            title={call.muted ? "Unmute" : "Mute"}
            onClick={() => setVoiceCallMuted(!call.muted)}
          >
            {call.muted ? <MicOff size={15} /> : <Mic size={15} />}
          </button>
        )}
        {!active && (
          <button type="button" class="vcall-icon" aria-label="Dismiss" title="Dismiss" onClick={dismissVoiceCall}>
            <X size={15} />
          </button>
        )}
      </div>
      {active && call.needsTap && (
        <div class="vcall-bar-tap">Needs your tap — answer the card in this conversation.</div>
      )}
      {failed && <PaidButton call={call} />}
    </div>
  );
}

function LiveCards({ call, party }) {
  const session = party.session;
  const secrets = pendingSecrets(session).length > 0;
  const tapId = call.needsTap?.itemId;
  return (
    <div class="vcall-live">
      {call.phase === "consulting" && (
        <div class="vcall-card">
          <div class="vcall-card-h"><span>Asking {party.name}</span><span class="vcall-spring" /><span>{call.asks}/{call.maxAsks}</span></div>
        </div>
      )}
      {session?.pendingPerm && (
        <div class={`vcall-card is-tap${tapId === session.pendingPerm.id ? " is-asked" : ""}`}>
          <div class="vcall-card-h is-tap">Needs your tap</div>
          <PermissionPrompt session={session} />
        </div>
      )}
      {session?.pendingAsk && (
        <div class={`vcall-card is-tap${tapId === session.pendingAsk.id ? " is-asked" : ""}`}>
          <div class="vcall-card-h is-tap">Needs your tap</div>
          <AskUserPrompt session={session} />
        </div>
      )}
      {secrets && (
        <div class="vcall-card is-secret">
          <KeyRound size={14} aria-hidden="true" />
          <span>A secret is waiting — answer it after the call.</span>
        </div>
      )}
      {call.passed.map((p) => (
        <div class="vcall-card" key={p.deliveryId}>
          <div class="vcall-card-h"><span>Passed to {party.name}</span>{p.queued && <span class="vcall-card-note">after its current step</span>}</div>
          <p class="vcall-card-body">{p.summary}</p>
        </div>
      ))}
    </div>
  );
}

// VoiceCallScreen — the phone's call screen. Face, name, time and billing,
// what waits for a tap, and two buttons: Mute and End.
export function VoiceCallScreen() {
  const call = useStore((s) => s.voiceCall);
  const owners = useStore((s) => s.owners.list);
  const sessions = useStore((s) => s.sessions);
  const elapsed = useCallElapsed(call);
  if (!call?.screen || typeof document === "undefined") return null;
  const party = callParty(owners, sessions, call.sessionId);
  const active = inCall(call);
  const done = call.phase === "ended" || call.phase === "failed";
  const close = () => (done ? dismissVoiceCall() : showVoiceCallScreen(false));
  return createPortal(
    <div class={`vcall-screen is-${call.phase}`} role="dialog" aria-modal="true" aria-label={`Call with ${party.name}`}>
      <button type="button" class="vcall-screen-back" aria-label={done ? "Close" : "Back to the conversation"} onClick={close}>
        {done ? <X size={20} /> : <ChevronDown size={22} />}
      </button>
      <div class="vcall-screen-face">
        {party.owner
          ? <OwnerAvatarFor owner={party.owner} state={call.phase === "speaking" ? "working" : "idle"} size={96} />
          : <span class="vcall-screen-glyph"><AudioLines size={44} /></span>}
      </div>
      <div class="vcall-screen-name">{party.name}</div>
      <div class="vcall-screen-meta">{callMeta(call, elapsed)}</div>
      <div class="vcall-screen-state">{callStatus(call, party.name)}</div>
      {active && <LiveCards call={call} party={party} />}
      {call.phase === "failed" && (
        <div class="vcall-screen-fail"><PaidButton call={call} /></div>
      )}
      <div class="vcall-spring-v" />
      {active ? (
        <div class="vcall-screen-buttons">
          <button
            type="button"
            class={`vcall-round${call.muted ? " is-on" : ""}`}
            aria-pressed={call.muted}
            disabled={call.phase === "starting" || call.phase === "ending"}
            onClick={() => setVoiceCallMuted(!call.muted)}
          >
            <span class="vcall-round-disc">{call.muted ? <MicOff size={22} /> : <Mic size={22} />}</span>
            <span class="vcall-round-label">{call.muted ? "Unmute" : "Mute"}</span>
          </button>
          <button
            type="button"
            class="vcall-round is-end"
            disabled={call.phase === "ending"}
            onClick={() => hangupVoiceCall()}
          >
            <span class="vcall-round-disc"><PhoneOff size={22} /></span>
            <span class="vcall-round-label">End</span>
          </button>
        </div>
      ) : (
        <div class="vcall-screen-buttons">
          <button type="button" class="vcall-round" onClick={close}>
            <span class="vcall-round-disc"><X size={22} /></span>
            <span class="vcall-round-label">Close</span>
          </button>
        </div>
      )}
    </div>,
    document.body,
  );
}
