const RESOURCE_TYPES = ['script', 'style', 'font', 'image', 'media'];

// Extract resource URLs without creating DOM elements. Some browsers may fetch
// image-like resources from DOMParser documents despite scripts being inert;
// the inspector must never make external requests merely by being opened.
export function extractExternalResources(html, baseURL = globalThis.location?.href) {
  const found = [];
  const add = (type, value) => addURL(found, type, value, baseURL);
  extractWithRegex(html, add);

  return summarize(found);
}

function extractWithRegex(html, add) {
  const tags = html.matchAll(/<(script|link|img|source|audio|video|track)\b[^>]*>/gi);
  for (const match of tags) {
    const tag = match[1].toLowerCase();
    const attrs = match[0];
    const src = attribute(attrs, tag === 'link' ? 'href' : 'src');
    const type = tag === 'script' ? 'script' : tag === 'link' ? linkTypeFromText(attrs) :
      tag === 'img' ? 'image' : tag === 'source' ? sourceTypeFromText(attrs) : 'media';
    if (src) add(type, src);
    const srcset = attribute(attrs, 'srcset');
    if (srcset) extractSrcset(srcset).forEach((value) => add(tag === 'source' ? sourceTypeFromText(attrs) : 'image', value));
    if (tag === 'video') {
      const poster = attribute(attrs, 'poster');
      if (poster) add('image', poster);
    }
    const style = attribute(attrs, 'style');
    if (style) extractCSS(style, add);
  }
  for (const tag of html.matchAll(/<[^>]+\bstyle\s*=/gi)) {
    const style = attribute(tag[0], 'style');
    if (style) extractCSS(style, add);
  }
  for (const style of html.matchAll(/<style\b[^>]*>([\s\S]*?)<\/style\s*>/gi)) extractCSS(style[1], add);
}

function attribute(tag, name) {
  const match = new RegExp(`\\s${name}\\s*=\\s*(?:"([^"]*)"|'([^']*)'|([^\\s>]+))`, 'i').exec(tag);
  return match?.[1] ?? match?.[2] ?? match?.[3] ?? null;
}

function linkTypeFromText(value) {
  const text = value.toLowerCase();
  if (text.includes('font')) return 'font';
  if (text.includes('image') || text.includes('icon')) return 'image';
  if (text.includes('script')) return 'script';
  if (text.includes('audio') || text.includes('video') || text.includes('media')) return 'media';
  return 'style';
}

function sourceTypeFromText(value) {
  return /picture/i.test(value) ? 'image' : 'media';
}

function extractSrcset(srcset) {
  return (srcset || '').split(',').map((candidate) => candidate.trim().split(/\s+/, 1)[0]).filter(Boolean);
}

function extractCSS(css, add) {
  if (!css) return;
  const imports = new Set();
  for (const match of css.matchAll(/@import\s+(?:url\(\s*)?(?:"([^"]*)"|'([^']*)'|([^\s;)]+))/gi)) {
    const value = match[1] ?? match[2] ?? match[3];
    imports.add(value);
    add('style', value);
  }
  const fonts = new Set();
  for (const block of css.matchAll(/@font-face\s*\{([\s\S]*?)\}/gi)) {
    for (const value of cssURLs(block[1])) {
      fonts.add(value);
      add('font', value);
    }
  }
  for (const value of cssURLs(css)) if (!imports.has(value) && !fonts.has(value)) add('image', value);
}

function cssURLs(css) {
  return [...css.matchAll(/url\(\s*(?:"([^"]*)"|'([^']*)'|([^\s)]+))\s*\)/gi)]
    .map((match) => match[1] ?? match[2] ?? match[3]);
}

function addURL(found, type, value, baseURL) {
  if (!RESOURCE_TYPES.includes(type) || !value) return;
  const raw = value.trim();
  if (!raw || /^(data|blob):/i.test(raw)) return;
  let parsed;
  try {
    parsed = new URL(raw, baseURL);
  } catch {
    return; // Relative URLs need a browser document base; malformed URLs never load.
  }
  if (!parsed.hostname || (parsed.protocol !== 'https:' && parsed.protocol !== 'http:')) return;
  found.push({ type, url: parsed.href, domain: parsed.hostname, secure: parsed.protocol === 'https:' });
}

function summarize(found) {
  const seen = new Set();
  const resources = [];
  const insecure = [];
  for (const item of found) {
    const key = `${item.type}\n${item.url}`;
    if (seen.has(key)) continue;
    seen.add(key);
    (item.secure ? resources : insecure).push(item);
  }
  return {
    resources,
    insecure,
    domains: [...new Set(resources.map((item) => item.domain))],
  };
}
