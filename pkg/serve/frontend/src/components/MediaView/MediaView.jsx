import { useEffect, useRef, useState } from 'preact/hooks';
import { Loader2 } from 'lucide-preact';
import { openPdfSession } from '../../data/util/pdf-session.js';
import './MediaView.css';

// Cap on a PDF read into memory. Video and audio never are: the element
// streams them straight from the URL with Range requests, which iOS Safari
// requires to play a video at all.
const MAX_PDF_SIZE = 64 * 1024 * 1024;
const PAGE_GUTTER = 12;

// MediaView — the in-app player for a video, audio or PDF, shared by the
// artifacts reader and the file/attachment sheet. It only fills its parent;
// the header (share, close) belongs to whichever surface hosts it. Same
// origin and cookie as every other file read: no signed URLs.
export function MediaView({ kind, name, url, onFailure }) {
  const [failed, setFailed] = useState(false);
  useEffect(() => setFailed(false), [kind, url]);

  if (failed) {
    return <div class="mv-status" role="alert">Cannot play this file here. Use Share to open it elsewhere.</div>;
  }
  const fail = () => { setFailed(true); onFailure?.(); };
  if (kind === 'video') {
    return (
      <div class="mv-stage">
        <video class="mv-video" src={url} controls playsinline preload="metadata" aria-label={name} onError={fail} />
      </div>
    );
  }
  if (kind === 'audio') {
    return (
      <div class="mv-stage mv-audio-stage">
        <div class="mv-audio-name">{name}</div>
        <audio class="mv-audio" src={url} controls preload="metadata" aria-label={name} onError={fail} />
      </div>
    );
  }
  return <PdfView name={name} url={url} onFail={fail} />;
}

// A PDF is drawn by pdf.js into a scroll container of our own. The native
// options do not paginate on iOS: an <iframe> turns the PDF into one image of
// page 1, and <object> leaves the paging to a viewer we cannot drive or test.
// pdf.js is a lazy chunk, loaded only when a PDF is opened, and the bytes come
// through fetch (same origin, same cookie) because the file endpoints answer
// `Content-Disposition: attachment` and `CSP: sandbox`.
function PdfView({ name, url, onFail }) {
  const [doc, setDoc] = useState(null);

  useEffect(() => {
    setDoc(null);
    const base = new URL('./', import.meta.url).href;
    const session = openPdfSession({
      fetchBytes: async () => {
        const response = await fetch(url, { cache: 'no-store' });
        if (!response.ok) throw new Error(`pdf failed: ${response.status}`);
        if (Number(response.headers.get('content-length')) > MAX_PDF_SIZE) throw new Error('pdf too large');
        const bytes = await response.arrayBuffer();
        if (bytes.byteLength > MAX_PDF_SIZE) throw new Error('pdf too large');
        return bytes;
      },
      loadPdfjs: () => import('pdfjs-dist/build/pdf.min.mjs'),
      workerSrc: `${base}pdf-worker.js`,
      // CCITT/JBig2/JPEG2000 images (scanned PDFs) decode through wasm that
      // pdf.js fetches from this directory: same origin, lazy.
      wasmUrl: base,
    });
    let cancelled = false;
    session.ready.then((loaded) => {
      if (!cancelled && loaded) setDoc(loaded);
    }, () => {
      if (!cancelled) onFail();
    });
    return () => {
      cancelled = true;
      session.destroy();
    };
  }, [url]);

  if (!doc) {
    return <div class="mv-status" role="status"><Loader2 class="spin" size={16} /> Opening…</div>;
  }
  return <PdfPages pdf={doc.pdf} pages={doc.pages} ratio={doc.ratio} name={name} />;
}

function PdfPages({ pdf, pages, ratio, name }) {
  const scroller = useRef(null);
  const [current, setCurrent] = useState(1);
  const [width, setWidth] = useState(0);

  useEffect(() => {
    const node = scroller.current;
    if (!node) return undefined;
    const measure = () => setWidth(Math.floor(node.clientWidth - PAGE_GUTTER * 2));
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(node);
    return () => observer.disconnect();
  }, []);

  // The page in view is the one covering the middle of the scroller.
  const onScroll = () => {
    const node = scroller.current;
    if (!node) return;
    const middle = node.getBoundingClientRect().top + node.clientHeight / 2;
    for (const page of node.querySelectorAll('[data-page]')) {
      const box = page.getBoundingClientRect();
      if (box.top <= middle && box.bottom >= middle) {
        setCurrent(Number(page.dataset.page));
        return;
      }
    }
  };

  const list = [];
  for (let number = 1; number <= pages; number++) {
    list.push(<PdfPage key={number} pdf={pdf} number={number} width={width} ratio={ratio} root={scroller} />);
  }
  return (
    <div class="mv-pdf-wrap">
      <div class="mv-pdf" ref={scroller} onScroll={onScroll} role="document" aria-label={name}>{list}</div>
      {pages > 1 && <output class="mv-pdf-count" aria-live="polite">{current} / {pages}</output>}
    </div>
  );
}

// One page. It reserves its height up front (page 1's proportions, corrected
// once drawn) so the scroll length is right before anything is rendered, and
// draws only when it comes within a screen of the viewport.
function PdfPage({ pdf, number, width, ratio, root }) {
  const holder = useRef(null);
  const canvas = useRef(null);
  const [near, setNear] = useState(false);
  const [height, setHeight] = useState(0);

  useEffect(() => {
    const node = holder.current;
    if (!node) return undefined;
    const observer = new IntersectionObserver(
      ([entry]) => { if (entry.isIntersecting) setNear(true); },
      { root: root.current, rootMargin: '100% 0px' },
    );
    observer.observe(node);
    return () => observer.disconnect();
  }, []);

  useEffect(() => {
    if (!near || width <= 0) return undefined;
    let cancelled = false;
    let task = null;
    (async () => {
      const page = await pdf.getPage(number);
      const base = page.getViewport({ scale: 1 });
      const scale = width / base.width;
      const ratioNow = Math.min(window.devicePixelRatio || 1, 2);
      const viewport = page.getViewport({ scale: scale * ratioNow });
      const target = canvas.current;
      if (cancelled || !target) return;
      target.width = Math.floor(viewport.width);
      target.height = Math.floor(viewport.height);
      setHeight(Math.floor(base.height * scale));
      task = page.render({ canvasContext: target.getContext('2d'), viewport });
      await task.promise;
    })().catch(() => {});
    return () => {
      cancelled = true;
      task?.cancel();
    };
  }, [near, width, pdf, number]);

  const boxHeight = height || Math.floor(width * ratio);
  return (
    <div class="mv-pdf-page" data-page={number} ref={holder} style={{ width: `${width}px`, height: `${boxHeight}px` }}>
      <canvas ref={canvas} style={{ width: '100%', height: '100%' }} aria-label={`Page ${number}`} />
    </div>
  );
}
