export const HTML_PREVIEW_SANDBOX = 'allow-scripts';
export const HTML_PREVIEW_CSP = "default-src 'none'; base-uri 'none'; connect-src https: wss:; script-src 'unsafe-inline' https:; style-src 'unsafe-inline' https:; font-src https:; img-src data: https:; media-src https:; object-src 'none'; frame-src 'none'; form-action 'none'; worker-src 'none'";

// Without a viewport meta an iframe lays out at the default desktop width
// (~980px) and is then scaled to fit, so on a phone a shared document renders
// legible-on-desktop text at a third of its size. Giving it the real width lets
// a responsive document lay itself out, and lets it be pinch-zoomed on its own
// instead of zooming the whole app.
export const HTML_PREVIEW_VIEWPORT =
  '<meta name="viewport" content="width=device-width, initial-scale=1">';

export function buildHTMLSrcdoc(body, styles) {
  return `<!doctype html><html><head><meta http-equiv="Content-Security-Policy" content="${HTML_PREVIEW_CSP}">${HTML_PREVIEW_VIEWPORT}<style>${styles}</style></head><body>${body}</body></html>`;
}
