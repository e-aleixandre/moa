// pdf-session.js — owns the lifetime of one pdf.js document. The thing to
// release is the LOADING TASK (`getDocument(...)`): in pdf.js 6 a
// PDFDocumentProxy has no destroy(), and a task that is never destroyed leaves
// its worker and the page resources alive. So the task is kept from the moment
// it exists, and destroy() releases it whatever state the load is in — still
// loading, loaded, failed or closed twice.
export function openPdfSession({ fetchBytes, loadPdfjs, workerSrc, wasmUrl }) {
  let task = null;
  let destroyed = false;

  const release = () => {
    const current = task;
    task = null;
    if (current) Promise.resolve(current.destroy()).catch(() => {});
  };

  const ready = (async () => {
    const bytes = await fetchBytes();
    if (destroyed) return null;
    const pdfjs = await loadPdfjs();
    if (destroyed) return null;
    pdfjs.GlobalWorkerOptions.workerSrc = workerSrc;
    task = pdfjs.getDocument({ data: bytes, wasmUrl });
    try {
      const pdf = await task.promise;
      if (destroyed) return null;
      const first = await pdf.getPage(1);
      const { width, height } = first.getViewport({ scale: 1 });
      return { pdf, pages: pdf.numPages, ratio: height / width };
    } catch (error) {
      release();
      throw error;
    }
  })();

  return {
    ready,
    destroy() {
      destroyed = true;
      release();
    },
  };
}
