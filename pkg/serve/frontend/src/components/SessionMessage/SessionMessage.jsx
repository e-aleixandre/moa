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
import { useStore } from "../../hooks/useStore.js";
import { StateDot } from "../../primitives/StateDot/StateDot.jsx";
import { renderMarkdown } from "../../data/util/markdown.js";
import { sessionDotState } from "../../data/util/format.js";
import "./SessionMessage.css";

// VERBS — the mono provenance line. It names the act, not the tool: `new` is
// the only one that also created the session it is talking to.
const VERBS = { send: "sent to", new: "started", answer: "answered in" };

// shortId is the fallback name for a session this client has never loaded (an
// old transcript, another project's roster). A 32-char id fills the line and
// says nothing more than its head does.
export function shortId(id) {
  const value = String(id || "");
  return value.length > 8 ? value.slice(0, 8) : value;
}

export function SessionMessage({
  action = "send", sessionId = "", text = "", answers = [], askId = "",
  cwd = "", model = "", thinking = "", title = "", queued = false, onOpenSession,
}) {
  // The store is the only place a session id becomes a name. A session that is
  // not loaded keeps its short id rather than an invented title, and its chip
  // does not offer to open what this client cannot open.
  const session = useStore((state) => (sessionId ? state.sessions[sessionId] : null));
  const name = (session?.title || "").trim() || title.trim() || shortId(sessionId);
  const openable = !!session && !!onOpenSession;
  const meta = [cwd, model && `${model}${thinking ? ` · ${thinking}` : ""}`].filter(Boolean);

  return (
    <section class="smsg">
      <div class="smsg-head">
        <span class="smsg-verb">{VERBS[action] || action}</span>
        {sessionId && (
          <button
            type="button"
            class="smsg-target"
            disabled={!openable}
            onClick={() => openable && onOpenSession(sessionId)}
            title={openable ? `Open ${name}` : name}
          >
            <StateDot state={session ? sessionDotState(session) : "saved"} size={7} />
            <span class="smsg-target-name">{name}</span>
          </button>
        )}
        {queued && <span class="smsg-note">queued · read at its next step</span>}
      </div>
      {meta.length > 0 && <div class="smsg-meta">{meta.join(" · ")}</div>}
      {askId && <div class="smsg-meta">question {askId}</div>}
      {text && (
        <div
          class="smsg-body zl-prose"
          dangerouslySetInnerHTML={{ __html: renderMarkdown(text) }}
        />
      )}
      {answers.length > 0 && (
        <ol class="smsg-answers">
          {answers.map((answer, i) => <li key={i}>{answer}</li>)}
        </ol>
      )}
    </section>
  );
}
