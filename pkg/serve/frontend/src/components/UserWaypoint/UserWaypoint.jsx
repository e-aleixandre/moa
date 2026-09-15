import { useRef, useState } from "preact/hooks";
import { Rewind as RewindIcon } from "lucide-preact";
import { sanitizeHtml } from "../../util/sanitize.js";
import { clockHHMM } from "../../data/util/clock.js";
import { Sheet } from "../Sheet/Sheet.jsx";
import { WaypointAttachments, attachmentImageSrc, attachmentLabel } from "./WaypointAttachments.jsx";
import { PreviewReference } from "./PreviewReference.jsx";
import "./UserWaypoint.css";

// UserWaypoint — the user's message. Markup and CSS are the catalogue's
// (catalog/zones-lab.jsx `UserMessage`, zones-lab.css `.zl-user`), MOVED here
// rather than imitated: the peach edge is the identity, and the one place in
// the product peach is allowed. There is no card and no "YOU" header — the
// bar is the speaker. The catalogue imports this component now, which is
// what makes one definition rather than two.
//
// What is NOT the catalogue's is everything the prototype never had, grafted
// on top: rewind, attachments, the live-preview reference, a parent-session
// accent, and the confirmation sheet. Ordinary messages show none of that.

// The lightbox keeps the attachment it is closing. Sheet animates its own
// exit, but it can only do that while it is still rendered: mounting it
// conditionally on `openAttachment` and passing a hard `open` meant the
// parent tore the whole thing out on the frame of the close, and the exit
// never ran. So `open` is the real state here, and the last attachment is
// held just long enough for the sheet to leave with something to show.
function ImageLightbox({ attachment, sessionId, onClose }) {
  const shown = useRef(attachment);
  if (attachment) shown.current = attachment;
  const src = attachmentImageSrc(shown.current, sessionId);
  if (!src) return null;
  const label = attachmentLabel(shown.current, "Image");

  return (
    <Sheet open={!!attachment} onClose={onClose} title={label} ariaLabel={`Preview ${label}`} class="wp-image-lightbox">
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
function RewindConfirm({ open, preview, onConfirm, onOpenTimeline, onClose }) {
  return (
    <Sheet open={open} onClose={onClose} title="Rewind here?" ariaLabel="Confirm rewind">
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
  label,
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
  // Ordinary messages have no label: the peach edge is the identity. Steer and
  // parent-session messages still name their source, because that is not "you".
  const showLabel = label && label !== "You";
  // `time` arrives as the server's epoch seconds, not as text. A pre-formatted
  // string (the catalogue's fixtures, "10:12") passes through untouched.
  const hhmm = typeof time === "string" && !/^\d+$/.test(time) ? time : clockHHMM(time);

  return (
    <>
      <div
        class={`zl-user${tone === "parent" ? " is-parent" : ""}${hasSkirt ? " has-skirt" : ""}${className ? ` ${className}` : ""}`}
        style={accent ? { "--waypoint-accent": `var(--${accent})` } : undefined}
        {...rest}
      >
        {showLabel && <div class="zl-user-label">{label}</div>}
        <div class="zl-user-body">
          {reference && <PreviewReference reference={reference} />}
          {html != null ? <div dangerouslySetInnerHTML={{ __html: sanitizeHtml(html) }} /> : children}
        </div>
        {(hhmm || onRewind) && (
          <span class="zl-user-when zl-data">
            {hhmm && <time>{hhmm}</time>}
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
          </span>
        )}
        <WaypointAttachments attachments={attachments} sessionId={sessionId} onOpenImage={setOpenAttachment} />
      </div>
      <ImageLightbox attachment={openAttachment} sessionId={sessionId} onClose={() => setOpenAttachment(null)} />
      {/* Rendered unconditionally, `open` carrying the state: see ImageLightbox.
          A conditional mount removes the sheet before it can leave. */}
      <RewindConfirm
          open={confirmRewind}
          preview={rewindPreview}
          onConfirm={() => {
            setConfirmRewind(false);
            onRewind();
          }}
          onOpenTimeline={onOpenTimeline}
          onClose={() => setConfirmRewind(false)}
        />
    </>
  );
}
