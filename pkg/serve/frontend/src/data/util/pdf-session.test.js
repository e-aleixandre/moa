// pdf-session.test.js — run with `bun test`
import { expect, test } from 'bun:test';
import { openPdfSession } from './pdf-session.js';

// A fake pdf.js shaped like 6.3.289: the loading task has destroy(); the
// document proxy does NOT.
function fakePdfjs({ failOn } = {}) {
  const tasks = [];
  const pdfjs = {
    GlobalWorkerOptions: {},
    tasks,
    getDocument(options) {
      let release;
      const gate = new Promise((resolve) => { release = resolve; });
      const task = {
        options,
        destroyed: 0,
        release,
        promise: gate.then(() => {
          if (failOn === 'load') throw new Error('invalid pdf');
          return {
            numPages: 3,
            getPage: async () => {
              if (failOn === 'page') throw new Error('bad page');
              return { getViewport: () => ({ width: 100, height: 200 }) };
            },
          };
        }),
        destroy() { this.destroyed++; return Promise.resolve(); },
      };
      tasks.push(task);
      return task;
    },
  };
  return pdfjs;
}

function session(pdfjs) {
  return openPdfSession({
    fetchBytes: async () => new ArrayBuffer(4),
    loadPdfjs: async () => pdfjs,
    workerSrc: '/w.js',
    wasmUrl: '/build/x/',
  });
}

const tick = () => new Promise((resolve) => setTimeout(resolve, 0));

test('closing a loaded PDF destroys its loading task, once', async () => {
  const pdfjs = fakePdfjs();
  const s = session(pdfjs);
  await tick(); await tick();
  pdfjs.tasks[0].release();
  const doc = await s.ready;
  expect(doc.pages).toBe(3);
  expect(doc.ratio).toBe(2);
  s.destroy();
  s.destroy();
  expect(pdfjs.tasks[0].destroyed).toBe(1);
});

test('closing while the document is still loading destroys the task', async () => {
  const pdfjs = fakePdfjs();
  const s = session(pdfjs);
  await tick(); await tick();
  s.destroy();
  expect(pdfjs.tasks[0].destroyed).toBe(1);
  pdfjs.tasks[0].release();
  expect(await s.ready).toBeNull();
});

test('closing before pdf.js is even loaded creates no task', async () => {
  const pdfjs = fakePdfjs();
  const s = session(pdfjs);
  s.destroy();
  expect(await s.ready).toBeNull();
  expect(pdfjs.tasks.length).toBe(0);
});

test('an invalid PDF releases its task instead of piling up workers', async () => {
  for (const failOn of ['load', 'page']) {
    const pdfjs = fakePdfjs({ failOn });
    const s = session(pdfjs);
    await tick(); await tick();
    pdfjs.tasks[0].release();
    await expect(s.ready).rejects.toThrow();
    expect(pdfjs.tasks[0].destroyed).toBe(1);
    s.destroy();
    expect(pdfjs.tasks[0].destroyed).toBe(1);
  }
});

test('the worker and the decoder (wasm) location are passed to pdf.js', async () => {
  const pdfjs = fakePdfjs();
  session(pdfjs);
  await tick(); await tick();
  expect(pdfjs.GlobalWorkerOptions.workerSrc).toBe('/w.js');
  expect(pdfjs.tasks[0].options.wasmUrl).toBe('/build/x/');
});
