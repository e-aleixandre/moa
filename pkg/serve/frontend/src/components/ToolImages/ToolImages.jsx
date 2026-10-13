import { useState } from "preact/hooks";
import { Image as ImageIcon } from "lucide-preact";
import { humanSize } from "../../data/util/file-card.js";
import { ImageLightbox } from "../UserWaypoint/UserWaypoint.jsx";
import { attachmentImageSrc } from "../UserWaypoint/WaypointAttachments.jsx";
import "./ToolImages.css";

// Past this size the picture is not fetched to draw a thumbnail: a multi-MB
// file is downloaded and fully decoded by the browser only to paint ~90px, a
// real cost on a phone. It becomes a tile that fetches on tap instead.
export const THUMB_MAX_BYTES = 1.5 * 1024 * 1024;

// More than this is a gallery, not a glance at what the model looked at; the
// rest stay in the model's context and out of the ledger.
const MAX_SHOWN = 4;

function fileName(path) {
  const clean = typeof path === "string" ? path.replace(/\/+$/, "") : "";
  return clean.slice(clean.lastIndexOf("/") + 1);
}

// What to draw for a tool's images: which ones are fetchable at all, how each
// is labelled, and whether it earns a thumbnail or only a tile. Pure, so the
// size rule is checked without a DOM.
export function toolImageEntries(images, sessionId, path, failed = {}) {
  const name = fileName(path) || "Image";
  const usable = (images || []).filter((image) => !failed[image.attachment_id] && attachmentImageSrc(image, sessionId));
  const shown = usable.slice(0, MAX_SHOWN);
  return {
    extra: usable.length - shown.length,
    entries: shown.map((image, index) => ({
      image,
      label: shown.length > 1 ? `${name} (${index + 1} of ${shown.length})` : name,
      kind: image.attachment_size > THUMB_MAX_BYTES ? "tile" : "thumb",
    })),
  };
}

function Thumb({ image, sessionId, label, onOpen, onError }) {
  return (
    <button type="button" class="ti-thumb" aria-label={`Open image ${label}`} onClick={onOpen}>
      <img src={attachmentImageSrc(image, sessionId)} alt="" loading="lazy" decoding="async" onError={onError} />
    </button>
  );
}

function Tile({ image, label, onOpen }) {
  const size = humanSize(image.attachment_size);
  return (
    <button type="button" class="ti-tile" aria-label={`Open image ${label}, ${size}`} onClick={onOpen}>
      <ImageIcon aria-hidden="true" size={16} />
      <span class="ti-tile-name">{label}</span>
      <span class="ti-tile-size">{size} · tap to open</span>
    </button>
  );
}

// ToolImages — the picture a tool handed to the model (read of a PNG, an MCP
// screenshot), shown in its ledger row's detail so you can see what it is
// looking at, not just the path. Tapping it opens the same lightbox a
// message's attached image opens. It is not an artifact: nothing here is
// published, listed in the Artifacts drawer, or kept past the conversation.
//
// The bytes never rode the socket: the picture comes from the session's
// attachment endpoint, only once the row is opened. A small one is a
// thumbnail; a heavy one is a tile that downloads on tap. One that cannot be
// fetched any more (the blob was pruned) simply drops out.
export function ToolImages({ images, sessionId, path }) {
  const [failed, setFailed] = useState({});
  const [open, setOpen] = useState(null);
  const { entries, extra } = toolImageEntries(images, sessionId, path, failed);
  if (entries.length === 0) return null;

  return (
    <div class="ti-strip">
      {entries.map(({ image, label, kind }) => {
        const onOpen = () => setOpen({ ...image, filename: label });
        return kind === "tile" ? (
          <Tile key={image.attachment_id} image={image} label={label} onOpen={onOpen} />
        ) : (
          <Thumb
            key={image.attachment_id}
            image={image}
            sessionId={sessionId}
            label={label}
            onOpen={onOpen}
            onError={() => setFailed((prev) => ({ ...prev, [image.attachment_id]: true }))}
          />
        );
      })}
      {extra > 0 && <span class="ti-more">+{extra}</span>}
      <ImageLightbox attachment={open} sessionId={sessionId} onClose={() => setOpen(null)} />
    </div>
  );
}
