import { Button, Spinner } from "../../primitives/index.js";
import { StreamingSkeleton } from "../StreamingSkeleton/StreamingSkeleton.jsx";
import "./HistoryHydrationTail.css";

export function historyHydrationTailVisible(session) {
  return !!(session?.historyStale || (
    session?.historyPending && session.historyTailNeeded && session.historyTailReady
  ));
}

// HistoryHydrationTail reserves a visible tail while the socket's init snapshot
// replaces a cached transcript. A session with no cache gets a full stream
// skeleton, so the empty surface reads as a conversation loading in place.
export function HistoryHydrationTail({ hasCachedTranscript, stale = false, onRetry }) {
  if (stale) {
    return (
      <div class={`history-hydration history-hydration--stale${hasCachedTranscript ? "" : " history-hydration--empty"}`} role="status">
        <div class="history-hydration-rule">
          <span class="history-hydration-label">Couldn’t refresh</span>
        </div>
        <div class="history-hydration-stale-copy">
          {hasCachedTranscript
            ? "Showing the last version you saw. Retrying automatically."
            : "Retrying automatically."}
          {onRetry && <Button variant="ghost" size="sm" className="history-hydration-retry" onClick={onRetry}>Retry now</Button>}
        </div>
      </div>
    );
  }

  if (!hasCachedTranscript) {
    return (
      <div class="history-hydration history-hydration--empty history-hydration--skeleton" role="status">
        <div class="history-hydration-skeleton" aria-hidden="true">
          <StreamingSkeleton className="history-hydration-skeleton-user" widths={["86%", "54%"]} />
          <StreamingSkeleton widths={["94%", "88%", "61%"]} />
          <StreamingSkeleton className="history-hydration-skeleton-tool" widths={["100%", "100%"]} />
          <StreamingSkeleton widths={["90%", "72%", "43%"]} />
        </div>
        <span class="history-hydration-sr">Loading conversation</span>
      </div>
    );
  }

  return (
    <div class="history-hydration" role="status">
      <div class="history-hydration-rule">
        <span class="history-hydration-label">
          <Spinner size={9} color="blue" /> Catching up
        </span>
      </div>
      <StreamingSkeleton widths={["100%"]} aria-hidden="true" />
      <StreamingSkeleton widths={["92%", "76%", "48%"]} aria-hidden="true" />
      <span class="history-hydration-sr">Showing your last view while the newest messages load.</span>
    </div>
  );
}
