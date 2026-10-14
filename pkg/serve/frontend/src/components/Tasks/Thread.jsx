import { useEffect, useRef, useState } from "preact/hooks";
import { User } from "lucide-preact";
import { useStore } from "../../hooks/useStore.js";
import { addToast } from "../../data/notifications.js";
import { postComment, setThreadDraft, tasksSlice, threadDraftOf, threadOf, watchThread } from "../../data/tasks.js";
import { commentAuthor, errorText, handoffSide, relAge, sessionName, threadPlaceholder } from "../../data/tasks-model.js";
import { Keycap, MOD_ENTER, OwnerMark } from "./parts.jsx";
import { TasksGlyph } from "./TasksGlyph.jsx";

// The thread of a task (one per task, Linear's Activity): what opened it,
// then every comment in order, oldest first. The composer sits in the
// detail's foot. Comments do not move the task's revision, so the thread is
// read again on every tasks_changed while it shows (watchThread).

const age = (at) => (relAge(at) === "now" ? "now" : `${relAge(at)} ago`);

function Face({ author, small }) {
  if (author.kind === "owner") return <span class="tk-c-face"><OwnerMark side={author} size={small ? 18 : 22} /></span>;
  return (
    <span class={`tk-c-av${small ? " is-small" : ""}`} aria-hidden="true">
      {author.kind === "human" ? <User size={small ? 10 : 12} strokeWidth={2.2} /> : <TasksGlyph />}
    </span>
  );
}

function Opened({ task, owners, sessions }) {
  let who;
  let text;
  if (task.handoff) {
    const from = handoffSide(task, "from", owners);
    const to = handoffSide(task, "to", owners);
    who = { kind: "owner", name: from.name, owner: from.owner || { name: from.name } };
    text = task.handoff.state === "sent"
      ? <><b>{from.name}</b> sent this to <b>{to.name}</b></>
      : <><b>{from.name}</b> asked to send this to <b>{to.name}</b></>;
  } else {
    const name = sessionName(sessions, task.requester_session_id);
    who = sessions[task.requester_session_id]?.kind === "owner"
      ? { kind: "owner", name, owner: owners.find((o) => o.session_id === task.requester_session_id) || { name } }
      : { kind: "session", name };
    text = <><b>{name}</b> asked you</>;
  }
  return (
    <div class="tk-mark">
      <Face author={who} small />
      <span class="tk-mark-t">{text}</span>
      {task.created_at > 0 && <span class="tk-c-at tk-data">{age(task.created_at)}</span>}
    </div>
  );
}

export function useThread(taskId, on) {
  const slice = useStore(tasksSlice);
  useEffect(() => (on ? watchThread(taskId) : undefined), [taskId, on]);
  return on ? threadOf(slice, taskId) : null;
}

export function Thread({ task, thread, owners, sessions }) {
  const comments = thread?.comments || [];
  const end = useRef(null);
  const seen = useRef(comments.length);
  // A comment arriving while the thread shows scrolls into view, as a new
  // message would; opening the task does not jump to the end.
  useEffect(() => {
    if (comments.length > seen.current) end.current?.scrollIntoView({ block: "nearest", behavior: "smooth" });
    seen.current = comments.length;
  }, [comments.length]);
  return (
    <section class="tk-block tk-activity" aria-label="Activity">
      <div class="tk-block-h">Activity</div>
      <Opened task={task} owners={owners} sessions={sessions} />
      {comments.map((c) => {
        const author = commentAuthor(c, task, { owners, sessions });
        return (
          <div key={c.id} class="tk-c">
            <Face author={author} />
            <div class="tk-c-main">
              <div class="tk-c-head">
                <span class="tk-c-who">{author.name}</span>
                {c.created_at > 0 && <span class="tk-c-at tk-data">{age(c.created_at)}</span>}
              </div>
              <p class="tk-c-body">{c.body}</p>
            </div>
          </div>
        );
      })}
      <div ref={end} />
    </section>
  );
}

function autosize(el) {
  if (!el) return;
  el.style.height = "auto";
  el.style.height = `${el.scrollHeight}px`;
}

// ThreadComposer — a comment goes out at once to whoever the task's thread
// reaches (the server decides). Send shows once there is something to send.
export function ThreadComposer({ task, owners, sessions, phone }) {
  const text = useStore((st) => threadDraftOf(tasksSlice(st), task.id));
  const [busy, setBusy] = useState(false);
  const field = useRef(null);
  const placeholder = threadPlaceholder(task, { owners, sessions });
  const send = () => {
    const body = text.trim();
    if (!body || busy) return;
    setBusy(true);
    postComment(task.id, body)
      .then(() => { setThreadDraft(task.id, ""); requestAnimationFrame(() => autosize(field.current)); })
      .catch((error) => addToast({ title: "Could not send the comment", detail: errorText(error), type: "error" }))
      .finally(() => setBusy(false));
  };
  return (
    <div class={`tk-composer${phone ? " is-phone" : ""}`}>
      <textarea
        ref={(el) => { field.current = el; autosize(el); }}
        class="tk-field tk-composer-f"
        rows={1}
        placeholder={placeholder}
        aria-label={placeholder}
        value={text}
        onInput={(e) => { setThreadDraft(task.id, e.currentTarget.value); autosize(e.currentTarget); }}
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey || e.altKey)) { e.preventDefault(); e.stopPropagation(); send(); }
        }}
      />
      {text.trim() && (
        <div class="tk-composer-acts">
          <span class="tk-grow" />
          <button type="button" class="zl-ask-btn is-primary" disabled={busy} onClick={send}>
            Send{!phone && <Keycap>{MOD_ENTER}</Keycap>}
          </button>
        </div>
      )}
    </div>
  );
}
