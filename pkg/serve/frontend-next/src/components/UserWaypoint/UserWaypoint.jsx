import { sanitizeHtml } from "../../util/sanitize.js";
import "./UserWaypoint.css";

// UserWaypoint — the user's prompt as a waypoint card inside
// the stream: peach border on the left, "YOU" header + time, text
// body. `html` allows passing an already-rendered body (e.g. markdown); if not
// given, `children` is used as-is (usually a <p>).
export function UserWaypoint({ time, children, html, className = "", ...rest }) {
  return (
    <div class={`waypoint ${className}`.trim()} {...rest}>
      <div class="who">
        <span class="who-label">You</span>
        {time && <time>{time}</time>}
      </div>
      {html != null ? (
        <div class="body" dangerouslySetInnerHTML={{ __html: sanitizeHtml(html) }} />
      ) : (
        <div class="body">{children}</div>
      )}
    </div>
  );
}
