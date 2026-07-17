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
