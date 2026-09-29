import { useEffect, useState } from "preact/hooks";
import { Check } from "lucide-preact";
import { useStore } from "../../hooks/useStore.js";
import { selectSessionDirectory, selectSessionTasks, watchSessionTasks } from "../../data/tasks.js";
import { pinnedLine, sessionRecords } from "../../data/tasks-model.js";
import { CompletionFlow, TasksGlyph } from "./parts.jsx";

// PinnedTaskLine — what this session asked of you, pinned over the composer
// so it does not sink in the transcript: the first open request, "+N" for the
// rest, and Done (with an optional note that the session receives). It reads
// the task database through GET /api/sessions/{id}/tasks and its
// invalidation, never the transcript.
export function PinnedTaskLine({ sessionId, phone = false, onOpenTask }) {
  const data = useStore((s) => selectSessionTasks(s, sessionId));
  const sessions = useStore(selectSessionDirectory);
  const [completing, setCompleting] = useState(null);
  useEffect(() => watchSessionTasks(sessionId), [sessionId]);
  useEffect(() => setCompleting(null), [sessionId]);

  const pin = pinnedLine(sessionRecords(data, sessionId));
  if (!pin) return null;
  const task = pin.task;
  const open = completing === task.id;
  return (
    <div class="tk-pin-host">
      <div class={`tk-pin${phone ? " is-phone" : ""}${open ? " is-open" : ""}`} role="region" aria-label="Asked of you">
        {open ? (
          <CompletionFlow
            task={task}
            sessions={sessions}
            phone={phone}
            onDone={() => setCompleting(null)}
            onCancel={() => setCompleting(null)}
          />
        ) : (
          <div class="tk-pin-bar">
            <span class="tk-pin-ico"><TasksGlyph /></span>
            <span class="tk-pin-k">For you</span>
            <button type="button" class="tk-pin-t" onClick={() => onOpenTask?.(task.id)}>{task.title}</button>
            {pin.more > 0 && <span class="tk-pin-more tk-data" aria-label={`${pin.more} more`}>+{pin.more}</span>}
            <button type="button" class="tk-pin-done" onClick={() => setCompleting(task.id)}>
              <Check size={14} strokeWidth={2.4} aria-hidden="true" />Done
            </button>
          </div>
        )}
      </div>
    </div>
  );
}
