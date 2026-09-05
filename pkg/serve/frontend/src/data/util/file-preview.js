// Preview limits live next to readCapped — the only thing that can enforce
// them against a real response — so the file viewer and the artifacts reader
// cap the same bytes instead of drifting apart.
export const MAX_PREVIEW_SIZE = 2 * 1024 * 1024;
export const MAX_HIGHLIGHT_SIZE = 150 * 1024;

export async function readCapped(resp, max) {
  const declared = Number(resp.headers.get('content-length'));
  if (declared && declared > max) throw Object.assign(new Error('too large'), { tooLarge: true });
  const type = resp.headers.get('content-type') || '';
  const reader = resp.body?.getReader?.();
  if (!reader) {
    const blob = await resp.blob();
    if (blob.size > max) throw Object.assign(new Error('too large'), { tooLarge: true });
    return blob;
  }
  const chunks = [];
  let total = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    total += value.length;
    if (total > max) {
      reader.cancel();
      throw Object.assign(new Error('too large'), { tooLarge: true });
    }
    chunks.push(value);
  }
  return new Blob(chunks, { type });
}
