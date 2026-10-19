import { useEffect, useState } from "preact/hooks";
import { Phone, PhoneMissed, PhoneOff } from "lucide-preact";
import { useStore } from "../../hooks/useStore.js";
import { canStopQuestionCall, questionCall, stopQuestionCall } from "../../data/for-you.js";
import { addToast } from "../../data/notifications.js";
import { callRequestsSlice } from "../../data/call-requests.js";
import "./QuestionCall.css";

// QuestionCall — the call an open question escalates to, in words: "Calls you
// in 8 min" (counting down by the minute), "Calling you…", "Missed call".
// Nothing when the question will not call.
export function QuestionCall({ item }) {
  const requests = useStore((s) => callRequestsSlice(s).list);
  const [now, setNow] = useState(Date.now());
  const pending = item?.call_state === "pending";
  useEffect(() => {
    if (!pending) return undefined;
    const t = setInterval(() => setNow(Date.now()), 15000);
    return () => clearInterval(t);
  }, [pending]);
  const call = questionCall(item, now, requests);
  if (!call) return null;
  const Icon = call.state === "missed" ? PhoneMissed : Phone;
  return (
    <span class={`qc-call is-${call.tone}`} data-call={call.state}>
      <Icon size={12} aria-hidden="true" />{call.text}
    </span>
  );
}

// StopQuestionCall — "Don't call": stops an idle session's question call
// while the question stays open. Nothing when there is no call to stop or
// the session is running (its live bar has Stop).
export function StopQuestionCall({ item }) {
  const session = useStore((s) => s.sessions?.[item?.session_id]);
  const [busy, setBusy] = useState(false);
  if (!canStopQuestionCall(item, session)) return null;
  const stop = (e) => {
    e.stopPropagation();
    setBusy(true);
    stopQuestionCall(item.session_id)
      .catch(() => addToast({ title: "Could not cancel the call", detail: "Try again.", type: "error" }))
      .finally(() => setBusy(false));
  };
  return (
    <button type="button" class="qc-stop" disabled={busy} onClick={stop}>
      <PhoneOff size={12} aria-hidden="true" />Don't call
    </button>
  );
}
