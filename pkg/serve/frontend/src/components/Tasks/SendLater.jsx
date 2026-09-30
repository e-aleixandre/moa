import { useState } from "preact/hooks";
import { AlarmClock } from "lucide-preact";
import { addToast } from "../../data/notifications.js";
import { createTask } from "../../data/tasks.js";
import { errorText } from "../../data/tasks-model.js";
import { DEFAULT_DELIVERY, deviceZone, submitSendLater, whenButton } from "../../data/schedule-model.js";
import { formatShortcut } from "../../data/util/shortcut.js";
import { CloseIcon, Keycap, MOD_ENTER, useEscape } from "./parts.jsx";
import { Delivery, WhenEditor, useNow } from "./Scheduled.jsx";

export const SEND_LATER_SHORTCUT = formatShortcut("↵", { mod: true, shift: true });

// SendLaterButton — the clock beside Send, only while there is a draft.
export function SendLaterButton({ on, disabled, onClick }) {
  return (
    <button
      type="button"
      class={`zl-attach sch-later${on ? " is-on" : ""}`}
      aria-label="Send later"
      aria-expanded={!!on}
      title={`Send later  ${SEND_LATER_SHORTCUT}`}
      disabled={disabled}
      onClick={onClick}
    >
      <AlarmClock size={15} aria-hidden="true" />
    </button>
  );
}

// SendLater — the composer's draft as a scheduled task for this session.
// The draft stays in the box until the server has the schedule; then only
// the text that was scheduled is cleared (onScheduled). A popover over the
// composer on the desktop, the body of a sheet on the phone.
export function SendLater({ sessionId, text, phone, onClose, onScheduled }) {
  const now = useNow();
  const tz = deviceZone();
  const [when, setWhen] = useState(null);
  const [delivery, setDelivery] = useState({ ...DEFAULT_DELIVERY });
  const [busy, setBusy] = useState(false);
  useEscape(!phone, onClose);
  const schedule = async () => {
    if (!when || busy) return;
    setBusy(true);
    const ok = await submitSendLater({ text, when, delivery, sessionId, tz }, {
      create: createTask,
      clear: onScheduled,
      fail: (error) => addToast({ sessionId, title: "Could not schedule it", detail: `${errorText(error)} Your text is still here.`, type: "error" }),
    });
    setBusy(false);
    if (ok) onClose();
  };
  const body = (
    <div
      class={`sch-later-body tk-root${phone ? " is-phone" : ""}`}
      onKeyDown={(e) => { if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) { e.preventDefault(); schedule(); } }}
    >
      <WhenEditor value={when} onChange={setWhen} phone={phone} autoFocus={!phone} onSubmit={schedule} />
      <Delivery value={delivery} onChange={setDelivery} target={{ kind: "session", id: sessionId }} phone={phone} />
      <div class="sch-later-foot">
        {!phone && <button type="button" class="zl-ask-btn is-quiet" onClick={onClose}>Cancel</button>}
        <span class="tk-grow" />
        <button type="button" class={`zl-ask-btn is-primary${phone ? " tk-wide" : ""}`} disabled={!when || busy} onClick={schedule}>
          {when ? `Schedule for ${whenButton(when, now, tz)}` : "Schedule"}{!phone && <Keycap>{MOD_ENTER}</Keycap>}
        </button>
      </div>
    </div>
  );
  if (phone) return body;
  return (
    <div class="sch-later-pop" role="dialog" aria-label="Send later">
      <div class="sch-later-head">
        <span class="sch-later-title">Send later</span>
        <span class="sch-later-draft">{String(text || "").split("\n")[0]}</span>
        <button type="button" class="tk-icon is-quiet" aria-label="Close" onClick={onClose}><CloseIcon /></button>
      </div>
      {body}
    </div>
  );
}
