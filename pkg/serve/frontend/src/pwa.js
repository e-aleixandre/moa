// PWA service-worker registration.
//
// Fase 1: register /sw.js so the app is installable. The SW must live at the
// site root to control the whole scope ('/'). Web Push subscription is wired
// in a later phase; this only ensures the worker is registered.

export function registerServiceWorker() {
  if (!('serviceWorker' in navigator)) return;
  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js').catch((err) => {
      console.warn('[pwa] service worker registration failed', err);
    });
  });
}
