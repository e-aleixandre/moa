import { useState } from "preact/hooks";
import { ChevronRight, ChevronDown, Code, File as FileIcon, FileText, Image as ImageIcon, Paperclip } from "lucide-preact";
import { humanSize } from "../../data/util/file-card.js";

// Only these image types are rendered inline; anything else falls back to a
// plain row with a file icon. Defense in depth: the backend already validates
// upload MIME + magic bytes, this keeps a malformed/legacy mime_type from
// reaching an <img> src.
const INLINE_IMAGE_MIMES = new Set(["image/png", "image/jpeg", "image/gif", "image/webp"]);

// Rows shown before the skirt folds the rest behind "N more". Four attachments
// still fit whole (folding one row away would be pointless); from five on the
// message keeps a fixed height whatever you sent.
const VISIBLE_ROWS = 3;
const FOLD_ABOVE = 4;

export function attachmentDataUrl({ data, mime_type }) {
  if (!data || !INLINE_IMAGE_MIMES.has(mime_type)) return null;
  return `data:${mime_type};base64,${data}`;
}

export function attachmentImageSrc(attachment, sessionId) {
  if (attachment?.data) return attachmentDataUrl(attachment);
  if (!attachment?.attachment_id || !sessionId || !INLINE_IMAGE_MIMES.has(attachment.mime_type)) return null;
  return `/api/sessions/${encodeURIComponent(sessionId)}/attachments/${encodeURIComponent(attachment.attachment_id)}`;
}

function attachmentDownloadURL(attachment, sessionId) {
  if (!attachment?.attachment_id || !sessionId) return null;
  return `/api/sessions/${encodeURIComponent(sessionId)}/attachments/${encodeURIComponent(attachment.attachment_id)}`;
}

export function attachmentLabel(attachment, fallback) {
  return attachment?.filename || attachment?.mime_type || fallback;
}

// attachmentBytes — the decoded size. Persisted attachments carry it; an
// optimistic one only has its base64 payload, which is 4 bytes per 3.
export function attachmentBytes(attachment) {
  if (typeof attachment?.attachment_size === "number" && attachment.attachment_size > 0) {
    return attachment.attachment_size;
  }
  if (typeof attachment?.data === "string" && attachment.data.length > 0) {
    const padding = (attachment.data.match(/=+$/) || [""])[0].length;
    return Math.max(0, Math.floor((attachment.data.length * 3) / 4) - padding);
  }
  return 0;
}

// attachmentType — the short uppercase mark ("PNG", "TSX") the desktop row
// shows before the size. The filename extension is what a person recognizes;
// the mime subtype is the fallback for a blob with no name.
export function attachmentType(attachment) {
  const name = attachment?.filename || "";
  const extension = name.includes(".") ? name.slice(name.lastIndexOf(".") + 1) : "";
  if (extension && extension.length <= 5) return extension.toUpperCase();
  const subtype = (attachment?.mime_type || "").split("/")[1] || "";
  return subtype.split("+")[0].slice(0, 5).toUpperCase();
}

const CODE_EXTENSIONS = new Set([
  "js", "jsx", "ts", "tsx", "go", "py", "rb", "rs", "java", "c", "h", "cpp", "cs",
  "sh", "css", "html", "json", "yaml", "yml", "toml", "sql", "php", "swift", "kt",
]);

function rowIcon(attachment) {
  const name = (attachment?.filename || "").toLowerCase();
  const extension = name.includes(".") ? name.slice(name.lastIndexOf(".") + 1) : "";
  const mime = attachment?.mime_type || "";
  if (CODE_EXTENSIONS.has(extension)) return <Code aria-hidden="true" />;
  if (attachment?.type === "image" || mime.startsWith("image/")) return <ImageIcon aria-hidden="true" />;
  if (mime.startsWith("text/") || mime === "application/pdf" || mime === "application/json") {
    return <FileText aria-hidden="true" />;
  }
  return <FileIcon aria-hidden="true" />;
}

// A plain function, not a component: the row's markup stays part of the same
// vnode tree, so the display rules are assertable without a DOM.
function rowBody({ attachment, label, imageSrc, onThumbnailError }) {
  const bytes = attachmentBytes(attachment);
  return (
    <>
      {imageSrc ? (
        <span class="skirt-thumb">
          <img src={imageSrc} alt="" onError={onThumbnailError} />
        </span>
      ) : (
        <span class="skirt-ico">{rowIcon(attachment)}</span>
      )}
      <span class="skirt-name">{label}</span>
      {/* an optimistic attachment has no size yet: the type alone is honest,
          "0 B" would not be. */}
      <span class="skirt-meta">
        <i class="skirt-type">{attachmentType(attachment)}{bytes > 0 ? " · " : ""}</i>
        {bytes > 0 ? humanSize(bytes) : ""}
      </span>
    </>
  );
}

// AttachmentRow renders one attachment as a 44px row. An image opens the
// lightbox, a stored document downloads from the attachment endpoint, and
// anything this device cannot reach stays a non-interactive chip row.
//
// Pure on purpose (the broken-thumbnail state lives in LiveAttachmentRow
// below): display rules stay coverable without a browser DOM.
export function AttachmentRow({ attachment, sessionId, onOpenImage, thumbnailFailed = false, onThumbnailError }) {
  const isImage = attachment.type === "image";
  const label = attachmentLabel(attachment, isImage ? "Image" : "File");
  const imageSrc = thumbnailFailed ? null : isImage ? attachmentImageSrc(attachment, sessionId) : null;
  const body = rowBody({ attachment, label, imageSrc, onThumbnailError });

  if (imageSrc) {
    return (
      <button
        type="button"
        class="skirt-row"
        onClick={() => onOpenImage?.(attachment)}
        aria-label={`Open ${label}`}
        title={`Open ${label}`}
      >
        {body}
        <ChevronRight class="skirt-chev" aria-hidden="true" />
      </button>
    );
  }

  const downloadURL = !isImage ? attachmentDownloadURL(attachment, sessionId) : null;
  if (downloadURL) {
    return (
      <a class="skirt-row" href={downloadURL} download title={`Download ${label}`}>
        {body}
        <ChevronRight class="skirt-chev" aria-hidden="true" />
      </a>
    );
  }

  return (
    <span class="skirt-row wp-attachment-chip" title="Not available on this device">
      {body}
    </span>
  );
}

// LiveAttachmentRow adds the only state a row needs: if the thumbnail fails to
// load (a blob the endpoint now 404s) the row degrades to the "not available"
// chip row rather than leaving a broken image behind.
function LiveAttachmentRow(props) {
  const [thumbnailFailed, setThumbnailFailed] = useState(false);
  return (
    <AttachmentRow
      {...props}
      thumbnailFailed={thumbnailFailed}
      onThumbnailError={() => setThumbnailFailed(true)}
    />
  );
}

// SkirtFold — the "N more" row and what it opens. It owns the only state of
// the skirt, which keeps WaypointAttachments itself renderable as a function.
export function SkirtFold({ rest, sessionId, onOpenImage }) {
  const [expanded, setExpanded] = useState(false);
  return (
    <>
      <button
        type="button"
        class={`skirt-row skirt-more${expanded ? " is-on" : ""}`}
        onClick={() => setExpanded((v) => !v)}
        aria-expanded={expanded}
      >
        <span class="skirt-ico skirt-ico-more">
          <ChevronDown aria-hidden="true" />
        </span>
        <span class="skirt-name skirt-more-label">{rest.length} more</span>
        <span class="skirt-meta skirt-more-names">
          {rest.map((a) => attachmentLabel(a, "file")).join(", ")}
        </span>
      </button>
      {expanded && (
        <div class="skirt-rest">
          {rest.map((attachment, index) => (
            <LiveAttachmentRow
              key={index}
              attachment={attachment}
              sessionId={sessionId}
              onOpenImage={onOpenImage}
            />
          ))}
        </div>
      )}
    </>
  );
}

// WaypointAttachments — "faldón": the attachments are the foot of the message
// itself, not a box inside a box. It bleeds to the card's edges and closes its
// corners; a mono header carries the count and the total weight, and each file
// is a 44px row. One attachment needs no header — the row already says it all.
// Past four, three rows show and the rest fold behind "N more", so a message
// keeps its height whatever you sent.
//
// Kept separately renderable so attachment display rules can be covered
// without needing a browser DOM in the frontend test suite.
export function WaypointAttachments({ attachments, sessionId, onOpenImage }) {
  const visibleAttachments = Array.isArray(attachments) ? attachments.filter(Boolean) : [];
  if (visibleAttachments.length === 0) return null;

  const total = visibleAttachments.reduce((sum, attachment) => sum + attachmentBytes(attachment), 0);
  const folds = visibleAttachments.length > FOLD_ABOVE;
  const shown = folds ? visibleAttachments.slice(0, VISIBLE_ROWS) : visibleAttachments;
  const rest = folds ? visibleAttachments.slice(VISIBLE_ROWS) : [];

  return (
    <div class="skirt wp-attachments" aria-label="Attachments">
      {visibleAttachments.length > 1 && (
        <div class="skirt-head">
          <Paperclip aria-hidden="true" />
          <span>{visibleAttachments.length} attachments</span>
          {total > 0 && <span class="skirt-head-size">{humanSize(total)}</span>}
        </div>
      )}
      {shown.map((attachment, index) => (
        <LiveAttachmentRow
          key={index}
          attachment={attachment}
          sessionId={sessionId}
          onOpenImage={onOpenImage}
        />
      ))}
      {folds && <SkirtFold rest={rest} sessionId={sessionId} onOpenImage={onOpenImage} />}
    </div>
  );
}
