// moa inspector — drop this file into the app you preview inside moa and load
// it with <script src="/inspector.js"></script>.
//
// Protocol (postMessage, both ways):
//   moa  → app : { type: 'moa-inspect', enabled: true|false }
//   moa  → app : { type: 'moa-hello', navigationEpoch }   // "are you there?"
//   app  → moa : { type: 'moa-ready', chain: true }  // the bridge exists
//   app  → moa : { type: 'moa-preview-navigation', navigationEpoch,
//                  supported, canGoBack }
//   moa  → app : { type: 'moa-preview-back', navigationEpoch }
//   moa  → app : { type: 'moa-scroll', x, y, dx, dy, reset, id? }
//   app  → moa : { type: 'moa-scrolled', id, dx, dy }   // only when id was given
//   moa  → app : { type: 'moa-view', epoch, zoom, zoomed }
//   app  → moa : { type: 'moa-wheel-zoom', epoch, deltaY|factor, x, y }
//   app  → moa : { type: 'moa-wheel-pan', epoch, zoom, rdx, rdy, dx, dy }
//   app  → moa : { type: 'moa-space', epoch, down }
//
// The three last packets are the DESKTOP half: a mouse or trackpad over the app
// is inside this document, and its events do not cross the iframe boundary, so
// the shell can only learn about them here. They are only ever sent after the
// shell has introduced itself with `moa-view` — an app loaded outside moa, or
// under an older shell, keeps every wheel and every key it always had.
//
// The scroll relay answers with how much the chosen scroller ACTUALLY moved so
// the shell can give the rest to its own zoom pan. `chain: true` is what says
// the answer will come: an older copy of this file never sends it, and the
// shell then keeps the plain fire-and-forget relay. Untagged packets (no `id`)
// behave exactly as they always have and are answered with nothing.
//   app  → moa : { type: 'moa-element', tag, id, classes, text, attrs,
//                  ancestors, selector, rect, url, path }
//
// Back is the app's OWN Navigation API and nothing else. `window.navigation`
// belongs to this document's browsing context, so `canGoBack` is a fact about
// this frame; `history.back()`/`history.length` are joint with the shell's own
// session history and would let a preview button consume a moa entry, so they
// are never used here. Where the API is missing the bridge says so and the
// shell keeps its button disabled — it does not simulate a reload.
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

  if (typeof navigator !== 'undefined' && navigator.serviceWorker) {
    navigator.serviceWorker.getRegistrations().then(function (registrations) {
      registrations.forEach(function (registration) { registration.unregister(); });
    }).catch(function () {});
  }

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
  var relayScrollState = newScrollState();
  // Whether the target above was already chosen for this gesture. The target
  // itself cannot say so: the page root is a legitimate answer and it is null.
  var relayScrollPicked = false;
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
  function scrollPosition(el) {
    if (el) return { x: el.scrollLeft || 0, y: el.scrollTop || 0 };
    return { x: window.scrollX || window.pageXOffset || 0, y: window.scrollY || window.pageYOffset || 0 };
  }
  function scrollBounds(el, axis) {
    if (el) {
      var extent = axis === 'x' ? el.scrollWidth - el.clientWidth : el.scrollHeight - el.clientHeight;
      if (axis === 'x' && window.getComputedStyle(el).direction === 'rtl') return { min: -Math.max(0, extent), max: 0 };
      return { min: 0, max: Math.max(0, extent) };
    }
    var root = document.documentElement;
    var body = document.body;
    var size = axis === 'x' ? 'scrollWidth' : 'scrollHeight';
    var viewport = axis === 'x' ? (window.innerWidth || root.clientWidth) : (window.innerHeight || root.clientHeight);
    if (!root[size] && !(body && body[size])) return { min: -Infinity, max: Infinity };
    return { min: 0, max: Math.max(0, Math.max(root[size] || 0, body && body[size] || 0) - (viewport || 0)) };
  }
  function newScrollState() {
    return { remainder: { x: 0, y: 0 }, direction: { x: 0, y: 0 }, actual: { x: null, y: null } };
  }
  function scrollAxis(before, requested, carry, bounds) {
    var start = Math.min(bounds.max, Math.max(bounds.min, before + carry));
    var ideal = Math.min(bounds.max, Math.max(bounds.min, start + requested));
    return { ideal: ideal, move: ideal - before, consumed: ideal - start };
  }
  function scrollWithCarry(el, dx, dy, state) {
    var before = scrollPosition(el);
    if (state.actual.x !== null && before.x !== state.actual.x) state.remainder.x = 0;
    if (state.actual.y !== null && before.y !== state.actual.y) state.remainder.y = 0;
    if (dx && state.direction.x && Math.sign(dx) !== state.direction.x) state.remainder.x = 0;
    if (dy && state.direction.y && Math.sign(dy) !== state.direction.y) state.remainder.y = 0;
    if (dx) state.direction.x = Math.sign(dx);
    if (dy) state.direction.y = Math.sign(dy);
    var planX = scrollAxis(before.x, dx, state.remainder.x, scrollBounds(el, 'x'));
    var planY = scrollAxis(before.y, dy, state.remainder.y, scrollBounds(el, 'y'));
    var scroll = { left: planX.move, top: planY.move, behavior: 'instant' };
    if (el) el.scrollBy(scroll);
    else window.scrollBy(scroll);
    var after = scrollPosition(el);
    var tookX = after.x - before.x;
    var tookY = after.y - before.y;
    var roundingX = planX.ideal - after.x;
    var roundingY = planY.ideal - after.y;
    var xRounded = Math.abs(roundingX) < 1;
    var yRounded = Math.abs(roundingY) < 1;
    // Carry only subpixel position error. At a real bound `consumed` is only
    // what still fit; a larger discrepancy is scroll snap's actual movement.
    state.remainder.x = xRounded ? roundingX : 0;
    state.remainder.y = yRounded ? roundingY : 0;
    state.actual = after;
    return { dx: xRounded ? planX.consumed : tookX, dy: yRounded ? planY.consumed : tookY };
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
  function scrollableAncestors(x, y) {
    var out = [];
    var el = elementAt(x, y);
    while (el && el !== document.documentElement) {
      var style = window.getComputedStyle(el);
      if ((/(auto|scroll)/.test(style.overflowY) && el.scrollHeight > el.clientHeight)
        || (/(auto|scroll)/.test(style.overflowX) && el.scrollWidth > el.clientWidth)) out.push(el);
      el = el.parentElement;
    }
    return out;
  }

  // ── Desktop: wheel and Space ───────────────────────────────────────────────
  // The shell tells this document what its view is; nothing below is sent
  // before that, and nothing below exists when the file is loaded outside moa.
  var viewEpoch = null;
  var viewZoom = 1;
  var spaceDown = false;
  var nativeGesture = false;
  var nativeGestureScale = 1;
  var lastPointer = null;
  var wheelScrollStates = new WeakMap();
  var wheelRootState = newScrollState();

  function wheelScrollState(target) {
    if (!target) return wheelRootState;
    var state = wheelScrollStates.get(target);
    if (!state) { state = newScrollState(); wheelScrollStates.set(target, state); }
    return state;
  }
  function wheelResidual(requested, consumed) {
    var rest = requested - consumed;
    if (!rest || Math.sign(rest) !== Math.sign(requested)) return 0;
    return Math.abs(rest) > Math.abs(requested) ? requested : rest;
  }
  function resetWheelScrollStates() {
    wheelScrollStates = new WeakMap();
    wheelRootState = newScrollState();
  }

  function editing() {
    var el = document.activeElement;
    if (!el || el.nodeType !== 1) return false;
    var tag = (el.tagName || '').toLowerCase();
    if (tag === 'input' || tag === 'textarea' || tag === 'select') return true;
    // isContentEditable covers a contenteditable host and anything inside it,
    // which is what the editors built on one (CodeMirror 6, Monaco, ProseMirror)
    // actually put the caret in. Editors built on a hidden textarea are the
    // `textarea` case above.
    return el.isContentEditable === true;
  }

  // Wheel deltas arrive in three units. The sizes are only known here, in the
  // document the wheel happened in, so this is where they become pixels.
  function wheelPixels(event, target) {
    var mode = event.deltaMode || 0;
    var scale = 1;
    if (mode === 1) scale = 16;
    else if (mode === 2) {
      var box = target || document.documentElement;
      scale = (box.clientHeight || window.innerHeight || 800);
    }
    return { dx: (event.deltaX || 0) * scale, dy: (event.deltaY || 0) * scale };
  }

  // consumeWheel — the app gets first refusal, from the innermost scrollable
  // ancestor through the page. Each step is asked for what is LEFT and answers
  // with what it actually took, per axis.
  // `behavior:'instant'` because the answer has to be read now, not after a
  // smooth animation — and because the shell needs the residual this frame.
  function consumeWheel(x, y, dx, dy) {
    var restX = dx;
    var restY = dy;
    var targets = scrollableAncestors(x, y);
    for (var i = 0; i < targets.length && (restX || restY); i++) {
      var target = targets[i];
      var took = scrollWithCarry(target, restX, restY, wheelScrollState(target));
      restX = wheelResidual(restX, took.dx);
      restY = wheelResidual(restY, took.dy);
    }
    if (restX || restY) {
      var pageTook = scrollWithCarry(null, restX, restY, wheelRootState);
      restX = wheelResidual(restX, pageTook.dx);
      restY = wheelResidual(restY, pageTook.dy);
    }
    return { dx: dx - restX, dy: dy - restY };
  }

  function onWheel(event) {
    if (viewEpoch === null) return;
    // The app's own wheel handlers run first (this listener is not capturing).
    // If one of them has already dealt with this wheel, it is not ours.
    if (event.defaultPrevented) return;
    if (Number.isFinite(event.clientX) && Number.isFinite(event.clientY)) lastPointer = { x: event.clientX, y: event.clientY };
    // Chromium and Firefox deliver a trackpad pinch as a wheel with ctrlKey,
    // which is also what a real Ctrl+wheel is: one path, no engine sniffing.
    // Safari's own gesture* events are NOT handled here — see DESKTOP-PLAN.md;
    // Ctrl+wheel with a mouse is a plain wheel event and works there too.
    if (event.ctrlKey) {
      event.preventDefault();
      if (nativeGesture) return;
      var pinch = wheelPixels(event, null);
      send({ type: 'moa-wheel-zoom', epoch: viewEpoch, deltaY: pinch.dy, x: event.clientX, y: event.clientY });
      return;
    }
    // Unzoomed there is no pan to give anything to: the app keeps its own
    // native (and smooth, and inertial) scrolling, untouched.
    if (!(viewZoom > 1)) return;
    var d = wheelPixels(event, elementAt(event.clientX, event.clientY));
    if (event.shiftKey && !d.dx) { d.dx = d.dy; d.dy = 0; }
    if (!d.dx && !d.dy) return;
    // From here this wheel is ours end to end: the default scroll is cancelled
    // BEFORE anything moves, so the app cannot be scrolled twice.
    event.preventDefault();
    var took = consumeWheel(event.clientX, event.clientY, d.dx, d.dy);
    if (took.dx === d.dx && took.dy === d.dy) return;
    send({ type: 'moa-wheel-pan', epoch: viewEpoch, zoom: viewZoom, rdx: d.dx, rdy: d.dy, dx: took.dx, dy: took.dy });
  }

  function gesturePoint(event) {
    if (Number.isFinite(event.clientX) && Number.isFinite(event.clientY)) {
      lastPointer = { x: event.clientX, y: event.clientY };
      return lastPointer;
    }
    return lastPointer || { x: (window.innerWidth || document.documentElement.clientWidth || 0) / 2, y: (window.innerHeight || document.documentElement.clientHeight || 0) / 2 };
  }
  function onGestureStart(event) {
    if (viewEpoch === null || !Number.isFinite(event.scale) || !(event.scale > 0)) return;
    nativeGesture = true;
    nativeGestureScale = event.scale;
    gesturePoint(event);
    event.preventDefault();
  }
  function onGestureChange(event) {
    if (!nativeGesture || !Number.isFinite(event.scale) || !(event.scale > 0) || !(nativeGestureScale > 0)) return;
    var factor = event.scale / nativeGestureScale;
    nativeGestureScale = event.scale;
    if (!Number.isFinite(factor) || !(factor > 0)) return;
    event.preventDefault();
    var point = gesturePoint(event);
    send({ type: 'moa-wheel-zoom', epoch: viewEpoch, factor: factor, x: point.x, y: point.y });
  }
  function endGesture(event) {
    if (!nativeGesture) return;
    nativeGesture = false;
    nativeGestureScale = 1;
    event.preventDefault();
  }

  function setSpace(down) {
    if (spaceDown === down) return;
    spaceDown = down;
    send({ type: 'moa-space', epoch: viewEpoch, down: down });
  }

  function onSpaceDown(event) {
    if (viewEpoch === null || event.key !== ' ' || event.repeat) return;
    // Space only means "pan" while there is something to pan. Unzoomed it is a
    // page-down, or a button being activated, and it stays that.
    if (!(viewZoom > 1) || editing()) return;
    event.preventDefault();
    setSpace(true);
  }

  function onSpaceUp(event) {
    if (event.key !== ' ') return;
    setSpace(false);
  }

  // A Space held while the window loses focus never gets its keyup: the shell
  // would keep a pan layer over an app nobody is panning.
  function releaseSpace() {
    setSpace(false);
  }

  // ── Back, through this document's own Navigation API ──────────────────
  //
  // Feature-gated on every member actually used, not on a browser name. The
  // epoch is learned ONLY from an authenticated hello: a report minted by the
  // document being replaced carries the old one and the shell drops it.
  var previewNavigation = window.navigation;
  var navigationSupported = !!previewNavigation
    && typeof previewNavigation.back === 'function'
    && typeof previewNavigation.addEventListener === 'function'
    && 'canGoBack' in previewNavigation;
  var navigationEpoch = null;

  function reportNavigation(backError) {
    if (navigationEpoch === null) return;
    var report = {
      type: 'moa-preview-navigation',
      navigationEpoch: navigationEpoch,
      supported: navigationSupported,
      canGoBack: navigationSupported ? previewNavigation.canGoBack === true : false,
    };
    // `canGoBack` can remain true when WebKit rejects a sandboxed native
    // traversal. Tell the shell about that distinct, terminal capability
    // failure rather than making it offer the same action forever.
    if (backError === 'SecurityError') report.backError = backError;
    send(report);
  }

  // A same-document traversal or an SPA route change: the entry list moved
  // without a new document, so nothing else would tell the shell.
  if (navigationSupported) {
    previewNavigation.addEventListener('currententrychange', reportNavigation);
  }

  function goBack(epoch) {
    // Not the current handshake, no API, or nothing to go back to: say what is
    // true now so the shell's button never stays stuck waiting.
    if (navigationEpoch === null || epoch !== navigationEpoch) return;
    if (!navigationSupported || previewNavigation.canGoBack !== true) {
      reportNavigation();
      return;
    }
    var result;
    try {
      result = previewNavigation.back();
    } catch (e) {
      reportNavigation(e && e.name);
      return;
    }
    // A traversal can be cancelled or fail; either way the shell has to leave
    // its busy state. A successful one answers through currententrychange (same
    // document) or through the next document's hello. Never a legacy fallback.
    watch(result && result.committed);
    watch(result && result.finished);
  }

  function watch(promise) {
    if (!promise || typeof promise.then !== 'function') return;
    promise.then(null, function (e) { reportNavigation(e && e.name); });
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
      send({ type: 'moa-ready', chain: true });
      // The hello is what mints the epoch this document answers under, and the
      // report that follows it is the only thing that can enable Back.
      if (Number.isFinite(data.navigationEpoch)) {
        navigationEpoch = data.navigationEpoch;
        reportNavigation();
      }
      return;
    }
    if (data.type === 'moa-preview-back') {
      goBack(data.navigationEpoch);
      return;
    }
    if (data.type === 'moa-tap') {
      relayScrollTarget = null;
      relayScrollPicked = false;
      tapAt(data.x, data.y);
      return;
    }
    if (data.type === 'moa-scroll') {
      if (data.reset) { relayScrollTarget = null; relayScrollPicked = false; relayScrollState = newScrollState(); }
      // A chained packet keeps ONE target for the whole gesture, root included;
      // an untagged one keeps the older rule, where a null target is re-asked
      // for on every packet.
      if (data.id !== undefined && data.id !== null ? !relayScrollPicked : !relayScrollTarget) {
        relayScrollTarget = scrollableAt(data.x, data.y);
        relayScrollPicked = true;
      }
      var requestedX = data.dx || 0;
      var requestedY = data.dy || 0;
      var answering = data.id !== undefined && data.id !== null;
      if (answering) {
        var took = scrollWithCarry(relayScrollTarget, requestedX, requestedY, relayScrollState);
        send({ type: 'moa-scrolled', id: data.id, dx: took.dx, dy: took.dy });
      } else {
        var scroll = { left: requestedX, top: requestedY, behavior: 'instant' };
        if (relayScrollTarget) relayScrollTarget.scrollBy(scroll);
        else window.scrollBy(scroll);
      }
      return;
    }
    if (data.type === 'moa-inspect-tap') {
      relayScrollTarget = null;
      relayScrollPicked = false;
      var inspected = elementAt(data.x, data.y);
      if (!inspected) return;
      pinned = inspected;
      paint(inspected);
      send(payload(inspected));
      return;
    }
    if (data.type === 'moa-view') {
      // The shell's frame of reference. `zoom` changes constantly (every step
      // of a pinch); `epoch` changes only when the coordinates this document
      // reports stop meaning anything — a new document, a new viewport width, a
      // resized stage, a closed panel. Only the latter cancels a held Space,
      // because only the latter is a gesture that no longer has a subject.
      var nextEpoch = Number.isFinite(data.epoch) ? data.epoch : 0;
      if (viewEpoch !== null && nextEpoch !== viewEpoch) { setSpace(false); resetWheelScrollStates(); }
      viewEpoch = nextEpoch;
      viewZoom = Number.isFinite(data.zoom) ? data.zoom : 1;
      if (spaceDown && !(viewZoom > 1)) setSpace(false);
    }
  });

  document.addEventListener('keydown', onKeyDown, true);
  document.addEventListener('keydown', onSpaceDown, true);
  document.addEventListener('keyup', onSpaceUp, true);
  window.addEventListener('blur', releaseSpace);
  document.addEventListener('visibilitychange', releaseSpace);
  window.addEventListener('wheel', onWheel, { passive: false });
  window.addEventListener('gesturestart', onGestureStart, { passive: false });
  window.addEventListener('gesturechange', onGestureChange, { passive: false });
  window.addEventListener('gestureend', endGesture, { passive: false });
  window.addEventListener('gesturecancel', endGesture, { passive: false });

  send({ type: 'moa-ready', chain: true });
})();
