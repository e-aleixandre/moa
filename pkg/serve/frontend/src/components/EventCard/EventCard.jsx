/* wake-on-event — EventCard: one pending event in the session drawer / spine. */
import "./EventCard.css";

// relAge mirrors the drawer's own age format (chrome.js relAge): the card
// lives among session rows and must not read like a different clock.
function relAge(created) {
  const ms = typeof created === "number" ? created : Date.parse(created);
  if (!Number.isFinite(ms)) return "";
  const min = Math.floor((Date.now() - ms) / 60000);
  if (min < 1) return "now";
  if (min < 60) return `${min}m`;
  const h = Math.floor(min / 60);
  if (h < 24) return `${h}h`;
  return `${Math.floor(h / 24)}d`;
}

// shortProject trims the project path the way the drawer trims session paths:
// the last two segments are what identifies a checkout at a glance.
function shortProject(path) {
  if (!path) return "";
  const parts = path.split("/").filter(Boolean);
  return parts.length <= 2 ? path : "…/" + parts.slice(-2).join("/");
}

// EventCard — an event waiting for a decision. Three actions, all terminal:
// send it to the suggested session, send it to a new one, or drop it. There is
// no "read": an event is either sent somewhere or dismissed, so the inbox
// never accumulates things that have already been dealt with.
//
// `suggestedTitle` is the title of the session the server picked (event
// `suggested`); without one there is no session in the project to send to, and
// the primary action is creating one.
export function EventCard({ event, suggestedTitle = "", onRoute, onNew, onDismiss, busy = false }) {
  const { id, source, project, title, body, suggested } = event;
  const hasSuggestion = !!suggested;
  return (
    <div class="evcard">
      <div class="evcard-src">
        <span class="evcard-source">{source}</span>
        <span class="evcard-proj">{shortProject(project)}</span>
        <span class="evcard-age">{relAge(event.created)}</span>
      </div>
      <h3 class="evcard-title">{title}</h3>
      {body && <p class="evcard-body">{body}</p>}
      <div class="evcard-acts">
        {hasSuggestion ? (
          <button
            type="button"
            class="evcard-go"
            disabled={busy}
            onClick={() => onRoute?.(id, suggested)}
          >
            <span class="evcard-go-label">Send to {suggestedTitle || "the open session"}</span>
          </button>
        ) : (
          <button type="button" class="evcard-go" disabled={busy} onClick={() => onNew?.(id)}>
            <span class="evcard-go-label">New session</span>
            <small>No session open in this project</small>
          </button>
        )}
        {hasSuggestion && (
          <button type="button" class="evcard-new" disabled={busy} onClick={() => onNew?.(id)}>
            New session
          </button>
        )}
        <button type="button" class="evcard-dismiss" disabled={busy} onClick={() => onDismiss?.(id)}>
          Dismiss
        </button>
      </div>
    </div>
  );
}
