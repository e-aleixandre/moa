import { useEffect, useRef } from "preact/hooks";
import "./BashJobLog.css";

// BashJobLog — the scrolling output pane of a background bash job's detail
// view, shared verbatim by the desktop BashJobView and the mobile
// MobileBashJobView (one implementation, so the two can't drift).
//
// It wears the same mono/crust logtail skin as BackgroundJob's inline peek —
// the strip in the stream and the full view are the same log, seen closer —
// but it is a scroll container rather than a bounded tail, because this view
// exists precisely to read the whole thing.
//
// While the job is live the pane STICKS TO THE BOTTOM as deltas land (same
// rule as ActivityLedger's live window): scrolling up to read something detaches
// it, scrolling back to the bottom re-attaches. Following the tail must never
// fight a user who is reading.
export function BashJobLog({ lines = [], live = false, hiddenLines = 0 }) {
  const ref = useRef(null);
  const stick = useRef(true);

  useEffect(() => {
    const el = ref.current;
    if (!el || !stick.current) return;
    el.scrollTop = el.scrollHeight;
  }, [lines, live]);

  const lastIdx = lines.length - 1;
  return (
    <div
      ref={ref}
      class="bashlog"
      role="log"
      aria-live="off"
      aria-label="Command output"
      onScroll={(event) => {
        const el = event.currentTarget;
        stick.current = el.scrollHeight - el.scrollTop - el.clientHeight <= 40;
      }}
    >
      {hiddenLines > 0 && (
        <div class="bashlog-elided">… {hiddenLines} earlier lines not shown</div>
      )}
      {lines.length > 0 ? (
        lines.map((line, i) => (
          <div key={i} class="bashlog-ln">
            {line}
            {i === lastIdx && live && <span class="bashlog-cursor" aria-hidden="true" />}
          </div>
        ))
      ) : (
        <div class="bashlog-ln waiting">
          {live ? "waiting for output" : "(no output)"}
          {live && <span class="bashlog-cursor" aria-hidden="true" />}
        </div>
      )}
    </div>
  );
}
