import { expect, test } from "bun:test";
import { WaypointAttachments } from "./WaypointAttachments.jsx";

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

test("an image attachment with data renders a data URL thumbnail", () => {
  let opened = null;
  const attachments = WaypointAttachments({
    attachments: [{ type: "image", data: "aGVsbG8=", mime_type: "image/png", filename: "proof.png" }],
    onOpenImage: (attachment) => {
      opened = attachment;
    },
  });
  const thumbnail = byClass(attachments, "wp-attachment-thumbnail");
  const image = descendants(attachments).find((node) => node.type === "img");

  expect(thumbnail).toBeDefined();
  expect(image.props.src).toBe("data:image/png;base64,aGVsbG8=");
  expect(image.props.alt).toBe("proof.png");
  thumbnail.props.onClick();
  expect(opened).toEqual({ type: "image", data: "aGVsbG8=", mime_type: "image/png", filename: "proof.png" });
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
