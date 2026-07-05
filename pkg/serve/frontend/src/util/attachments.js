// attachments.js — client-side file → attachment conversion for the composer.
//
// Images are downscaled/re-encoded in the browser (canvas) before upload so a
// typical 8 MB phone photo doesn't blow past the server's per-image limit or
// waste bandwidth/tokens. GIFs are passed through untouched to preserve
// animation. Non-image files are base64-encoded as-is. Server-side, images
// and PDFs are sent to the model natively; other files are saved to disk and
// the agent accesses them by path.

const MAX_DIMENSION = 1568; // long-edge cap; the API's cost/quality sweet spot
const JPEG_QUALITY = 0.85;
const SMALL_IMAGE_BYTES = 500 * 1024;
export const MAX_CLIENT_FILE_SIZE = 32 * 1024 * 1024; // reject obviously-huge files before upload

// processFile converts a File into { name, mime, data, size, isImage }, where
// data is a base64 string (no "data:" prefix). Throws on files that are
// clearly too large to bother uploading.
export async function processFile(file) {
  if (file.size > MAX_CLIENT_FILE_SIZE) {
    throw new Error(`${file.name}: file too large (max 32 MB)`);
  }

  const isImage = file.type.startsWith('image/');
  if (isImage && file.type !== 'image/gif') {
    const dims = await imageDimensions(file).catch(() => null);
    const oversized = dims && (dims.width > MAX_DIMENSION || dims.height > MAX_DIMENSION);
    if (oversized || file.size > SMALL_IMAGE_BYTES) {
      return downscaleImage(file, dims);
    }
  }

  const data = await readAsBase64(file);
  return { name: file.name, mime: file.type || 'application/octet-stream', data, size: file.size, isImage };
}

function imageDimensions(file) {
  return new Promise((resolve, reject) => {
    const url = URL.createObjectURL(file);
    const img = new Image();
    img.onload = () => {
      URL.revokeObjectURL(url);
      resolve({ width: img.naturalWidth, height: img.naturalHeight });
    };
    img.onerror = (e) => {
      URL.revokeObjectURL(url);
      reject(e);
    };
    img.src = url;
  });
}

function downscaleImage(file) {
  return new Promise((resolve, reject) => {
    const url = URL.createObjectURL(file);
    const img = new Image();
    img.onload = () => {
      URL.revokeObjectURL(url);
      const scale = Math.min(1, MAX_DIMENSION / Math.max(img.naturalWidth, img.naturalHeight));
      const w = Math.max(1, Math.round(img.naturalWidth * scale));
      const h = Math.max(1, Math.round(img.naturalHeight * scale));
      const canvas = document.createElement('canvas');
      canvas.width = w;
      canvas.height = h;
      const ctx = canvas.getContext('2d');
      ctx.drawImage(img, 0, 0, w, h);
      const dataUrl = canvas.toDataURL('image/jpeg', JPEG_QUALITY);
      const data = dataUrl.slice(dataUrl.indexOf(',') + 1);
      resolve({
        name: file.name,
        mime: 'image/jpeg',
        data,
        size: Math.round(data.length * 0.75),
        isImage: true,
      });
    };
    img.onerror = reject;
    img.src = url;
  });
}

function readAsBase64(file) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => {
      const result = reader.result;
      resolve(result.slice(result.indexOf(',') + 1));
    };
    reader.onerror = reject;
    reader.readAsDataURL(file);
  });
}
