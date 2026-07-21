import { expect, test } from "bun:test";
import { WaypointAttachments, attachmentImageSrc } from "./WaypointAttachments.jsx";

function descendants(node, nodes = []) {
  if (node == null || typeof node === "string") return nodes;
  nodes.push(node);
  const children = node.props?.children;
  for (const child of Array.isArray(children) ? children : [children]) descendants(child, nodes);
  return nodes;
}

function byClass(node, className) {
  return descendants(node).find((child) => child.props?.class === className);
}

function textContent(node) {
  if (node == null) return "";
  if (typeof node === "string") return node;
  const children = node.props?.children;
  return (Array.isArray(children) ? children : [children]).map(textContent).join("");
}

// The thumbnail vnode is an AttachmentThumbnail component (holds its own
// broken-image fallback state); assert on its resolved props rather than the
// nested <img>, which a static vnode walk doesn't expand.
function thumbnailNode(node) {
  return descendants(node).find((child) => child.props?.imageSrc != null);
}

test("an image attachment with data renders a data URL thumbnail", () => {
  let opened = null;
  const attachments = WaypointAttachments({
    attachments: [{ type: "image", data: "aGVsbG8=", mime_type: "image/png", filename: "proof.png" }],
    onOpenImage: (attachment) => {
      opened = attachment;
    },
  });
  const thumbnail = thumbnailNode(attachments);

  expect(thumbnail).toBeDefined();
  expect(thumbnail.props.imageSrc).toBe("data:image/png;base64,aGVsbG8=");
  expect(thumbnail.props.label).toBe("proof.png");
  thumbnail.props.onOpen();
  expect(opened).toEqual({ type: "image", data: "aGVsbG8=", mime_type: "image/png", filename: "proof.png" });
});

test("a persisted image attachment renders an endpoint thumbnail", () => {
  const attachments = WaypointAttachments({
    attachments: [{ type: "image", attachment_id: "att_1", mime_type: "image/png", filename: "proof.png" }],
    sessionId: "session/1",
  });
  const thumbnail = thumbnailNode(attachments);

  expect(thumbnail.props.imageSrc).toBe("/api/sessions/session%2F1/attachments/att_1");
});

test("a persisted image without a session renders an unavailable chip", () => {
  const attachments = WaypointAttachments({
    attachments: [{ type: "image", attachment_id: "att_1", mime_type: "image/png", filename: "proof.png" }],
  });

  expect(byClass(attachments, "wp-attachment-chip")).toBeDefined();
  expect(descendants(attachments).find((node) => node.type === "img")).toBeUndefined();
});

test("a persisted image with a non-inline MIME renders an unavailable chip", () => {
  const attachments = WaypointAttachments({
    attachments: [{ type: "image", attachment_id: "att_1", mime_type: "image/svg+xml", filename: "proof.svg" }],
    sessionId: "s1",
  });

  expect(byClass(attachments, "wp-attachment-chip")).toBeDefined();
  expect(descendants(attachments).find((node) => node.type === "img")).toBeUndefined();
});

test("attachmentImageSrc prefers inline data and otherwise requires a session-backed raster attachment", () => {
  expect(attachmentImageSrc({ type: "image", data: "aGVsbG8=", mime_type: "image/png" }, "s1"))
    .toBe("data:image/png;base64,aGVsbG8=");
  expect(attachmentImageSrc({ type: "image", attachment_id: "att_1", mime_type: "image/jpeg" }, "s1"))
    .toBe("/api/sessions/s1/attachments/att_1");
  expect(attachmentImageSrc({ type: "image", attachment_id: "att_1", mime_type: "image/png" }))
    .toBeNull();
  expect(attachmentImageSrc({ type: "image", attachment_id: "att_1", mime_type: "image/svg+xml" }, "s1"))
    .toBeNull();
});

test("a stripped image attachment renders an image chip, not an image", () => {
  const attachments = WaypointAttachments({
    attachments: [{ type: "image", data: "", mime_type: "image/jpeg", filename: "Image" }],
  });

  expect(byClass(attachments, "wp-attachment-chip")).toBeDefined();
  expect(descendants(attachments).find((node) => node.type === "img")).toBeUndefined();
  expect(textContent(attachments)).toContain("Image");
});

test("a document attachment renders a file chip", () => {
  const attachments = WaypointAttachments({
    attachments: [{ type: "document", data: "", mime_type: "application/pdf", filename: "notes.pdf" }],
  });

  expect(byClass(attachments, "wp-attachment-chip")).toBeDefined();
  expect(textContent(attachments)).toContain("notes.pdf");
});

test("a text-only waypoint has no attachments strip", () => {
  expect(WaypointAttachments({ attachments: [] })).toBeNull();
});
