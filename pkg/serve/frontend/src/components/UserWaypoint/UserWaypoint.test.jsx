import { expect, test } from "bun:test";
import { UserWaypoint } from "./UserWaypoint.jsx";
import {
  WaypointAttachments,
  AttachmentRow,
  attachmentImageSrc,
  attachmentBytes,
  attachmentType,
} from "./WaypointAttachments.jsx";

function descendants(node, nodes = []) {
  if (node == null || typeof node === "string") return nodes;
  if (Array.isArray(node)) {
    for (const child of node) descendants(child, nodes);
    return nodes;
  }
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
  if (typeof node === "number") return String(node);
  const children = node.props?.children;
  return (Array.isArray(children) ? children : [children]).map(textContent).join("");
}

// A row is an AttachmentRow component (it holds its own broken-thumbnail
// state), so a static vnode walk of the skirt does not expand it: render the
// row itself when the assertion is about what a row paints.
function rows(node) {
  return descendants(node).filter((child) => child.props?.attachment != null);
}

function imageNode(node) {
  return descendants(node).find((child) => child.type === "img");
}

function renderRow(attachment, sessionId, onOpenImage) {
  return AttachmentRow({ attachment, sessionId, onOpenImage });
}

test("an image attachment with data renders a data URL thumbnail", () => {
  let opened = null;
  const attachment = { type: "image", data: "aGVsbG8=", mime_type: "image/png", filename: "proof.png" };
  const row = renderRow(attachment, undefined, (a) => {
    opened = a;
  });
  const image = imageNode(row);

  expect(row.type).toBe("button");
  expect(image.props.src).toBe("data:image/png;base64,aGVsbG8=");
  expect(textContent(row)).toContain("proof.png");
  row.props.onClick();
  expect(opened).toEqual(attachment);
});

test("a persisted image attachment renders an endpoint thumbnail", () => {
  const row = renderRow(
    { type: "image", attachment_id: "att_1", mime_type: "image/png", filename: "proof.png" },
    "session/1"
  );

  expect(imageNode(row).props.src).toBe("/api/sessions/session%2F1/attachments/att_1");
});

test("a persisted image without a session renders an unavailable chip", () => {
  const row = renderRow({ type: "image", attachment_id: "att_1", mime_type: "image/png", filename: "proof.png" });

  expect(row.props.class).toContain("wp-attachment-chip");
  expect(imageNode(row)).toBeUndefined();
});

test("a persisted image with a non-inline MIME renders an unavailable chip", () => {
  const row = renderRow(
    { type: "image", attachment_id: "att_1", mime_type: "image/svg+xml", filename: "proof.svg" },
    "s1"
  );

  expect(row.props.class).toContain("wp-attachment-chip");
  expect(imageNode(row)).toBeUndefined();
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
  const row = renderRow({ type: "image", data: "", mime_type: "image/jpeg", filename: "Image" });

  expect(row.props.class).toContain("wp-attachment-chip");
  expect(imageNode(row)).toBeUndefined();
  expect(textContent(row)).toContain("Image");
});

test("a document attachment with no session renders an unavailable row", () => {
  const row = renderRow({ type: "document", data: "", mime_type: "application/pdf", filename: "notes.pdf" });

  expect(row.props.class).toContain("wp-attachment-chip");
  expect(textContent(row)).toContain("notes.pdf");
});

test("an optimistic document is an unavailable chip rather than a download link", () => {
  const row = renderRow({ type: "document", mime_type: "application/vnd.ms-excel", filename: "informe.xls" }, "s1");

  expect(row.type).toBe("span");
  expect(row.props.class).toContain("wp-attachment-chip");
  expect(textContent(row)).toContain("informe.xls");
});

test("a persisted document row downloads from its attachment endpoint", () => {
  const row = renderRow(
    { type: "document", attachment_id: "att_file", mime_type: "text/csv", filename: "report.csv" },
    "session/1"
  );

  expect(row.type).toBe("a");
  expect(row.props.href).toBe("/api/sessions/session%2F1/attachments/att_file");
  expect("download" in row.props).toBe(true);
});

test("a single attachment is one row with no header", () => {
  const skirt = WaypointAttachments({
    attachments: [{ type: "image", attachment_id: "a1", attachment_size: 1024, mime_type: "image/png", filename: "one.png" }],
    sessionId: "s1",
  });

  expect(skirt.props.class).toContain("skirt");
  expect(byClass(skirt, "skirt-head")).toBeUndefined();
  expect(rows(skirt)).toHaveLength(1);
});

test("several attachments get a header with the count and total weight", () => {
  const skirt = WaypointAttachments({
    attachments: [
      { type: "image", attachment_id: "a1", attachment_size: 2048, mime_type: "image/png", filename: "one.png" },
      { type: "document", attachment_id: "a2", attachment_size: 1024, mime_type: "application/pdf", filename: "two.pdf" },
      { type: "document", attachment_id: "a3", attachment_size: 1024, mime_type: "text/csv", filename: "three.csv" },
    ],
    sessionId: "s1",
  });

  expect(textContent(byClass(skirt, "skirt-head"))).toContain("3 attachments");
  expect(textContent(byClass(skirt, "skirt-head"))).toContain("4.0 KB");
  expect(rows(skirt)).toHaveLength(3);
});

test("eight attachments show three rows and fold the rest behind N more", () => {
  const attachments = Array.from({ length: 8 }, (_, i) => ({
    type: "document",
    attachment_id: `a${i}`,
    attachment_size: 1024,
    mime_type: "text/plain",
    filename: `file-${i}.txt`,
  }));
  const skirt = WaypointAttachments({ attachments, sessionId: "s1" });
  const fold = descendants(skirt).find((node) => Array.isArray(node.props?.rest));

  expect(rows(skirt)).toHaveLength(3);
  expect(fold.props.rest).toHaveLength(5);
  expect(fold.props.rest.map((a) => a.filename)).toContain("file-3.txt");
  expect(textContent(skirt)).not.toContain("file-7.txt");
});

test("attachmentBytes falls back to the base64 payload length", () => {
  expect(attachmentBytes({ attachment_size: 4096 })).toBe(4096);
  expect(attachmentBytes({ data: "aGVsbG8=" })).toBe(5);
  expect(attachmentBytes({})).toBe(0);
});

test("attachmentType prefers the filename extension over the mime subtype", () => {
  expect(attachmentType({ filename: "pricing.tsx", mime_type: "text/plain" })).toBe("TSX");
  expect(attachmentType({ mime_type: "image/svg+xml" })).toBe("SVG");
});

test("a text-only waypoint has no attachments strip", () => {
  expect(WaypointAttachments({ attachments: [] })).toBeNull();
});

test("a parent task uses the parent label and subagent accent", () => {
  const waypoint = UserWaypoint({
    tone: "parent",
    accent: "teal",
    label: "↳ FROM PARENT",
    children: <p>Review this change.</p>,
  });
  const card = descendants(waypoint).find((node) => node.props?.class === "waypoint waypoint-parent");

  expect(card).toBeDefined();
  expect(card.props.style).toEqual({ "--waypoint-accent": "var(--teal)" });
  expect(textContent(waypoint)).toContain("↳ FROM PARENT");
  expect(textContent(waypoint)).not.toContain("You");
});

test("an ordinary user waypoint remains labeled You", () => {
  const waypoint = UserWaypoint({ children: <p>Steer the child.</p> });
  const card = descendants(waypoint).find((node) => node.props?.class === "waypoint waypoint-user");

  expect(card).toBeDefined();
  expect(textContent(waypoint)).toContain("You");
});
