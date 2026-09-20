// VoiceCallBlock — one voice call, seen from inside the conversation it
// interrupted.
//
// While a call runs, the delegate can ask this session (ask_session). The
// question reaches the model as a user-role message because that is the only
// role it can arrive in — and that is exactly why it must not be DRAWN as one:
// a transcript reopened five days later would read the owner asking his own
// questions and the session answering them. Nobody typed those lines.
//
// So it borrows EventBlock's grammar — glyph in the gutter, mono provenance
// line, one-line summary, chevron, collapsed by default — because that grammar
// already reads as "this arrived, you did not say it". What it adds is the
// pairing: the whole call is ONE block, and inside it every question is
// labelled with who spoke. No peach anywhere: peach is the owner's colour, and
// the owner is not in this conversation.
import { useState } from "preact/hooks";
import { ChevronRight, AudioLines } from "lucide-preact";
import { eventAge } from "../EventBlock/EventBlock.jsx";
import "./VoiceCallBlock.css";

// What the collapsed block says happened, in one line.
export function voiceCallSummary(exchanges = []) {
  const n = Array.isArray(exchanges) ? exchanges.length : 0;
  if (n === 0) return "A voice delegate called this session";
  return `The voice delegate asked this session ${n === 1 ? "1 question" : `${n} questions`}`;
}

// An exchange whose answer never landed in the transcript: the call ended
// first, or the run produced no text. Saying so is better than a blank line
// pretending the session said nothing. An exchange being answered RIGHT NOW is
// a different thing and must not borrow this copy, nor count as unanswered.
export const VOICE_ANSWER_PENDING = "No answer recorded in this conversation";
export const VOICE_ANSWER_WORKING = "Answering…";

export function voiceCallUnanswered(exchanges = []) {
  return (Array.isArray(exchanges) ? exchanges : []).filter((x) => !x?.answer && !x?.streaming).length;
}

export function VoiceCallBlock({ exchanges = [], time }) {
  const [open, setOpen] = useState(false);
  const age = eventAge(time);
  const unanswered = voiceCallUnanswered(exchanges);

  return (
    <section class={`vcb${open ? " open" : ""}`}>
      <button
        type="button"
        class="vcb-head"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
      >
        <span class="vcb-id">
          <span class="vcb-meta">
            <span class="vcb-glyph" aria-hidden="true"><AudioLines size={13} /></span>
            <span class="vcb-source">voice call</span>
            {age && <span>· {age}</span>}
            {unanswered > 0 && <span>· {unanswered} unanswered</span>}
          </span>
          <span class="vcb-title">{voiceCallSummary(exchanges)}</span>
        </span>
        <span class="vcb-chev" aria-hidden="true"><ChevronRight size={14} /></span>
      </button>
      {open && (
        <ol class="vcb-log">
          {exchanges.map((exchange) => (
            <li class="vcb-x" key={exchange.id}>
              <div class="vcb-line">
                <span class="vcb-who">The delegate asked</span>
                <p class="vcb-said">{exchange.question}</p>
              </div>
              <div class="vcb-line is-answer">
                <span class="vcb-who">This session answered</span>
                <p class={`vcb-said${exchange.answer ? "" : " is-pending"}`}>
                  {exchange.answer || (exchange.streaming ? VOICE_ANSWER_WORKING : VOICE_ANSWER_PENDING)}
                </p>
              </div>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}
