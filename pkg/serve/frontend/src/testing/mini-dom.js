// mini-dom.js — just enough of a DOM for preact's real render() in bun tests,
// so a component runs with its real hooks and can be rendered again with new
// props (an empty list, then the GET's answer). Not a browser: no layout, no
// CSS, events only by dispatch(). Install with installMiniDom(); it returns
// the function that puts the previous globals back.

class Node {
  constructor(nodeType, name) {
    this.nodeType = nodeType;
    this.nodeName = name;
    this.localName = name && name.toLowerCase();
    this.childNodes = [];
    this.parentNode = null;
    this.attributes = {};
    this.listeners = {};
    this.style = { setProperty: (k, v) => { this.style[k] = v; } };
  }
  get firstChild() { return this.childNodes[0] || null; }
  get nextSibling() {
    const kids = this.parentNode?.childNodes || [];
    return kids[kids.indexOf(this) + 1] || null;
  }
  insertBefore(node, ref) {
    node.parentNode?.removeChild(node);
    const i = ref ? this.childNodes.indexOf(ref) : -1;
    if (i < 0) this.childNodes.push(node); else this.childNodes.splice(i, 0, node);
    node.parentNode = this;
    return node;
  }
  appendChild(node) { return this.insertBefore(node, null); }
  removeChild(node) {
    const i = this.childNodes.indexOf(node);
    if (i >= 0) this.childNodes.splice(i, 1);
    node.parentNode = null;
    return node;
  }
  remove() { this.parentNode?.removeChild(this); }
  setAttribute(k, v) { this.attributes[k] = String(v); }
  getAttribute(k) { return k in this.attributes ? this.attributes[k] : null; }
  removeAttribute(k) { delete this.attributes[k]; }
  addEventListener(t, fn) { (this.listeners[t] ||= []).push(fn); }
  removeEventListener(t, fn) { this.listeners[t] = (this.listeners[t] || []).filter((f) => f !== fn); }
  dispatch(type) {
    const event = { type, target: this, currentTarget: this, stopPropagation() { this.stopped = true; }, preventDefault() {} };
    for (let n = this; n && !event.stopped; n = n.parentNode) {
      event.currentTarget = n;
      // preact names the event "Click" when "onclick" is not a property of
      // the node, as here: match the type whatever its case.
      for (const [t, fns] of Object.entries(n.listeners)) {
        if (t.toLowerCase() === type) fns.forEach((fn) => fn.call(n, Object.assign(event, { type: t })));
      }
    }
  }
  get textContent() { return this.nodeType === 3 ? this.data : this.childNodes.map((c) => c.textContent).join(""); }
  get className() { return this.attributes.class || ""; }
  // all — every element below that matches the predicate.
  all(match) {
    const out = [];
    const walk = (n) => n.childNodes.forEach((c) => { if (c.nodeType === 1 && match(c)) out.push(c); walk(c); });
    walk(this);
    return out;
  }
  byClass(name) { return this.all((n) => n.className.split(/\s+/).includes(name)); }
}

export function installMiniDom() {
  const saved = { document: globalThis.document };
  const document = {
    createElement: (t) => new Node(1, t.toUpperCase()),
    createElementNS: (_ns, t) => new Node(1, t),
    createTextNode: (data) => Object.assign(new Node(3, "#text"), { data: String(data) }),
    addEventListener() {},
    removeEventListener() {},
  };
  document.body = document.createElement("body");
  globalThis.document = document;
  return () => { globalThis.document = saved.document; };
}
