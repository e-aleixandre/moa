// SessionMessage — what an owner said to one of its sessions.
//
// The owner's `sessions` tool used to land in the ledger like any other call,
// and the row read "sessions · send · Sent to 66f1c2… ()": neither who was
// addressed nor what was said. Both are the whole content of the act. So it
// stops being a tool row and becomes what it is — a message to somebody —
// with the target named (and openable) and the text rendered as the markdown
// the session actually received.
//
// It is NOT a UserWaypoint: the owner is not the user, and the left rule is
// the user's mark (CRITERIO-VISUAL §1). It is the ledger's own surface, the
// same one every other piece of this turn's work sits on.
import { useState, useRef, useLayoutEffect } from "preact/hooks";
import { ChevronRight } from "lucide-preact";
import { renderMarkdown } from "../../data/util/markdown.js";
import { ownerMessageSummary, ownerMessageFolds } from "../../data/util/owner-message.js";
import { SessionChip, shortId } from "../SessionChip/SessionChip.jsx";
import "./SessionMessage.css";

// VERBS — the mono provenance line. It names the act, not the tool: `new` is
// the only one that also created the session it is talking to.
const VERBS = { send: "sent to", new: "started", answer: "answered in" };

// Re-exported: callers and tests reached it through this module first.
export { shortId };

export function SessionMessage({
  action = "send", sessionId = "", text = "", answers = [], askId = "",
  cwd = "", model = "", thinking = "", title = "", queued = false, onOpenSession,
  // Called with the disclosure's node once the message is open, so the
  // transcript can keep it where it was tapped (see stream-scroll.js).
  onExpand,
}) {
  const meta = [cwd, model && `${model}${thinking ? ` · ${thinking}` : ""}`].filter(Boolean);
  // What the owner SENT is an assignment, and an assignment is long: measured
  // in the owner's own conversation, one of these blocks was 3705px on an
  // 844px phone. The head is not the problem — the verb, the session it went
  // to, the folder, the model and the thinking are exactly what the reader
  // wants at a glance — so the head stays and the message folds under it.
  //
  // An `answer` carries no text at all — the projection gives it the answers
  // and the question id and nothing else — so it is measured on what it
  // actually holds, and it says what it holds. Calling that "The message"
  // would name an outbound message that does not exist.
  const answered = answers.join(" ");
  const folds = ownerMessageFolds(text) || ownerMessageFolds(answered);
  const summary = ownerMessageSummary(text)
    || (answers.length > 0 ? `${answers.length} ${answers.length === 1 ? "answer" : "answers"}` : "");
  const [open, setOpen] = useState(false);
  const folded = folds && !open;
  const discRef = useRef(null);
  useLayoutEffect(() => {
    if (open && discRef.current && onExpand) onExpand(discRef.current);
  }, [open]);

  return (
    <section class="smsg">
      <div class="smsg-head">
        <span class="smsg-verb">{VERBS[action] || action}</span>
        {sessionId && <SessionChip sessionId={sessionId} title={title} onOpen={onOpenSession} />}
        {queued && <span class="smsg-note">queued · read at its next step</span>}
      </div>
      {meta.length > 0 && <div class="smsg-meta">{meta.join(" · ")}</div>}
      {askId && <div class="smsg-meta">question {askId}</div>}
      {folds && (
        <button
          type="button"
          class="smsg-disc"
          ref={discRef}
          aria-expanded={open}
          onClick={() => setOpen((value) => !value)}
        >
          <span class="smsg-sum">{summary}</span>
          <span class="smsg-chev" aria-hidden="true"><ChevronRight size={14} /></span>
        </button>
      )}
      {text && !folded && (
        <div
          class="smsg-body zl-prose"
          dangerouslySetInnerHTML={{ __html: renderMarkdown(text) }}
        />
      )}
      {answers.length > 0 && !folded && (
        <ol class="smsg-answers">
          {answers.map((answer, i) => <li key={i}>{answer}</li>)}
        </ol>
      )}
    </section>
  );
}
