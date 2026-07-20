import { sanitizeHtml } from "../../util/sanitize.js";
import "./UserWaypoint.css";

// UserWaypoint — the user's prompt as a waypoint card inside
// the stream: peach border on the left, "YOU" header + time, text
// body. `html` allows passing an already-rendered body (e.g. markdown); if not
// given, `children` is used as-is (usually a <p>). `label` overrides the "You"
// header text (e.g. "You — steer" for a mid-run course-correction); the peach
// treatment stays (peach = user), only the header word differs.
export function UserWaypoint({ time, children, html, label = "You", className = "", ...rest }) {
  return (
    <div class={`waypoint ${className}`.trim()} {...rest}>
      <div class="who">
        <span class="who-label">{label}</span>
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
