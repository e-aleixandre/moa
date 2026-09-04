import { useState } from "preact/hooks";
import { Rewind as RewindIcon } from "lucide-preact";
import { sanitizeHtml } from "../../util/sanitize.js";
import { Sheet } from "../Sheet/Sheet.jsx";
import { WaypointAttachments, attachmentImageSrc, attachmentLabel } from "./WaypointAttachments.jsx";
import { PreviewReference } from "./PreviewReference.jsx";
import "./UserWaypoint.css";

// UserWaypoint — the user's prompt as a waypoint card inside
// the stream: peach border on the left, "YOU" header + time, text
// body. `html` allows passing an already-rendered body (e.g. markdown); if not
// given, `children` is used as-is (usually a <p>). `label` overrides the "You"
// header text (e.g. "You — steer" for a mid-run course-correction). `tone`
// selects a semantic source treatment; ordinary user messages remain peach.
function ImageLightbox({ attachment, sessionId, onClose }) {
  const src = attachmentImageSrc(attachment, sessionId);
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

// RewindConfirm — the gate between tapping a waypoint's rewind mark and
// actually branching. Rewinding is not destructive (the server starts a NEW
// branch and keeps everything), but it DOES replace what you are looking at, so
// it gets a confirmation rather than firing on a stray tap — this mark sits on
// every user message, well inside the thumb's path while scrolling.
//
// `onOpenTimeline`, when given, offers the full RewindTimeline from here: this
// mark can only target YOUR messages, while the timeline also lists assistant
// turns and shows which points already have branches. On mobile that link is
// the only way back to it, since the status line no longer carries Rewind.
function RewindConfirm({ preview, onConfirm, onOpenTimeline, onClose }) {
  return (
    <Sheet open onClose={onClose} title="Rewind here?" ariaLabel="Confirm rewind">
      <div class="wp-rewind-confirm">
        <p class="wp-rewind-lead">The conversation goes back to this message:</p>
        <blockquote class="wp-rewind-quote">{preview}</blockquote>
        <p class="wp-rewind-note">
          Nothing is deleted — rewinding starts a new branch, and the current one stays
          reachable from the full timeline.
        </p>
        <div class="wp-rewind-acts">
          <button type="button" class="wp-rewind-go" onClick={onConfirm}>
            <RewindIcon size={13} aria-hidden="true" /> Rewind here
          </button>
          <button type="button" class="wp-rewind-cancel" onClick={onClose}>
            Cancel
          </button>
        </div>
        {onOpenTimeline && (
          <button
            type="button"
            class="wp-rewind-all"
            onClick={() => {
              onClose();
              onOpenTimeline();
            }}
          >
            See all points and branches
          </button>
        )}
      </div>
    </Sheet>
  );
}

export function UserWaypoint({
  time,
  children,
  html,
  label = "You",
  tone = "user",
  accent,
  className = "",
  attachments,
  sessionId,
  // The element pointed at in the Live Preview, parsed off this message's own
  // feedback block (data/util/preview-reference.js). Painted as a tag tied to
  // the message's spine instead of the raw block.
  reference,
  onRewind,
  onOpenTimeline,
  rewindDisabled = false,
  // Plain text of this message, for the confirmation to quote back. Passed in
  // rather than read off `children`/`html`, which may be a rendered VNode or
  // sanitized markup — the caller already holds the source string.
  rewindPreview = "",
  ...rest
}) {
  const [openAttachment, setOpenAttachment] = useState(null);
  const [confirmRewind, setConfirmRewind] = useState(false);
  // The attachments skirt is the card's own foot: it bleeds to the edges and
  // closes the bottom corners, so the card gives up its bottom padding.
  const hasSkirt = Array.isArray(attachments) && attachments.filter(Boolean).length > 0;

  return (
    <>
      <div
        class={`waypoint waypoint-${tone}${hasSkirt ? " has-skirt" : ""} ${className}`.trim()}
        style={accent ? { "--waypoint-accent": `var(--${accent})` } : undefined}
        {...rest}
      >
        <div class="who">
          <span class="who-label">{label}</span>
          {time && <time>{time}</time>}
          {onRewind && (
            <button
              type="button"
              class="wp-rewind"
              disabled={rewindDisabled}
              onClick={() => setConfirmRewind(true)}
              aria-label="Rewind the conversation to this message"
              title="Rewind here"
            >
              <RewindIcon size={12} aria-hidden="true" />
            </button>
          )}
        </div>
        <div class="body">
          {reference && <PreviewReference reference={reference} />}
          {html != null ? <div dangerouslySetInnerHTML={{ __html: sanitizeHtml(html) }} /> : children}
        </div>
        <WaypointAttachments attachments={attachments} sessionId={sessionId} onOpenImage={setOpenAttachment} />
      </div>
      {openAttachment && (
        <ImageLightbox attachment={openAttachment} sessionId={sessionId} onClose={() => setOpenAttachment(null)} />
      )}
      {confirmRewind && (
        <RewindConfirm
          preview={rewindPreview}
          onConfirm={() => {
            setConfirmRewind(false);
            onRewind();
          }}
          onOpenTimeline={onOpenTimeline}
          onClose={() => setConfirmRewind(false)}
        />
      )}
    </>
  );
}
