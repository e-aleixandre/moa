// moa service worker.
//
// Fase 1 (PWA shell): minimal lifecycle so moa serve is installable as a PWA
// and a service worker is present for the app's scope. There is no offline
// caching — the app is only used over the tailnet against a live server.
//
// Web Push handlers ('push' / 'notificationclick') are added in a later phase.

self.addEventListener('install', () => {
  // Activate this worker immediately instead of waiting for old clients to close.
  self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  // Take control of already-open clients so the SW is active without a reload.
  event.waitUntil(self.clients.claim());
});
