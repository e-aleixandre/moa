import { useState } from "preact/hooks";
import { GitFork, Copy, Check } from "lucide-preact";
import { copyToClipboard } from "../../data/util/format.js";
import "./SubagentDetails.css";

// SubagentDetails — what the "Subagent details" sheet says about a branch, on
// both the desktop and the mobile view.
//
// Deliberately NOT the task prompt: the task is already readable in full in the
// parent's transcript, where it was written, and repeating it here turned the
// sheet into a wall of raw text. What is left is what you cannot read anywhere
// else — who this branch is, how it runs, and the two identifiers you may need
// to paste somewhere (its job ID, and its parent session's).

export function SubagentDetails({ session, view, accent }) {
  const facts = [view.model, view.thinking || "off", view.async ? "background" : "sync"];
  return (
    <div class="sa-details">
      <div class="sa-details-head">
        <GitFork size={14} style={accent ? { color: `var(--${accent})` } : undefined} aria-hidden="true" />
        <b class="sa-details-name" style={accent ? { color: `var(--${accent})` } : undefined}>{view.name}</b>
      </div>
      <div class="sa-details-facts">
        {facts.map((f, i) => (
          <>
            {i > 0 && <span class="sa-details-dot" aria-hidden="true">·</span>}
            <span>{f}</span>
          </>
        ))}
      </div>
      <div class="sa-details-ids">
        <IdRow label="Job ID" value={view.jobId} />
        <IdRow label="Parent session" value={session.id} />
      </div>
    </div>
  );
}

// IdRow — an identifier and the one thing you do with it. The whole row is the
// button, so the tap target is the row and not a word inside it.
function IdRow({ label, value }) {
  const [copied, setCopied] = useState(false);
  const copy = () => copyToClipboard(value).then((ok) => {
    if (!ok) return;
    setCopied(true);
    setTimeout(() => setCopied(false), 1200);
  });
  return (
    <button type="button" class="sa-idrow" onClick={copy} aria-label={`Copy ${label}`}>
      <span class="sa-idrow-key">{label}</span>
      <span class="sa-idrow-val">{value}</span>
      <span class="sa-idrow-act">{copied ? <Check size={14} /> : <Copy size={14} />}</span>
    </button>
  );
}
