import { Info } from "lucide-preact";
import "./PromptResolutionNotice.css";

export function promptResolutionText(notice) {
  if (notice?.kind === "permission") {
    if (notice.outcome === "approved") return "This permission was approved in another client.";
    if (notice.outcome === "denied") return "This permission was denied in another client.";
  }
  if (notice?.kind === "ask" && notice.outcome === "answered") {
    return "This request was answered in another client.";
  }
  return "This request was resolved in another client.";
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
