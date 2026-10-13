// tool-images.js — the images a tool handed to the model (read of a PNG, an MCP
// screenshot), as the references the server persists: history content blocks
// and the live tool_end share one shape, `{type:'image', attachment_id,
// mime_type, attachment_size}`. Never bytes: the transcript view fetches the
// picture from the session's attachment endpoint, and only for what the user
// can see. An inline image (no attachment_id) has nothing to fetch, so it is
// left out and its row stays path-only.

const IMAGE_MIMES = new Set(['image/png', 'image/jpeg', 'image/gif', 'image/webp']);

export function toolImageRefs(content) {
  if (!Array.isArray(content)) return undefined;
  const refs = [];
  for (const c of content) {
    if (c && c.type === 'image' && c.attachment_id && IMAGE_MIMES.has(c.mime_type)) {
      refs.push({ attachment_id: c.attachment_id, mime_type: c.mime_type, attachment_size: c.attachment_size || 0 });
    }
  }
  return refs.length > 0 ? refs : undefined;
}
