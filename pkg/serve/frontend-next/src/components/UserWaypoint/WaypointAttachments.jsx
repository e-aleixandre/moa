import { useState } from "preact/hooks";
import { FileText, Image as ImageIcon } from "lucide-preact";

// Only these image types are rendered inline; anything else falls back to a
// chip. Defense in depth: the backend already validates upload MIME + magic
// bytes, this keeps a malformed/legacy mime_type from reaching an <img> src.
const INLINE_IMAGE_MIMES = new Set(["image/png", "image/jpeg", "image/gif", "image/webp"]);

export function attachmentDataUrl({ data, mime_type }) {
  if (!data || !INLINE_IMAGE_MIMES.has(mime_type)) return null;
  return `data:${mime_type};base64,${data}`;
}

export function attachmentImageSrc(attachment, sessionId) {
  if (attachment?.data) return attachmentDataUrl(attachment);
  if (!attachment?.attachment_id || !sessionId || !INLINE_IMAGE_MIMES.has(attachment.mime_type)) return null;
  return `/api/sessions/${encodeURIComponent(sessionId)}/attachments/${encodeURIComponent(attachment.attachment_id)}`;
}

export function attachmentLabel(attachment, fallback) {
  return attachment?.filename || attachment?.mime_type || fallback;
}

function unavailableImageChip(label, key) {
  return (
    <span key={key} class="wp-attachment-chip" title="Not available on this device">
      <ImageIcon aria-hidden="true" />
      {label}
    </span>
  );
}

// AttachmentThumbnail renders a single image attachment as a clickable
// thumbnail. If the image fails to load (e.g. a deleted/expired blob the
// endpoint now 404s), it swaps to the "unavailable" chip instead of leaving a
// broken-image icon. It renders EITHER the thumbnail OR the chip, never both.
function AttachmentThumbnail({ imageSrc, label, onOpen }) {
  const [failed, setFailed] = useState(false);
  if (failed) return unavailableImageChip(label);
  return (
    <button
      type="button"
      class="wp-attachment-thumbnail"
      onClick={onOpen}
      aria-label={`Open ${label}`}
      title={`Open ${label}`}
    >
      <img src={imageSrc} alt={label} onError={() => setFailed(true)} />
    </button>
  );
}

// Kept separately renderable so attachment display rules can be covered
// without needing a browser DOM in the frontend test suite.
export function WaypointAttachments({ attachments, sessionId, onOpenImage }) {
  const visibleAttachments = Array.isArray(attachments) ? attachments.filter(Boolean) : [];
  if (visibleAttachments.length === 0) return null;

  return (
    <div class="wp-attachments" aria-label="Attachments">
      {visibleAttachments.map((attachment, index) => {
        const imageSrc = attachment.type === "image" ? attachmentImageSrc(attachment, sessionId) : null;
        const label = attachmentLabel(attachment, attachment.type === "image" ? "Image" : "File");

        if (imageSrc) {
          return (
            <AttachmentThumbnail
              key={index}
              imageSrc={imageSrc}
              label={label}
              onOpen={() => onOpenImage?.(attachment)}
            />
          );
        }

        if (attachment.type === "image") {
          return unavailableImageChip(label, index);
        }

        return (
          <span key={index} class="wp-attachment-chip">
            <FileText aria-hidden="true" />
            {label}
          </span>
        );
      })}
    </div>
  );
}
