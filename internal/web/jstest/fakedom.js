"use strict";
// fakedom.js: the hand-written fake DOM the pane-view tests share (the repo has no jsdom). Just enough
// of Node and Text for the code under test: children, class and attribute bookkeeping, focus, click,
// after / remove / contains, and a querySelector over a class or a tag name. One page's scripts and
// `document` are the test's own: see page() in activitytab.test.js and elevbanner.test.js.

class Text { constructor(t) { this.text = t; this.parent = null; } get textContent() { return this.text; } set textContent(v) { this.text = String(v); } }
class Node {
  constructor(tag, doc) {
    this.tag = tag; this.doc = doc; this.children = []; this.parent = null; this.own = "";
    this.className = ""; this.attrs = {}; this.style = {}; this.dataset = {}; this.hidden = false; this.disabled = false;
    this.isConnected = true; this.tabIndex = -1;
    const self = this;
    this.classList = {
      toggle(c, on) {
        const set = new Set(self.className.split(/\s+/).filter(Boolean));
        if (on === undefined ? !set.has(c) : on) set.add(c); else set.delete(c);
        self.className = [...set].join(" ");
      },
      contains: (c) => self.className.split(/\s+/).includes(c),
    };
  }
  get textContent() { return this.own + this.children.map((c) => c.textContent).join(""); }
  set textContent(v) { this.children.forEach((c) => { c.parent = null; }); this.children = []; this.own = String(v); }
  get firstChild() { return this.children[0] || null; }
  get parentNode() { return this.parent; }
  get parentElement() { return this.parent; }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  removeAttribute(k) { delete this.attrs[k]; }
  appendChild(c) { if (c.parent) c.parent.children.splice(c.parent.children.indexOf(c), 1); c.parent = this; this.children.push(c); return c; }
  append(...cs) { cs.forEach((c) => this.appendChild(c)); }
  replaceChildren(...cs) { this.children.forEach((c) => { c.parent = null; }); this.children = []; this.own = ""; cs.forEach((c) => this.appendChild(c)); }
  remove() { if (this.parent) { this.parent.children.splice(this.parent.children.indexOf(this), 1); this.parent = null; } }
  // Inserts `c` as this node's next sibling (moving it if it is already placed).
  after(c) {
    if (!this.parent) return;
    if (c.parent) c.parent.children.splice(c.parent.children.indexOf(c), 1);
    c.parent = this.parent;
    this.parent.children.splice(this.parent.children.indexOf(this) + 1, 0, c);
  }
  contains(n) { for (let x = n; x; x = x.parent) if (x === this) return true; return false; }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  focus() { this.doc.activeElement = this; }
  click() { if (this.onclick) this.onclick({ target: this }); }
  all(sel, out) {
    for (const c of this.children) {
      if (!(c instanceof Node)) continue;
      const hit = sel[0] === "." ? c.className.split(/\s+/).includes(sel.slice(1)) : c.tag === sel;
      if (hit) out.push(c);
      c.all(sel, out);
    }
    return out;
  }
  querySelectorAll(sel) { return this.all(sel, []); }
  querySelector(sel) { return this.all(sel, [])[0] || null; }
}

module.exports = { Node, Text };
