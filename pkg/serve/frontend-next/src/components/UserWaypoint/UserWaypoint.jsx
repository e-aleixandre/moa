import { useState } from "preact/hooks";
import { sanitizeHtml } from "../../util/sanitize.js";
import { Sheet } from "../Sheet/Sheet.jsx";
import { WaypointAttachments, attachmentDataUrl, attachmentLabel } from "./WaypointAttachments.jsx";
import "./UserWaypoint.css";

// UserWaypoint — the user's prompt as a waypoint card inside
// the stream: peach border on the left, "YOU" header + time, text
// body. `html` allows passing an already-rendered body (e.g. markdown); if not
// given, `children` is used as-is (usually a <p>). `label` overrides the "You"
// header text (e.g. "You — steer" for a mid-run course-correction); the peach
// treatment stays (peach = user), only the header word differs.
function ImageLightbox({ attachment, onClose }) {
  const src = attachmentDataUrl(attachment);
  if (!src) return null;
  const label = attachmentLabel(attachment, "Image");

  return (
    <Sheet open onClose={onClose} title={label} ariaLabel={`Preview ${label}`} class="wp-image-lightbox">
      <div class="wp-image-lightbox-body">
        <img src={src} alt={label} />
      </div>
    </Sheet>
  );
}

export function UserWaypoint({ time, children, html, label = "You", className = "", attachments, ...rest }) {
  const [openAttachment, setOpenAttachment] = useState(null);

  return (
    <>
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
        <WaypointAttachments attachments={attachments} onOpenImage={setOpenAttachment} />
      </div>
      {openAttachment && (
        <ImageLightbox attachment={openAttachment} onClose={() => setOpenAttachment(null)} />
      )}
    </>
  );
}
