// App-level presentation decisions kept pure so keyboard and viewport behavior
// can be exercised without mounting the application root.

export function globalPaletteContext(state) {
  if (state.isMobile) return "mobile";
  return state.view === "grid" ? "grid" : "conversation";
}

export function shouldLockMobileDocument(state) {
  return state.isMobile;
}

export function isDesktopGridShortcut(event, state, blockingOverlay) {
  return !state.isMobile
    && !blockingOverlay
    && (event.metaKey || event.ctrlKey)
    && String(event.key).toLowerCase() === "g";
}
