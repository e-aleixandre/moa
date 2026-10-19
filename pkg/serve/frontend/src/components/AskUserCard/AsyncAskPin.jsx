import { useEffect, useState } from "preact/hooks";
import { ChevronDown, Lock, MessageCircleQuestionMark } from "lucide-preact";
import { AskUserPrompt } from "./AskUserPrompt.jsx";
import "./AsyncAskPin.css";

// AsyncAskPin — an open asynchronous question, pinned over the composer the
// way a scheduled run or a request is (Tasks' PinnedTaskLine): one line that
// says what was asked, while the session keeps working and the transcript
// stays readable. Answer opens the card in place, inside the pin; Later folds
// it back. A blocking question keeps its full card in the transcript.
export function AsyncAskPin({ session, phone = false }) {
  const ask = session?.pendingAsk;
  const [open, setOpen] = useState(false);
  useEffect(() => setOpen(false), [ask?.id]);
  if (!ask?.async || !(ask.questions?.length > 0)) return null;
  const first = ask.questions[0];
  const more = ask.questions.length - 1;
  const tap = !!ask.requires_tap;
  return (
    <div class="tk-pin-host">
      <div
        class={`tk-pin aq-pin${phone ? " is-phone" : ""}${open ? " is-open" : ""}`}
        role="region"
        aria-label="Question for you"
      >
        {open ? (
          <>
            <div class="aq-pin-head">
              <span class="aq-pin-ico"><MessageCircleQuestionMark size={15} aria-hidden="true" /></span>
              <span class="tk-pin-k">{more > 0 ? `${ask.questions.length} questions` : "Question"}</span>
              {tap && <span class="aq-pin-tap"><Lock size={12} aria-hidden="true" />Needs your tap</span>}
              <span class="aq-pin-spring" />
              <button type="button" class="aq-pin-later" onClick={() => setOpen(false)} aria-label="Answer later">
                Later<ChevronDown size={14} aria-hidden="true" />
              </button>
            </div>
            <div class="aq-pin-body">
              <AskUserPrompt key={ask.id} session={session} compact />
            </div>
          </>
        ) : (
          <div class="tk-pin-bar" onClick={() => setOpen(true)}>
            <span class="aq-pin-ico"><MessageCircleQuestionMark size={15} aria-hidden="true" /></span>
            <button type="button" class="tk-pin-t" onClick={(e) => { e.stopPropagation(); setOpen(true); }}>
              {first.question}
            </button>
            {more > 0 && <span class="tk-pin-more tk-data" aria-label={`${more} more`}>+{more}</span>}
            {tap && !phone && <span class="aq-pin-tap"><Lock size={12} aria-hidden="true" />Needs your tap</span>}
            <button type="button" class="tk-pin-done aq-pin-answer" onClick={(e) => { e.stopPropagation(); setOpen(true); }}>
              Answer
            </button>
          </div>
        )}
      </div>
    </div>
  );
}
