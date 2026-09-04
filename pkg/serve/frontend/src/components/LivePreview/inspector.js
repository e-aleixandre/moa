// moa inspector — drop this file into the app you preview inside moa and load
// it with <script src="/inspector.js"></script>.
//
// Protocol (postMessage, both ways):
//   moa  → app : { type: 'moa-inspect', enabled: true|false }
//   moa  → app : { type: 'moa-hello' }          // "are you there?"
//   app  → moa : { type: 'moa-ready' }          // the bridge exists
//   app  → moa : { type: 'moa-element', tag, id, classes, text, attrs,
//                  ancestors, selector, rect, url, path }
//
// Zoom is NOT here — a pinch that crosses the iframe boundary is
// split between two touch-active documents and flickers; the shell owns it with
// an overlay of its own (LivePreview's Zoom mode).
//
// Vanilla, no build step, no imports. Does nothing when not inside an iframe.
(function () {
  if (typeof window === 'undefined' || window.parent === window) return;

  var enabled = false;
  var shellOrigin = document.currentScript && document.currentScript.getAttribute('data-moa-origin');
  if (!shellOrigin) return;
  var overlay = null;
  var pinned = null;
  var hovered = null;

  function ensureOverlay() {
    if (overlay) return overlay;
    overlay = document.createElement('div');
    overlay.setAttribute('data-moa-inspector', 'overlay');
    var s = overlay.style;
    s.position = 'fixed';
    s.pointerEvents = 'none';
    s.zIndex = '2147483647';
    s.border = '2px solid #cba6f7';
    s.background = 'rgba(203, 166, 247, 0.18)';
    s.borderRadius = '2px';
    s.transition = 'all 60ms linear';
    s.display = 'none';
    document.documentElement.appendChild(overlay);
    return overlay;
  }

  function paint(el) {
    if (!el || !el.getBoundingClientRect) return;
    var r = el.getBoundingClientRect();
    var o = ensureOverlay();
    o.style.display = 'block';
    o.style.top = r.top + 'px';
    o.style.left = r.left + 'px';
    o.style.width = r.width + 'px';
    o.style.height = r.height + 'px';
  }

  function isOwn(el) {
    return !!(el && el.getAttribute && el.getAttribute('data-moa-inspector'));
  }

  function describe(el) {
    var tag = el.tagName.toLowerCase();
    var id = el.id || '';
    var out = tag + (id ? '#' + id : '');
    var cls = classList(el);
    for (var i = 0; i < cls.length && i < 3; i++) out += '.' + cls[i];
    return out;
  }

  function classList(el) {
    var raw = el.getAttribute ? el.getAttribute('class') || '' : '';
    return raw.split(/\s+/).filter(function (c) { return c.length > 0; });
  }

  function ancestorsOf(el) {
    var out = [];
    var node = el.parentElement;
    while (node && out.length < 4) {
      var tag = node.tagName.toLowerCase();
      if (tag === 'html' || tag === 'body') break;
      out.unshift(describe(node));
      node = node.parentElement;
    }
    return out;
  }

  // A readable-enough CSS path: id wins, otherwise tag + first class, plus
  // :nth-child when the parent has several similar children.
  function selectorOf(el) {
    var parts = [];
    var node = el;
    var depth = 0;
    while (node && node.nodeType === 1 && depth < 6) {
      var tag = node.tagName.toLowerCase();
      if (tag === 'html' || tag === 'body') break;
      if (node.id) {
        parts.unshift('#' + node.id);
        break;
      }
      var part = tag;
      var cls = classList(node);
      if (cls.length) part += '.' + cls[0];
      var parent = node.parentElement;
      if (parent) {
        var same = 0;
        var index = 0;
        for (var i = 0; i < parent.children.length; i++) {
          var child = parent.children[i];
          if (child.tagName === node.tagName) {
            same++;
            if (child === node) index = same;
          }
        }
        if (same > 1) part += ':nth-of-type(' + index + ')';
      }
      parts.unshift(part);
      node = parent;
      depth++;
    }
    return parts.join(' > ');
  }

  function attrsOf(el) {
    var out = {};
    var count = 0;
    var attrs = el.attributes || [];
    for (var i = 0; i < attrs.length && count < 6; i++) {
      var name = attrs[i].name;
      var keep = name.indexOf('data-') === 0
        || name === 'aria-label'
        || name === 'name'
        || name === 'href'
        || name === 'type'
        || name === 'role';
      if (!keep) continue;
      var value = attrs[i].value || '';
      out[name] = value.length > 60 ? value.slice(0, 60) + '…' : value;
      count++;
    }
    return out;
  }

  function payload(el) {
    var text = (el.textContent || '').replace(/\s+/g, ' ').trim();
    var rect = el.getBoundingClientRect();
    return {
      type: 'moa-element',
      tag: el.tagName.toLowerCase(),
      id: el.id || '',
      classes: classList(el),
      text: text.length > 80 ? text.slice(0, 80) + '…' : text,
      attrs: attrsOf(el),
      ancestors: ancestorsOf(el),
      selector: selectorOf(el),
      rect: { x: rect.left, y: rect.top, width: rect.width, height: rect.height },
      url: window.location.href,
      path: window.location.pathname + window.location.search
    };
  }

  function targetFrom(event) {
    var el = event.target;
    if (event.touches && event.touches.length) {
      el = document.elementFromPoint(event.touches[0].clientX, event.touches[0].clientY);
    } else if (event.changedTouches && event.changedTouches.length) {
      el = document.elementFromPoint(event.changedTouches[0].clientX, event.changedTouches[0].clientY);
    }
    if (!el || el.nodeType !== 1 || isOwn(el)) return null;
    return el;
  }

  function onOver(event) {
    var el = targetFrom(event);
    if (!el) return;
    hovered = el;
    paint(el);
  }

  function select(event) {
    var el = targetFrom(event) || hovered;
    if (!el) return;
    event.preventDefault();
    event.stopPropagation();
    pinned = el;
    paint(el);
    send(payload(el));
  }

  function onKeyDown(event) {
    if (event.key !== 'Escape') return;
    event.preventDefault();
    event.stopPropagation();
    send({ type: 'moa-escape' });
  }

  function onScrollOrResize() {
    if (pinned) paint(pinned);
    else if (overlay) overlay.style.display = 'none';
  }

  function enable() {
    if (enabled) return;
    enabled = true;
    document.addEventListener('mouseover', onOver, true);
    document.addEventListener('click', select, true);
    window.addEventListener('scroll', onScrollOrResize, true);
    window.addEventListener('resize', onScrollOrResize);
    document.documentElement.style.cursor = 'crosshair';
  }

  function disable() {
    if (!enabled) return;
    enabled = false;
    document.removeEventListener('mouseover', onOver, true);
    document.removeEventListener('click', select, true);
    window.removeEventListener('scroll', onScrollOrResize, true);
    window.removeEventListener('resize', onScrollOrResize);
    document.documentElement.style.cursor = '';
    pinned = null;
    hovered = null;
    if (overlay && overlay.parentNode) overlay.parentNode.removeChild(overlay);
    overlay = null;
  }

  function send(msg) {
    try {
      window.parent.postMessage(msg, shellOrigin);
    } catch (e) { /* the shell went away */ }
  }

  var relayScrollTarget = null;
  function elementAt(x, y) {
    var el = document.elementFromPoint(x, y);
    return !el || isOwn(el) ? null : el;
  }
  function focusable(el) {
    return !!(el && el.matches && el.matches('a[href], button, input, select, textarea, [tabindex]:not([tabindex="-1"]), [contenteditable="true"]'));
  }
  function tapAt(x, y) {
    var el = elementAt(x, y);
    if (!el) return;
    if (focusable(el) && el.focus) el.focus();
    el.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, view: window, clientX: x, clientY: y }));
  }
  function scrollableAt(x, y) {
    var el = elementAt(x, y);
    while (el && el !== document.documentElement) {
      var style = window.getComputedStyle(el);
      if (/(auto|scroll)/.test(style.overflowY) && el.scrollHeight > el.clientHeight) return el;
      if (/(auto|scroll)/.test(style.overflowX) && el.scrollWidth > el.clientWidth) return el;
      el = el.parentElement;
    }
    return null;
  }

  window.addEventListener('message', function (event) {
    if (event.source !== window.parent || event.origin !== shellOrigin) return;
    var data = event.data;
    if (!data || !data.type) return;
    if (data.type === 'moa-inspect') {
      if (data.enabled) enable();
      else disable();
      return;
    }
    if (data.type === 'moa-hello') {
      send({ type: 'moa-ready' });
      return;
    }
    if (data.type === 'moa-tap') {
      relayScrollTarget = null;
      tapAt(data.x, data.y);
      return;
    }
    if (data.type === 'moa-scroll') {
      if (data.reset) relayScrollTarget = null;
      if (!relayScrollTarget) relayScrollTarget = scrollableAt(data.x, data.y);
      if (relayScrollTarget) relayScrollTarget.scrollBy(data.dx || 0, data.dy || 0);
      else window.scrollBy(data.dx || 0, data.dy || 0);
      return;
    }
    if (data.type === 'moa-inspect-tap') {
      relayScrollTarget = null;
      var inspected = elementAt(data.x, data.y);
      if (!inspected) return;
      pinned = inspected;
      paint(inspected);
      send(payload(inspected));
    }
  });

  document.addEventListener('keydown', onKeyDown, true);

  send({ type: 'moa-ready' });
})();
