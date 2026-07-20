import { useState } from "preact/hooks";
import { ChevronDown } from "lucide-preact";
import "./BackgroundJob.css";

// BackgroundJob — background-work strip (async bash) inside the
// conversation stream: lives as a content piece between paragraphs of the
// assistant document, just like FanoutBlock/ActivityLedger — hence why
// it's in components/ and not layout/.
//
// The "peek" expands a mono logtail with the last lines; the open/closed
// state is local (useState) and exposed with aria-expanded on the
// button, same pattern as LedgerRow (ActivityLedger) for collapsible
// rows. Everything lives under a single `.bg-job` root (strip + logtail),
// instead of two loose siblings, to keep the CSS co-located under a single
// component root.
export function BackgroundJob({
  jobId,
  jobLabel = "BG · JOB",
  cmd,
  progress,
  elapsed,
  lines = [],
  defaultOpen = false,
}) {
  const [open, setOpen] = useState(defaultOpen);
  const lastIdx = lines.length - 1;

  return (
    <div class="bg-job" data-live-surface="bash" data-live-id={jobId}>
      <div class="bgjob">
        <span class="tag">{jobLabel}</span>
        <span class="cmd">
          {cmd}
          {progress && <span class="tail"> · {progress}</span>}
        </span>
        {elapsed && <span class="elapsed">{elapsed}</span>}
        <button
          type="button"
          class="peek"
          aria-expanded={open}
          onClick={() => setOpen((v) => !v)}
        >
          peek <ChevronDown size={12} class={`peek-caret${open ? " up" : ""}`} aria-hidden="true" />
        </button>
      </div>

      {open && lines.length > 0 && (
        <div class="logtail" role="log" aria-live="polite" aria-relevant="additions text">
          {lines.map((line, i) => {
            const isLast = i === lastIdx;
            const tone = typeof line === "string" ? undefined : line.tone;
            const text = typeof line === "string" ? line : line.text;
            return (
              <div key={line.id ?? i} class={`ln${tone ? ` t-${tone}` : ""}`}>
                {text}
                {isLast && <span class="ln-cursor" aria-hidden="true" />}
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}
