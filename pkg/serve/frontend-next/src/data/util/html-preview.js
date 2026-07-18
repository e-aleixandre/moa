export const HTML_PREVIEW_SANDBOX = 'allow-scripts';
export const HTML_PREVIEW_CSP = "default-src 'none'; base-uri 'none'; connect-src https: wss:; script-src 'unsafe-inline' https:; style-src 'unsafe-inline' https:; font-src https:; img-src data: https:; media-src https:; object-src 'none'; frame-src 'none'; form-action 'none'; worker-src 'none'";

export function buildHTMLSrcdoc(body, styles) {
  return `<!doctype html><html><head><meta http-equiv="Content-Security-Policy" content="${HTML_PREVIEW_CSP}"><style>${styles}</style></head><body>${body}</body></html>`;
}
