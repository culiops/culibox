"use strict";

// Find in file and Go to line for the viewer (#51 D5-D8).
//
// Pure part (node-testable): vfFold, vfFindAll, vfPrefix, vfLocate, vfCountLabel.
// Browser part: VFRead / VFEdit (the two search engines) and ViewerFind (the bar
// that drives one of them). Plain text, case-insensitive, no replace, no regex:
// the anti-goals in the design are IDE find.

// Matches beyond this are not collected; the count then reads "10000+".
const VF_CAP = 10000;
// CodeMirror marks beyond this are not drawn (only the current match is).
const VF_CM_MARKS = 2000;

// vfFold lowercases one UTF-16 unit at a time and keeps a unit as it is when its
// lowercase is not one unit long ("İ".toLowerCase() is two), so the folded text
// has the same length as the original and a match offset in one is an offset in
// the other. Done per unit (not on the whole string) so a query and a text fold
// the same way whatever surrounds them (final sigma).
function vfFold(s) {
  if (!/[^\x00-\x7f]/.test(s)) return s.toLowerCase();
  const out = new Array(s.length);
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    const l = c.toLowerCase();
    out[i] = l.length === 1 ? l : c;
  }
  return out.join("");
}

// vfFindAll lists the non-overlapping, ordered, case-insensitive occurrences of
// `query` in `text` as [start, end) offsets, stopping once `cap` are found.
// `capped` says there were more. An empty query finds nothing.
function vfFindAll(text, query, cap) {
  const hits = [];
  if (!query) return { hits, capped: false };
  const t = vfFold(text);
  const q = vfFold(query);
  let from = 0;
  for (;;) {
    const i = t.indexOf(q, from);
    if (i < 0) return { hits, capped: false };
    if (hits.length >= cap) return { hits, capped: true };
    hits.push([i, i + q.length]);
    from = i + q.length;
  }
}

// vfPrefix: prefix[i] is the offset where segment i starts; the last entry is the
// total length.
function vfPrefix(segments) {
  const p = new Array(segments.length + 1);
  p[0] = 0;
  for (let i = 0; i < segments.length; i++) p[i + 1] = p[i] + segments[i].length;
  return p;
}

// vfLocatePrefix maps an offset in the concatenation to {seg, off}. A START
// offset on a boundary belongs to the segment that begins there; an END offset
// (isEnd) to the one that ends there, so a match never begins or ends on an
// empty edge. Empty segments are skipped either way. Binary search: a minified
// line can hold thousands of text nodes.
function vfLocatePrefix(prefix, offset, isEnd) {
  const n = prefix.length - 1;
  if (n <= 0) return { seg: 0, off: 0 };
  const total = prefix[n];
  if (offset > total) offset = total;
  let lo = 0, hi = n - 1;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    const reach = prefix[mid + 1];
    if (isEnd ? reach >= offset : reach > offset) hi = mid;
    else lo = mid + 1;
  }
  return { seg: lo, off: offset - prefix[lo] };
}

function vfLocate(segments, offset, isEnd) {
  return vfLocatePrefix(vfPrefix(segments), offset, !!isEnd);
}

// vfLineSubmit decides what Enter (or Go) does in line mode. A whole number goes
// to that line (revealLine clamps and toasts a line past the end); anything else
// (empty, letters, "3x") leaves the bar open for another try. `total` is accepted
// so the clamp can move here without changing callers.
function vfLineSubmit(raw, total) {
  void total;
  const m = /^\s*([+-]?\d+)\s*$/.exec(String(raw == null ? "" : raw));
  if (!m) return { action: "stay" };
  return { action: "go", line: parseInt(m[1], 10) };
}

function vfCountLabel(cur, total, capped) {
  if (!total) return "No results";
  return cur + " of " + total + (capped ? "+" : "");
}

// --- engines ---------------------------------------------------------------
// An engine owns the marks of one search. Shared interface:
//   search(query, anchor, quiet) -> {total, capped, cur}   (cur is 1-based, 0 = none)
//   step(dir) -> cur            move to the next (+1) / previous (-1), wrapping
//   clear()                     remove every mark
//   anchor() -> number          where the current match starts (offset into the text)
// `quiet` repaints without moving the cursor or the scroll (a re-render while
// the user is typing in the editor).

const VF_BLOCKS = "p,li,h1,h2,h3,h4,h5,h6,pre,td,th,dt,dd,blockquote,figcaption,summary,div";

// VFRead searches the rendered read view: the `.vl-c` cells of a code view, or
// the blocks of rendered markdown. A match may span two hljs tokens (two text
// nodes of one line), so a line's text is the concatenation of its text nodes.
// Highlights are CSS Custom Highlight API ranges ("vf-all", "vf-cur"): the DOM
// is never touched.
class VFRead {
  constructor(viewer) {
    this.v = viewer;
    this.root = null;      // the element the groups were collected from
    this.groups = [];      // {nodes, prefix, start, row}
    this.text = "";        // every group's text, joined with "\n"
    this.ranges = [];
    this.starts = [];      // joined-text start offset of each hit
    this.rows = [];        // the row element of each hit (scroll target)
    this.cur = -1;
    this.capped = false;
    this.painted = false;  // this engine's highlights are the registered ones
  }

  static supported() {
    return typeof CSS !== "undefined" && !!CSS.highlights && typeof Highlight === "function";
  }

  // collect (re)reads the text nodes under the rendered view. Cached against the
  // root element: a re-render makes a new one.
  collect() {
    const content = this.v.content;
    const root = content.querySelector(".viewer-pre, .md-body");
    if (root === this.root && root) return !!root;
    this.root = root;
    this.groups = [];
    this.text = "";
    if (!root) return false;
    const code = root.classList.contains("viewer-pre");
    const w = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    let last = null, g = null, pos = 0;
    const parts = [];
    for (let n = w.nextNode(); n; n = w.nextNode()) {
      const p = n.parentElement;
      if (!p) continue;
      let key;
      if (code) {
        if (p.closest(".vl-n")) continue;
        key = p.closest(".vl-c");
      } else {
        key = p.closest(VF_BLOCKS);
      }
      if (!key) continue;
      if (key !== last) {
        last = key;
        g = { nodes: [], texts: [], key, row: code ? key.closest(".vl") : key, start: 0 };
        this.groups.push(g);
      }
      g.nodes.push(n);
      g.texts.push(n.data);
    }
    for (const gr of this.groups) {
      gr.prefix = vfPrefix(gr.texts);
      gr.start = pos;
      parts.push(gr.texts.join(""));
      pos += gr.prefix[gr.prefix.length - 1] + 1; // +1: the "\n" between groups
    }
    this.text = parts.join("\n");
    return true;
  }

  search(query, anchor, quiet) {
    this.clearPaint();
    this.ranges = []; this.starts = []; this.rows = []; this.cur = -1;
    if (!query || !this.collect()) return { total: 0, capped: false, cur: 0 };
    const r = vfFindAll(this.text, query, VF_CAP);
    this.capped = r.capped;
    let gi = 0;
    const groups = this.groups;
    for (const [s, e] of r.hits) {
      while (gi + 1 < groups.length && groups[gi + 1].start <= s) gi++;
      const g = groups[gi];
      const a = vfLocatePrefix(g.prefix, s - g.start, false);
      const b = vfLocatePrefix(g.prefix, e - g.start, true);
      const range = document.createRange();
      range.setStart(g.nodes[a.seg], a.off);
      range.setEnd(g.nodes[b.seg], b.off);
      this.ranges.push(range);
      this.starts.push(s);
      this.rows.push(g.row);
    }
    if (!this.ranges.length) return { total: 0, capped: false, cur: 0 };
    let i = this.starts.findIndex((s) => s >= (anchor || 0));
    if (i < 0) i = 0;
    this.cur = i;
    this.paint(quiet);
    return { total: this.ranges.length, capped: this.capped, cur: i + 1 };
  }

  step(dir) {
    if (!this.ranges.length) return 0;
    const n = this.ranges.length;
    this.cur = (this.cur + dir + n) % n;
    if (this.painted && VFRead.supported()) {
      // Only the current match moved: the all-matches highlight stays as it is.
      const now = new Highlight(this.ranges[this.cur]);
      now.priority = 1;
      CSS.highlights.set("vf-cur", now);
      this.v.revealRange(this.ranges[this.cur], this.rows[this.cur]);
    } else this.paint(false);
    return this.cur + 1;
  }

  anchor() { return this.cur >= 0 ? this.starts[this.cur] : 0; }

  paint(quiet) {
    const cur = this.ranges[this.cur];
    if (VFRead.supported()) {
      const all = new Highlight(...this.ranges);
      const now = new Highlight(cur);
      now.priority = 1; // the current match wins where the two overlap
      CSS.highlights.set("vf-all", all);
      CSS.highlights.set("vf-cur", now);
      this.painted = true;
    } else if (!quiet) {
      // No Highlight API: the current match is the selection, and the rest go
      // untinted.
      const sel = getSelection();
      sel.removeAllRanges();
      sel.addRange(cur);
    }
    if (!quiet) this.v.revealRange(cur, this.rows[this.cur]);
  }

  clearPaint() {
    // Only if they are ours: another viewer's find may hold the global names.
    if (this.painted && VFRead.supported()) { CSS.highlights.delete("vf-all"); CSS.highlights.delete("vf-cur"); }
    this.painted = false;
  }

  total() { return this.ranges.length; }

  clear() {
    this.clearPaint();
    this.ranges = []; this.starts = []; this.rows = []; this.cur = -1;
    this.root = null; this.groups = []; this.text = "";
  }
}

// VFEdit searches the CodeMirror 5 buffer with core API only: getValue,
// posFromIndex, markText, setSelection, scrollIntoView. CodeMirror draws only the
// lines near the viewport (measured, plan Task 4), so the browser's own find
// cannot see the rest, and none of this needs the vendored search addon.
class VFEdit {
  constructor(viewer) {
    this.v = viewer;
    this.hits = [];
    this.marks = [];
    this.curMark = null;
    this.cur = -1;
    this.capped = false;
  }

  get cm() { return this.v.cm; }

  search(query, anchor, quiet) {
    this.clearMarks();
    this.hits = []; this.cur = -1;
    const cm = this.cm;
    if (!query || !cm) return { total: 0, capped: false, cur: 0 };
    const r = vfFindAll(cm.getValue(), query, VF_CAP);
    this.hits = r.hits; this.capped = r.capped;
    if (!this.hits.length) return { total: 0, capped: false, cur: 0 };
    let i = this.hits.findIndex((h) => h[0] >= (anchor || 0));
    if (i < 0) i = 0;
    this.cur = i;
    this.paint(quiet);
    return { total: this.hits.length, capped: this.capped, cur: i + 1 };
  }

  step(dir) {
    if (!this.hits.length) return 0;
    const n = this.hits.length;
    this.cur = (this.cur + dir + n) % n;
    this.paint(false);
    return this.cur + 1;
  }

  anchor() { return this.cur >= 0 ? this.hits[this.cur][0] : this.cm ? this.cm.indexFromPos(this.cm.getCursor("from")) : 0; }

  paint(quiet) {
    const cm = this.cm;
    if (!cm) return;
    this.clearMarks();
    const pos = (h) => [cm.posFromIndex(h[0]), cm.posFromIndex(h[1])];
    cm.operation(() => {
      if (this.hits.length <= VF_CM_MARKS) {
        for (let i = 0; i < this.hits.length; i++) {
          if (i === this.cur) continue;
          const [a, b] = pos(this.hits[i]);
          this.marks.push(cm.markText(a, b, { className: "cm-find" }));
        }
      }
      const [a, b] = pos(this.hits[this.cur]);
      this.curMark = cm.markText(a, b, { className: "cm-find-cur" });
      if (!quiet) {
        cm.setSelection(a, b);
        cm.scrollIntoView({ from: a, to: b }, 120);
      }
    });
  }

  clearMarks() {
    for (const m of this.marks) m.clear();
    this.marks = [];
    if (this.curMark) { this.curMark.clear(); this.curMark = null; }
  }

  total() { return this.hits.length; }

  clear() { this.clearMarks(); this.hits = []; this.cur = -1; }
}

// The one ViewerFind whose highlights are registered under the fixed CSS names
// ("vf-all", "vf-cur" are global to the document, so only one viewer can show
// them). Opening or repainting another bar takes them over.
let vfOwner = null;

// ViewerFind is the bar plus the engine it drives. It is mounted in the viewer's
// element between the toolbar and the scroller, so a re-render of the scroller
// never removes it. Two modes share the bar: "find" and "line" (Go to line).
class ViewerFind {
  constructor(viewer) {
    this.v = viewer;
    this.mode = "find";
    this.engine = null;
    this.timer = null;
    this.query = "";       // the query the engine last ran
    this.findQuery = "";   // the text the find input held when it was last left
    this.cmOff = null;
    const bar = document.createElement("div");
    bar.className = "vf-bar";
    bar.setAttribute("role", "search");
    bar.hidden = true;
    const input = document.createElement("input");
    input.className = "vf-input";
    input.type = "text";
    input.autocomplete = "off";
    input.spellcheck = false;
    input.setAttribute("autocapitalize", "off");
    const count = document.createElement("span");
    count.className = "vf-count";
    count.setAttribute("aria-live", "polite");
    const mk = (cls, text, label, title, fn) => {
      const b = document.createElement("button");
      b.type = "button";
      b.className = "vf-btn " + cls;
      b.textContent = text;
      b.title = title;
      b.setAttribute("aria-label", label);
      b.onclick = fn;
      return b;
    };
    // The step keys are not registry chords; renderChord is the registry's own
    // renderer (the hint-drift guard bans spelling a key by hand).
    const enter = renderChord({ keys: ["Enter"] }, IS_MAC);
    const shiftEnter = renderChord({ shift: true, keys: ["Enter"] }, IS_MAC);
    this.prev = mk("vf-prev", "↑", "Previous match", "Previous match (" + shiftEnter + ")", () => { this.step(-1); this.input.focus(); });
    this.next = mk("vf-next", "↓", "Next match", "Next match (" + enter + ")", () => { this.step(+1); this.input.focus(); });
    this.go = mk("vf-go", "Go", "Go to line", "Go to the line (" + enter + ")", () => this.submitLine());
    this.closeBtn = mk("vf-close", "×", "Close find bar", "Close (" + renderChord({ keys: ["Escape"] }, IS_MAC) + ")", () => this.close());
    bar.append(input, count, this.prev, this.next, this.go, this.closeBtn);
    this.el = bar; this.input = input; this.count = count;
    input.addEventListener("input", () => this.onInput());
    input.addEventListener("keydown", (e) => this.onKey(e));
    this.setMode("find");
  }

  get isOpen() { return !this.el.hidden; }

  // setMode swaps the controls and the labels. The find query is stashed when
  // leaving find mode and put back when returning to it.
  setMode(mode) {
    if (this.mode === "find" && mode === "line") this.findQuery = this.input.value;
    const wasLine = this.mode === "line";
    this.mode = mode;
    const line = mode === "line";
    this.el.dataset.mode = mode;
    this.prev.hidden = this.next.hidden = this.count.hidden = line;
    this.go.hidden = !line;
    if (line) {
      this.input.setAttribute("inputmode", "numeric");
      this.input.placeholder = "Go to line (1–" + Math.max(1, this.v.lineTotal()) + ")";
      this.input.setAttribute("aria-label", "Go to line");
      this.input.title = "Go to line (" + shortcutHint("viewer.gotoLine", IS_MAC) + ")";
      this.input.value = "";
      this.count.textContent = "";
      this.el.classList.remove("vf-none");
      if (this.engine) this.engine.clear(); // the find marks go while the bar is a line box
    } else {
      this.input.removeAttribute("inputmode");
      this.input.placeholder = "Find in file";
      this.input.setAttribute("aria-label", "Find in file");
      this.input.title = "Find in file (" + shortcutHint("viewer.find", IS_MAC) + ")";
      if (wasLine) this.input.value = this.findQuery;
    }
  }

  // open shows the bar in `mode`, focused. A non-empty single-line selection
  // inside the viewer prefills (and selects) the find query.
  open(mode) {
    this.el.hidden = false;
    if (mode !== this.mode) this.setMode(mode);
    else if (mode === "line") this.setMode("line"); // refresh the 1-N range
    if (mode === "find") {
      const sel = this.selectedText();
      if (sel) this.input.value = sel;
      else if (this.input.value === "") this.input.value = this.findQuery;
    }
    this.input.focus();
    this.input.select();
    if (mode === "find") this.refresh(false);
  }

  selectedText() {
    const v = this.v;
    let s = "";
    if (v.editing && v.cm) s = v.cm.getSelection();
    else {
      const sel = typeof getSelection === "function" ? getSelection() : null;
      if (sel && !sel.isCollapsed && sel.rangeCount && v.content.contains(sel.getRangeAt(0).commonAncestorContainer)) s = sel.toString();
    }
    return s && !/[\r\n]/.test(s) && s.trim() ? s : "";
  }

  // The engine for what the viewer shows now, created on demand.
  pick() {
    const kind = this.v.editing && this.v.cm ? "edit" : "read";
    if (this.engine && this.engine.kind === kind) return this.engine;
    if (this.engine) this.engine.clear();
    this.unwatchEditor();
    this.engine = kind === "edit" ? new VFEdit(this.v) : new VFRead(this.v);
    this.engine.kind = kind;
    if (kind === "edit") this.watchEditor();
    return this.engine;
  }

  // While the editor changes under an open bar the marks drift; rerun quietly.
  watchEditor() {
    const cm = this.v.cm;
    if (!cm) return;
    let t = null;
    const fn = () => {
      clearTimeout(t);
      t = setTimeout(() => { if (this.isOpen && this.mode === "find" && this.v.cm === cm) this.refresh(true); }, 250);
    };
    cm.on("change", fn);
    this.cmOff = () => { clearTimeout(t); cm.off("change", fn); };
  }

  unwatchEditor() { if (this.cmOff) { this.cmOff(); this.cmOff = null; } }

  claim() {
    if (vfOwner && vfOwner !== this) vfOwner.dropPaint();
    vfOwner = this;
  }

  // dropPaint takes this bar's highlights off the document (another viewer is
  // taking the names). The bar stays open; its next search repaints.
  dropPaint() {
    if (this.engine && this.engine.kind === "read") this.engine.clearPaint();
    if (vfOwner === this) vfOwner = null;
  }

  onInput() {
    clearTimeout(this.timer);
    if (this.mode === "line") return;
    this.timer = setTimeout(() => this.refresh(false), 80);
  }

  // refresh reruns the query against whatever is rendered now. Called on input,
  // after every re-render (reload, Follow) and when edit mode starts or ends.
  // `quiet` repaints without scrolling or moving the cursor.
  refresh(quiet) {
    if (!this.isOpen || this.mode !== "find") return;
    clearTimeout(this.timer);
    const eng = this.pick();
    this.claim();
    this.query = this.input.value;
    const r = eng.search(this.query, eng.anchor(), quiet);
    this.paintCount(r);
  }

  paintCount(r) {
    this.count.textContent = this.query ? vfCountLabel(r.cur, r.total, r.capped) : "";
    this.el.classList.toggle("vf-none", !!this.query && !r.total);
  }

  step(dir) {
    if (this.mode !== "find") return;
    // Another viewer took the highlight names (or nothing ran yet): search again.
    if (!this.engine || vfOwner !== this || this.query !== this.input.value) { this.refresh(false); return; }
    const cur = this.engine.step(dir);
    this.paintCount({ cur, total: this.engine.total(), capped: this.engine.capped });
  }

  // submitLine goes to the line and closes the bar, focus back on the reader or
  // the editor. A non-number keeps the bar and refocuses the input.
  submitLine() {
    const d = vfLineSubmit(this.input.value, this.v.lineTotal());
    if (d.action !== "go") { this.input.focus(); return; }
    this.v.revealLine(d.line);
    this.close(true); // noFocus: focus is placed explicitly below
    if (this.v.editing && this.v.cm) this.v.cm.focus();
    else this.v.focusReader();
  }

  onKey(e) {
    if (e.key === "Escape") {
      e.preventDefault(); e.stopPropagation();
      this.close();
    } else if (e.key === "Enter") {
      e.preventDefault(); e.stopPropagation();
      if (this.mode === "line") this.submitLine();
      else this.step(e.shiftKey ? -1 : +1);
    }
  }

  // leaveEdit drops the editor's marks as the editor goes away; the bar stays
  // open and the next render searches the read view.
  leaveEdit() {
    if (this.engine && this.engine.kind === "edit") {
      this.engine.clear();
      this.engine = null;
      this.unwatchEditor();
    }
  }

  // close hides the bar, removes every mark and gives focus back to the scroller
  // (or the editor) when it was in the bar. `noFocus` (dispose) must not steal it.
  close(noFocus) {
    clearTimeout(this.timer);
    const hadFocus = !noFocus && this.el.contains(document.activeElement);
    if (this.mode === "find") this.findQuery = this.input.value;
    this.el.hidden = true;
    this.unwatchEditor();
    if (this.engine) { this.engine.clear(); this.engine = null; }
    if (vfOwner === this) vfOwner = null;
    this.count.textContent = "";
    this.el.classList.remove("vf-none");
    if (hadFocus) {
      if (this.v.editing && this.v.cm) this.v.cm.focus();
      else this.v.focusReader();
    }
  }
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { vfFold, vfFindAll, vfPrefix, vfLocate, vfLocatePrefix, vfCountLabel, vfLineSubmit };
}
