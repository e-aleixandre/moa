import { useEffect, useState } from "preact/hooks";
import { Phone, PhoneMissed } from "lucide-preact";
import { useStore } from "../../hooks/useStore.js";
import { questionCall } from "../../data/for-you.js";
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
