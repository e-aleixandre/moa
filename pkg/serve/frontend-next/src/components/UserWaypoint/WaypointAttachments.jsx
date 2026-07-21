import { FileText, Image as ImageIcon } from "lucide-preact";

// Only these image types are rendered inline; anything else falls back to a
// chip. Defense in depth: the backend already validates upload MIME + magic
// bytes, this keeps a malformed/legacy mime_type from reaching an <img> src.
const INLINE_IMAGE_MIMES = new Set(["image/png", "image/jpeg", "image/gif", "image/webp"]);

export function attachmentDataUrl({ data, mime_type }) {
  if (!data) return null;
  const mime = INLINE_IMAGE_MIMES.has(mime_type) ? mime_type : "image/png";
  return `data:${mime};base64,${data}`;
}

export function attachmentLabel(attachment, fallback) {
  return attachment?.filename || attachment?.mime_type || fallback;
}

// Kept separately renderable so attachment display rules can be covered
// without needing a browser DOM in the frontend test suite.
export function WaypointAttachments({ attachments, onOpenImage }) {
  const visibleAttachments = Array.isArray(attachments) ? attachments.filter(Boolean) : [];
  if (visibleAttachments.length === 0) return null;

  return (
    <div class="wp-attachments" aria-label="Attachments">
      {visibleAttachments.map((attachment, index) => {
        const imageSrc = attachment.type === "image" ? attachmentDataUrl(attachment) : null;
        const label = attachmentLabel(attachment, attachment.type === "image" ? "Image" : "File");

        if (imageSrc) {
          return (
            <button
              key={index}
              type="button"
              class="wp-attachment-thumbnail"
              onClick={() => onOpenImage?.(attachment)}
              aria-label={`Open ${label}`}
              title={`Open ${label}`}
            >
              <img src={imageSrc} alt={label} />
            </button>
          );
        }

        if (attachment.type === "image") {
          return (
            <span key={index} class="wp-attachment-chip" title="Not available on this device">
              <ImageIcon aria-hidden="true" />
              {label}
            </span>
          );
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
