import { expect, test } from 'bun:test';
import { extractExternalResources } from './html-resources.js';

test('extracts and classifies absolute HTTPS resource URLs from inert HTML', () => {
  const report = extractExternalResources(`
    <script src="https://cdn.example/app.js"></script>
    <link rel="stylesheet" href="https://cdn.example/site.css">
    <link rel="preload" as="font" href="https://fonts.example/face.woff2">
    <img src="https://images.example/hero.png" srcset="https://images.example/hero-2x.png 2x, /relative.png 1x">
    <picture><source srcset="https://images.example/hero.webp 1x"><img src="data:image/png;base64,a"></picture>
    <video src="https://media.example/movie.mp4" poster="https://images.example/poster.jpg"><source src="https://media.example/movie.webm"></video>
    <style>@import url("https://cdn.example/theme.css"); @font-face { src: url(https://fonts.example/local.woff2) } .hero { background-image: url(https://images.example/bg.png) }</style>
    <div style="background: url('https://images.example/inline.png')"></div>
  `);

  expect(report.domains).toEqual(['cdn.example', 'fonts.example', 'images.example', 'media.example']);
  expect(report.resources).toEqual(expect.arrayContaining([
    { type: 'script', domain: 'cdn.example', secure: true, url: 'https://cdn.example/app.js' },
    { type: 'style', domain: 'cdn.example', secure: true, url: 'https://cdn.example/site.css' },
    { type: 'font', domain: 'fonts.example', secure: true, url: 'https://fonts.example/face.woff2' },
    { type: 'font', domain: 'fonts.example', secure: true, url: 'https://fonts.example/local.woff2' },
    { type: 'image', domain: 'images.example', secure: true, url: 'https://images.example/hero.png' },
    { type: 'media', domain: 'media.example', secure: true, url: 'https://media.example/movie.mp4' },
  ]));
  expect(report.resources.some((item) => item.url.includes('/relative.png'))).toBe(false);
  expect(report.resources.some((item) => item.url.startsWith('data:'))).toBe(false);
});

test('resolves relative and protocol-relative URLs from the preview document base', () => {
  const report = extractExternalResources(`
    <script src="http://legacy.example/app.js"></script>
    <img src="//protocol-relative.example/image.png">
    <img src="relative.png"><img src="blob:abc"><img src="data:image/png;base64,abc">
  `, 'https://moa.example/chat');

  expect(report.resources).toEqual(expect.arrayContaining([
    { type: 'image', domain: 'protocol-relative.example', secure: true, url: 'https://protocol-relative.example/image.png' },
    { type: 'image', domain: 'moa.example', secure: true, url: 'https://moa.example/relative.png' },
  ]));
  expect(report.insecure).toEqual([
    { type: 'script', domain: 'legacy.example', secure: false, url: 'http://legacy.example/app.js' },
  ]);
});
