import { expect, test } from 'bun:test';
import { buildHTMLSrcdoc, HTML_PREVIEW_SANDBOX } from './html-preview.js';

test('HTML source documents permit HTTPS rendering assets and connections', () => {
  const srcdoc = buildHTMLSrcdoc('<script src="https://cdn.example/app.js"></script>', '');

  expect(srcdoc).toContain("default-src 'none'");
  expect(srcdoc).toContain('connect-src https: wss:');
  expect(srcdoc).toContain("script-src 'unsafe-inline' https:");
  expect(srcdoc).toContain("style-src 'unsafe-inline' https:");
  expect(srcdoc).toContain('font-src https:');
  expect(srcdoc).toContain('img-src data: https:');
  expect(srcdoc).toContain('media-src https:');
  expect(srcdoc).toContain("form-action 'none'");
  expect(srcdoc).toContain("worker-src 'none'");
  expect(srcdoc).toContain('<script src="https://cdn.example/app.js"></script>');
});

test('HTML previews retain the restrictive script-only sandbox permission', () => {
  expect(HTML_PREVIEW_SANDBOX).toBe('allow-scripts');
  expect(HTML_PREVIEW_SANDBOX).not.toContain('allow-same-origin');
  expect(HTML_PREVIEW_SANDBOX).not.toContain('allow-popups');
  expect(HTML_PREVIEW_SANDBOX).not.toContain('allow-top-navigation');
  expect(HTML_PREVIEW_SANDBOX).not.toContain('allow-forms');
});
