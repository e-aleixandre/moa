import { useEffect, useRef, useState } from 'preact/hooks';
import { pdfPages } from '../MediaView/pdf-pages.js';
import { textInRect } from '../../data/doc-notes.js';

const CROP_MAX = 1200;
const THUMB_MAX = 220;
const MIN_BOX = 16;

// The thing under a box: a PDF page (its canvas) or the image of the reader.
function sourceAt(layer, x, y) {
  layer.style.pointerEvents = 'none';
  const hit = document.elementFromPoint(x, y);
  layer.style.pointerEvents = '';
  const page = hit?.closest?.('.mv-pdf-page');
  if (page) return { page, node: page.querySelector('canvas') };
  const img = hit?.closest?.('.af-image-wrap')?.querySelector('img');
  return img ? { page: null, node: img } : null;
}

function drawn(node) {
  return node.tagName === 'IMG' ? node.complete && node.naturalWidth > 0 : node.dataset.ready === '1';
}

// paint — the part of `node` (a canvas or an image) given as fractions of it,
// at the resolution it has, never over CROP_MAX on the long side.
function paint(node, f, max) {
  const pxW = node.tagName === 'IMG' ? node.naturalWidth : node.width;
  const pxH = node.tagName === 'IMG' ? node.naturalHeight : node.height;
  const sw = f.w * pxW;
  const sh = f.h * pxH;
  const scale = Math.min(1, max / Math.max(sw, sh));
  const out = document.createElement('canvas');
  out.width = Math.max(1, Math.round(sw * scale));
  out.height = Math.max(1, Math.round(sh * scale));
  const ctx = out.getContext('2d');
  ctx.fillStyle = '#fff';
  ctx.fillRect(0, 0, out.width, out.height);
  ctx.drawImage(node, f.x * pxW, f.y * pxH, sw, sh, 0, 0, out.width, out.height);
  return out;
}

// pageText — what the page says inside the box, from pdf.js's own text items
// (the canvas has no text layer). Empty for a scanned page.
async function pageText(pageEl, box) {
  const page = pdfPages.get(pageEl);
  if (!page) return '';
  try {
    const base = page.getViewport({ scale: 1 });
    const k = pageEl.getBoundingClientRect().width / base.width;
    const pr = pageEl.getBoundingClientRect();
    const rect = {
      left: (box.left - pr.left) / k, right: (box.right - pr.left) / k,
      top: (box.top - pr.top) / k, bottom: (box.bottom - pr.top) / k,
    };
    const content = await page.getTextContent();
    return textInRect(content.items, (x, y) => base.convertToViewportPoint(x, y), rect);
  } catch (_) {
    return '';
  }
}

// markArea — a box in screen coordinates becomes the pick of an area note.
export async function markArea(layer, box) {
  const src = sourceAt(layer, (box.left + box.right) / 2, (box.top + box.bottom) / 2);
  if (!src || !src.node || !drawn(src.node)) return null;
  const sr = src.node.getBoundingClientRect();
  const left = Math.max(box.left, sr.left);
  const top = Math.max(box.top, sr.top);
  const right = Math.min(box.right, sr.right);
  const bottom = Math.min(box.bottom, sr.bottom);
  if (right - left < 8 || bottom - top < 8) return null;
  const f = { x: (left - sr.left) / sr.width, y: (top - sr.top) / sr.height, w: (right - left) / sr.width, h: (bottom - top) / sr.height };
  const crop = paint(src.node, f, CROP_MAX).toDataURL('image/jpeg', 0.85);
  const thumb = paint(src.node, f, THUMB_MAX).toDataURL('image/jpeg', 0.6);
  const number = src.page ? Number(src.page.dataset.page) : null;
  const quote = src.page ? await pageText(src.page, { left, top, right, bottom }) : '';
  return {
    kind: 'area', anchor: null, quote, crop, thumb,
    rect: { page: number, ...f },
    where: number ? `page ${number}` : 'image',
  };
}

// cropOf — the full-size crop of an area note at send time: the one kept in
// memory, else re-cut from the rectangle if that part is on screen, else the
// thumbnail (a reloaded draft whose page is not drawn).
export function cropOf(note) {
  if (note.crop) return note.crop;
  const r = note.rect;
  if (r) {
    const node = r.page
      ? document.querySelector(`.mv-pdf-page[data-page="${r.page}"] canvas`)
      : document.querySelector('.af-image-wrap img');
    if (node && drawn(node)) return paint(node, r, CROP_MAX).toDataURL('image/jpeg', 0.85);
  }
  return note.thumb || null;
}

// AreaLayer — over an image or a PDF page while the mode is on: drag a box.
// Nothing under it scrolls, pans or swipes to the next file while it draws.
export function AreaLayer({ dn }) {
  const layer = useRef(null);
  const [box, setBox] = useState(null);
  const start = useRef(null);
  // Bumped whenever the mode ends (Escape, Cancel, another file, unmount): a
  // box still being read when that happens must not open a note afterwards.
  const epoch = useRef(0);
  useEffect(() => {
    if (!dn.areaMode) setBox(null);
    return () => { epoch.current += 1; start.current = null; };
  }, [dn.areaMode]);
  if (!dn.areaMode) return null;

  const local = (event) => {
    const r = layer.current.getBoundingClientRect();
    return { x: event.clientX - r.left, y: event.clientY - r.top };
  };
  const down = (event) => {
    event.preventDefault();
    layer.current.setPointerCapture?.(event.pointerId);
    start.current = local(event);
    setBox({ ...start.current, w: 0, h: 0 });
  };
  const move = (event) => {
    if (!start.current) return;
    const p = local(event);
    const s = start.current;
    setBox({ x: Math.min(s.x, p.x), y: Math.min(s.y, p.y), w: Math.abs(p.x - s.x), h: Math.abs(p.y - s.y) });
  };
  const up = async () => {
    const s = start.current;
    start.current = null;
    if (!s || !box || box.w < MIN_BOX || box.h < MIN_BOX) { setBox(null); return; }
    const r = layer.current.getBoundingClientRect();
    const screen = { left: r.left + box.x, top: r.top + box.y, right: r.left + box.x + box.w, bottom: r.top + box.y + box.h };
    const mine = epoch.current;
    const pick = await markArea(layer.current, screen);
    if (mine !== epoch.current) return;
    setBox(null);
    if (pick) { dn.setAreaMode(false); dn.choose(pick); }
  };
  return (
    <div
      ref={layer}
      class="dn-area-layer is-drawing"
      onPointerDown={down}
      onPointerMove={move}
      onPointerUp={up}
      onPointerCancel={() => { start.current = null; setBox(null); }}
    >
      {box && <div class="dn-area-box" style={{ left: `${box.x}px`, top: `${box.y}px`, width: `${box.w}px`, height: `${box.h}px` }} />}
    </div>
  );
}
