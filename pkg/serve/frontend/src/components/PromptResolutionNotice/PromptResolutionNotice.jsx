import { Info } from "lucide-preact";
import "./PromptResolutionNotice.css";

export function promptResolutionText(notice) {
  return notice ? "This request is no longer pending." : "";
}

// PromptResolutionNotice is deliberately transcript-only: it explains why a
// prompt vanished without participating in attention acknowledgement or read
// state.
export function PromptResolutionNotice({ notice }) {
  if (!notice) return null;
  return (
    <div class="prompt-resolution-notice" role="status">
      <Info size={14} strokeWidth={1.8} aria-hidden="true" />
      <span>{promptResolutionText(notice)}</span>
    </div>
  );
}
