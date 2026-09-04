import { useState } from "preact/hooks";
import { ChevronRight, FileText, Pencil, Scissors } from "lucide-preact";
import "./CompactionCard.css";

export const COMPACTION_SUMMARY_PREVIEW = 1200;

export function compactionSummaryPreview(summary, limit = COMPACTION_SUMMARY_PREVIEW) {
  const text = typeof summary === "string" ? summary : "";
  return text.length > limit ? { text: text.slice(0, limit) + "…", truncated: true } : { text, truncated: false };
}

// compactionClock renders the time of day a compaction happened. Reopening a
// session hours later, when it happened is what places it against the work
// around it. The transcript carries epoch SECONDS (tree.Entry.Timestamp.Unix),
// so it is scaled here like every other timestamp in the UI.
export function compactionClock(timestampSeconds) {
  if (!Number.isFinite(timestampSeconds) || timestampSeconds <= 0) return "";
  return new Date(timestampSeconds * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

function FileList({ title, files, Icon }) {
  if (!files?.length) return null;
  return (
    <section class="cc-files">
      <h4><Icon size={13} aria-hidden="true" /> {title} <span>{files.length}</span></h4>
      <ul>
        {files.map((file, index) => <li key={`${file}-${index}`} title={file}>{file}</li>)}
      </ul>
    </section>
  );
}

// CompactionCard follows ActivityLedger's compact, keyboard-native <button>
// header and recessed detail surface while keeping long summaries/files bounded.
export function CompactionCard({ summary = "", tokensBefore = 0, timestamp = 0, readFiles = [], modifiedFiles = [] }) {
  const [open, setOpen] = useState(false);
  const [fullSummary, setFullSummary] = useState(false);
  const preview = compactionSummaryPreview(summary);
  const tokenLabel = tokensBefore > 0 ? `${Math.round(tokensBefore / 1000)}K tokens summarized` : "Context compacted";
  const shownSummary = fullSummary ? summary : preview.text;
  const clockLabel = compactionClock(timestamp);

  return (
    <section class={`cc${open ? " open" : ""}`}>
      <button type="button" class="cc-header" aria-expanded={open} onClick={() => setOpen(value => !value)}>
        <span class="cc-icon" aria-hidden="true"><Scissors size={14} /></span>
        <span class="cc-title"><b>Context compacted</b>{clockLabel && <span> · {clockLabel}</span>}<span> · {tokenLabel}</span></span>
        <span class="cc-chev" aria-hidden="true"><ChevronRight size={13} /></span>
      </button>
      {open && (
        <div class="cc-detail">
          {summary ? (
            <section class={`cc-summary${fullSummary ? " full" : ""}`}>
              <h4>Summary</h4>
              <pre>{shownSummary}</pre>
              {preview.truncated && (
                <button type="button" class="cc-more" onClick={() => setFullSummary(value => !value)}>
                  {fullSummary ? "Show less" : "Show all"}
                </button>
              )}
            </section>
          ) : <p class="cc-empty">No summary details are available for this older compaction.</p>}
          {tokensBefore > 0 && <p class="cc-tokens">{tokensBefore.toLocaleString()} tokens before compaction</p>}
          <FileList title="Files read" files={readFiles} Icon={FileText} />
          <FileList title="Files modified" files={modifiedFiles} Icon={Pencil} />
        </div>
      )}
    </section>
  );
}
