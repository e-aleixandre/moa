// IncomingCall — an owner asking to talk to you, over whatever is on screen.
// Two buttons and the reason, nothing else: Answer or Write it instead. On a
// desktop it is a card in the top-right corner (no modal: the work behind it
// stays usable); on a phone it takes the screen, as an incoming call does.
// A request held behind a call in progress is not here but in the call's own
// bar (HeldCallNotice).
import { useEffect, useState } from "preact/hooks";
import { createPortal } from "preact/compat";
import { Phone, PenLine, MessageCircle } from "lucide-preact";
import { useStore } from "../../hooks/useStore.js";
import { incomingRequest, incomingTitle, heldRequests, heldTitle } from "../../data/call-requests-model.js";
import { answerCallRequest, writeCallRequest, callHeldRequest } from "../../data/call-requests.js";
import { shouldRing, startRingTone } from "../../data/ring-tone.js";
import { openOwnerConversation } from "../../data/owners.js";
import { OwnerAvatarFor } from "../Owners/OwnerAvatar.jsx";
import "./IncomingCall.css";

function useVisible() {
  const [visible, setVisible] = useState(typeof document === "undefined" || document.visibilityState !== "hidden");
  useEffect(() => {
    const on = () => setVisible(document.visibilityState !== "hidden");
    document.addEventListener("visibilitychange", on);
    return () => document.removeEventListener("visibilitychange", on);
  }, []);
  return visible;
}

export function IncomingCallView({ request, owner, phone, onAnswer, onWrite }) {
  const ring = request.level === "ring";
  return (
    <div
      class={`icall${phone ? " is-phone" : ""}${ring ? " is-ring" : " is-ask"}`}
      role="alertdialog"
      aria-label={incomingTitle(request)}
    >
      <div class="icall-head">
        <span class="icall-face">
          {owner
            ? <OwnerAvatarFor owner={owner} state={ring ? "working" : "idle"} size={phone ? 72 : 40} />
            : <span class="icall-glyph"><MessageCircle size={phone ? 32 : 20} /></span>}
        </span>
        <span class="icall-who">
          <span class="icall-name">{request.owner_name}</span>
          <span class="icall-verb">{ring ? "is calling" : "wants to talk"}</span>
        </span>
      </div>
      {request.reason && <p class="icall-reason">{request.reason}</p>}
      <div class="icall-acts">
        <button type="button" class="icall-answer" onClick={onAnswer}>
          <Phone size={18} aria-hidden="true" /> Answer
        </button>
        <button type="button" class="icall-write" onClick={onWrite}>
          <PenLine size={16} aria-hidden="true" /> Write it instead
        </button>
      </div>
    </div>
  );
}

export function IncomingCall() {
  const request = useStore((s) => incomingRequest(s.callRequests?.list));
  const owners = useStore((s) => s.owners.list);
  const phone = useStore((s) => s.isMobile);
  const visible = useVisible();
  const id = request?.id;
  const ringing = shouldRing(request, visible);
  useEffect(() => (ringing ? startRingTone() : undefined), [ringing, id]);
  if (!request || typeof document === "undefined") return null;
  const owner = (owners || []).find((o) => o.id === request.owner_id) || null;
  const answer = () => {
    if (answerCallRequest(request) && !phone) openOwnerConversation(owner || { session_id: request.session_id });
  };
  return createPortal(
    <IncomingCallView request={request} owner={owner} phone={phone} onAnswer={answer} onWrite={() => writeCallRequest(request.id)} />,
    document.body,
  );
}

// HeldCallNotice — during a call, an owner that wanted to talk. Call hangs up
// this call and calls them; Cancel turns their request into a written question.
export function HeldCallView({ request, compact = false }) {
  return (
    <div class={`hcall${compact ? " is-compact" : ""}`} role="group" aria-label={heldTitle(request)}>
      <div class="hcall-text">
        <span class="hcall-title">{request.owner_name} wanted to talk</span>
        {request.reason && <span class="hcall-reason">{request.reason}</span>}
      </div>
      <div class="hcall-acts">
        <button type="button" class="hcall-call" onClick={() => callHeldRequest(request)}>Call</button>
        <button type="button" class="hcall-cancel" onClick={() => writeCallRequest(request.id)}>Cancel</button>
      </div>
    </div>
  );
}

export function HeldCallNotice({ compact = false }) {
  const request = useStore((s) => heldRequests(s.callRequests?.list)[0] || null);
  return request ? <HeldCallView request={request} compact={compact} /> : null;
}
