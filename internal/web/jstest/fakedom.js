"use strict";
// fakedom.js: the hand-written fake DOM the pane-view tests share (the repo has no jsdom). Just enough
// of Node and Text for the code under test: children, class and attribute bookkeeping, focus, click,
// after / remove / contains, and a querySelector over compound selectors (tag, .class, [attr], [attr="v"])
// and descendant chains of them. A dataset write is mirrored to its data-* attribute, so [data-x="v"] finds
// it. One page's scripts and `document` are the test's own: see page() in activitytab.test.js,
// elevbanner.test.js and accesstab.test.js.

class Text { constructor(t) { this.text = t; this.parent = null; } get textContent() { return this.text; } set textContent(v) { this.text = String(v); } }
class Node {
  constructor(tag, doc) {
    this.tag = tag; this.doc = doc; this.children = []; this.parent = null; this.own = "";
    this.className = ""; this.attrs = {}; this.style = {}; this.hidden = false; this.disabled = false;
    this.isConnected = true; this.tabIndex = -1;
    const self = this;
    // dataset.x = "v" is the attribute data-x, as in a browser, so a [data-x="v"] selector finds it.
    this.dataset = new Proxy({}, {
      set(t, k, v) { t[k] = String(v); self.attrs["data-" + String(k).replace(/[A-Z]/g, (m) => "-" + m.toLowerCase())] = String(v); return true; },
      deleteProperty(t, k) { delete t[k]; delete self.attrs["data-" + String(k).replace(/[A-Z]/g, (m) => "-" + m.toLowerCase())]; return true; },
    });
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
  // Inserts `c` before `ref` (at the end for null), moving it if it is already placed. A `ref` that is no child of this node throws,
  // as the DOM's NotFoundError does (#350: a lax fake hid exactly that throw).
  insertBefore(c, ref) {
    if (ref && ref.parent !== this) throw new Error("NotFoundError: insertBefore: the reference node is not a child of this node");
    if (c.parent) c.parent.children.splice(c.parent.children.indexOf(c), 1);
    c.parent = this;
    const at = ref ? this.children.indexOf(ref) : -1;
    if (at < 0) this.children.push(c); else this.children.splice(at, 0, c);
    return c;
  }
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
  // Matches one compound selector (an optional tag, then any number of .classes and [attr] / [attr="v"] tests).
  is(compound) {
    const m = /^([a-z][a-z0-9]*)?((?:\.[\w-]+|\[[\w-]+(?:="[^"]*")?\])*)$/i.exec(compound);
    if (!m) return false;
    if (m[1] && this.tag !== m[1]) return false;
    const have = this.className.split(/\s+/);
    for (const t of m[2].match(/\.[\w-]+|\[[^\]]+\]/g) || []) {
      if (t[0] === ".") { if (!have.includes(t.slice(1))) return false; continue; }
      const [k, v] = t.slice(1, -1).split("=");
      const got = this.getAttribute(k);
      if (got === null || (v !== undefined && got !== v.replace(/^"|"$/g, ""))) return false;
    }
    return true;
  }
  // Descendant chains ("a .b c") over compound selectors. The last compound must match this node; the
  // rest must match ancestors, in order, below `root` (the node the query started on).
  matches(chain, root) {
    if (!this.is(chain[chain.length - 1])) return false;
    let i = chain.length - 2;
    for (let a = this.parent; i >= 0 && a && a !== root.parent; a = a.parent) if (a instanceof Node && a.is(chain[i])) i--;
    return i < 0;
  }
  all(sel, out, root) {
    const chain = sel.trim().split(/\s+/);
    for (const c of this.children) {
      if (!(c instanceof Node)) continue;
      if (c.matches(chain, root || this)) out.push(c);
      c.all(sel, out, root || this);
    }
    return out;
  }
  querySelectorAll(sel) { return this.all(sel, []); }
  querySelector(sel) { return this.all(sel, [])[0] || null; }
}

module.exports = { Node, Text };
