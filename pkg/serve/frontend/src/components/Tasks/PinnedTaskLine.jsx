import { useEffect, useState } from "preact/hooks";
import { Check } from "lucide-preact";
import { useStore } from "../../hooks/useStore.js";
import { selectSessionDirectory, selectSessionTasks, watchSessionTasks } from "../../data/tasks.js";
import { pinnedLine, sessionRecords } from "../../data/tasks-model.js";
import { CompletionFlow, TasksGlyph } from "./parts.jsx";
import { SchedPinBar, useNow } from "./Scheduled.jsx";
import { deviceZone, schedPin } from "../../data/schedule-model.js";

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

  const now = useNow();
  const pin = pinnedLine(sessionRecords(data, sessionId));
  if (!pin) {
    // Nothing asked of you: what is scheduled into this session takes the
    // same slot ("Scheduled", or "Waiting for you" when a run needs your OK).
    const sched = schedPin(data?.scheduled, now, deviceZone());
    if (!sched) return null;
    return <div class="tk-pin-host"><SchedPinBar pin={sched} phone={phone} onOpenTask={onOpenTask} /></div>;
  }
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
