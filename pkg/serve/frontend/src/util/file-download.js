// Download through a blob URL so standalone PWAs do not navigate away to the
// browser's full-screen download view. On supported mobile browsers this uses
// the native share sheet instead.
export async function downloadFile({ name, mime, url }) {
  const resp = await fetch(url);
  if (!resp.ok) throw new Error(`download failed: ${resp.status}`);

  const blob = await resp.blob();
  const file = new File([blob], name, { type: mime || blob.type });
  if (navigator.canShare && navigator.canShare({ files: [file] })) {
    try {
      await navigator.share({ files: [file] });
      return;
    } catch (error) {
      // An AbortError means the user dismissed the share sheet, which should
      // not silently turn into a download. Some installed mobile PWAs instead
      // reject an unavailable share sheet with NotAllowedError; fall through
      // to the normal blob download in that case.
      if (error?.name === 'AbortError') throw error;
    }
  }

  const blobUrl = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = blobUrl;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(blobUrl), 30000);
}
