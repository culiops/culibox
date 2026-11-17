"use strict";

// The Files dock's editor tab strip and breadcrumb row (spec 5.4, mockup B1): DOM only.
// What a tab IS belongs to docktabs.js and what the crumbs say to dockcrumbs.js;
// filesdrawer.js asks them and hands the answers here to draw: dockTabItems' view
// model for the strip, crumbsFor's list and editedLabel's text for the row, and the
// callbacks a click should call. Nothing in this file decides anything about files.
//
// The elements are index.html's (#dock-bar > #dock-tabs, #dock-acts; #dock-crumbs >
// #dock-trail, #dock-edited), inside the dock's viewer column, so the phone, where
// the dock is not shown, never reaches any of it. Every name, path and label goes to
// the page as text.

const DK_STEP_KEYS = { ArrowLeft: -1, ArrowRight: 1, Home: "home", End: "end" };

// The callbacks of the latest paint, read when an event fires so a tab element that
// outlives a repaint still calls the current ones.
let dkTabOn = {};
let dkCrumbOn = {};
// key -> the tab's element. A repaint updates these in place rather than building
// new ones: a double-click needs the element its first click landed on, and a
// keyboard user's focus must not be lost to a repaint (the dirty dot arrives while
// one is typing in the file beside it).
const dkTabEls = new Map();
let dkItems = [];        // the items of the latest paint, for the arrow keys
let dkStop = null;       // the tab that is the strip's Tab stop (roving tabindex)
let dkActiveSeen = null; // the active key the strip was last scrolled to
let dkTrailSig = "";     // what the trail was last built from

function dkCall(fn, ...args) {
  if (typeof fn === "function") fn(...args);
}

// dkReveal scrolls the tab strip sideways just far enough to show `el`, by hand: an
// element's own scrollIntoView would also move every scrollable ancestor.
function dkReveal(host, el) {
  const l = el.offsetLeft;
  const r = l + el.offsetWidth;
  if (l < host.scrollLeft) host.scrollLeft = l;
  else if (r > host.scrollLeft + host.clientWidth) host.scrollLeft = r - host.clientWidth;
}

// dkClipped: is `el` half in view, half out? A tab whose width grew after it was
// revealed (a git letter arrives on one before it) is left cut at the edge. One
// scrolled wholly out of view was put there by the user and is left alone.
function dkClipped(host, el) {
  const l = el.offsetLeft;
  const r = l + el.offsetWidth;
  const from = host.scrollLeft;
  const to = from + host.clientWidth;
  return (l < from && r > from) || (r > to && l < to);
}

// dkKey: the keys a focused tab answers to. Plain keys only: a chord belongs to the
// app (shortcuts.js). Arrows, Home and End move focus along the strip; Enter and
// Space show the tab; Delete closes it, which asks first when it holds an edit. On a Mac the key that is labelled delete is
// Backspace (there is no forward-delete key on a laptop), so it closes the tab there too.
function dkKey(e, el) {
  if (e.ctrlKey || e.metaKey || e.altKey || e.shiftKey) return;
  const key = el.dataset.key;
  if (e.key in DK_STEP_KEYS) {
    const to = dkTabEls.get(dockStepKey({ tabs: dkItems }, key, DK_STEP_KEYS[e.key]));
    if (to) { e.preventDefault(); to.focus(); }
  } else if (e.key === "Enter" || e.key === " ") {
    e.preventDefault();
    dkCall(dkTabOn.activate, key);
  } else if (e.key === "Delete" || (e.key === "Backspace" && typeof IS_MAC !== "undefined" && IS_MAC)) {
    e.preventDefault();
    dkCall(dkTabOn.close, key);
  }
}

// dkSpan: a text-only span with a class.
function dkSpan(cls) {
  const s = document.createElement("span");
  s.className = cls;
  return s;
}

// dkTabBuild makes one tab's element: icon, name, git letter, the dirty dot and the
// close button. dkTabFill, not this, says what they show.
function dkTabBuild() {
  const el = document.createElement("div");
  el.setAttribute("role", "tab");
  const ic = dkSpan("dk-ic");
  ic.setAttribute("aria-hidden", "true");
  const dot = dkSpan("dk-dirty");
  dot.setAttribute("role", "img");
  dot.setAttribute("aria-label", "Unsaved changes");
  dot.title = "Unsaved changes";
  const x = document.createElement("button");
  x.type = "button";
  x.className = "dk-x";
  x.tabIndex = -1; // the tab itself is the Tab stop, and Delete closes it
  x.textContent = "✕";
  el.append(ic, dkSpan("dk-name"), dkSpan("dk-st"), dot, x);
  el.onclick = () => { dkStop = el.dataset.key; dkCall(dkTabOn.activate, el.dataset.key); };
  el.ondblclick = () => dkCall(dkTabOn.pin, el.dataset.key);
  // Middle-click closes. Its mousedown is the browser's autoscroll on some systems.
  el.onmousedown = (e) => { if (e.button === 1) e.preventDefault(); };
  el.onauxclick = (e) => { if (e.button === 1) { e.preventDefault(); dkCall(dkTabOn.close, el.dataset.key); } };
  el.onkeydown = (e) => dkKey(e, el);
  el.onfocus = () => { dkStop = el.dataset.key; dkRove(); };
  x.onclick = (e) => { e.stopPropagation(); dkCall(dkTabOn.close, el.dataset.key); };
  return el;
}

// dkRove makes the tab named by dkStop the strip's one Tab stop.
function dkRove() {
  for (const [key, el] of dkTabEls) el.tabIndex = key === dkStop ? 0 : -1;
}

// dkTabFill brings one tab's element in line with its item. Text is written only
// when it changed, so a repaint that changes nothing costs the page nothing.
function dkTabFill(el, it) {
  const set = (cls, text) => {
    const n = el.querySelector("." + cls);
    if (n && n.textContent !== text) n.textContent = text;
    return n;
  };
  el.dataset.key = it.key;
  const cls = "dk-tab" + (it.active ? " dk-tab-on" : "") + (it.preview ? " dk-prev" : "");
  if (el.className !== cls) el.className = cls;
  el.setAttribute("aria-selected", it.active ? "true" : "false");
  el.title = it.title;
  el.dataset.dirty = it.dirty ? "1" : "";
  el.dataset.box = it.box ? "1" : "";
  set("dk-ic", typeof iconFor === "function" ? iconFor(it.path.split("/").pop() || it.path, false, false) : "");
  set("dk-name", it.name);
  const st = set("dk-st", it.letter);
  if (st) {
    st.className = "dk-st" + (it.letter ? " dk-st-" + it.letter.toLowerCase() : "");
    st.title = it.letter ? { M: "Modified", A: "Added (staged)", U: "Untracked", C: "Conflict" }[it.letter] : "";
  }
  const x = el.querySelector(".dk-x");
  if (x) {
    x.title = "Close " + it.name;
    x.setAttribute("aria-label", "Close " + it.name);
  }
}

// dockStripPaint draws the strip for `items` (dockTabItems), and hides it when there
// is no tab. `on` = {activate(key), pin(key), close(key)}.
function dockStripPaint(items, on) {
  dkTabOn = on || {};
  dkItems = items;
  const bar = document.getElementById("dock-bar");
  const host = document.getElementById("dock-tabs");
  if (!bar || !host) return;
  bar.hidden = items.length === 0;
  const live = new Set(items.map((it) => it.key));
  for (const [key, el] of dkTabEls) {
    if (live.has(key)) continue;
    el.remove();
    dkTabEls.delete(key);
  }
  const order = items.map((it) => {
    let el = dkTabEls.get(it.key);
    if (!el) { el = dkTabBuild(); dkTabEls.set(it.key, el); }
    dkTabFill(el, it);
    return el;
  });
  // Reordering takes a focused tab out of the document and puts it back, which drops
  // focus: only when the order is not what is drawn.
  if (host.children.length !== order.length || order.some((el, i) => host.children[i] !== el)) host.replaceChildren(...order);
  const active = items.find((it) => it.active);
  if (!live.has(dkStop)) dkStop = active ? active.key : items.length ? items[0].key : null;
  dkRove();
  const activeEl = active ? dkTabEls.get(active.key) : null;
  if (activeEl && (active.key !== dkActiveSeen || dkClipped(host, activeEl))) dkReveal(host, activeEl);
  dkActiveSeen = active ? active.key : null;
}

// dockCrumbsPaint draws the row under the strip: the path as buttons (`crumbs`, from
// crumbsFor; a click calls on.reveal(path), and with no on.reveal they are plain
// text, not buttons: a file inside a box is in no tree), and on the right the "Edited by" text
// (empty: nothing is drawn). No crumbs hides the row.
function dockCrumbsPaint(crumbs, edited, on) {
  dkCrumbOn = on || {};
  const row = document.getElementById("dock-crumbs");
  const trail = document.getElementById("dock-trail");
  const ed = document.getElementById("dock-edited");
  if (!row || !trail || !ed) return;
  row.hidden = crumbs.length === 0;
  const clickable = typeof dkCrumbOn.reveal === "function";
  const sig = (clickable ? "1" : "0") + crumbs.map((c) => c.path).join("\n");
  if (sig !== dkTrailSig) { // rebuilt only when the path changed: a crumb with focus keeps it
    dkTrailSig = sig;
    const els = [];
    crumbs.forEach((c, i) => {
      if (i > 0) {
        const sep = dkSpan("dk-sep");
        sep.textContent = "›";
        sep.setAttribute("aria-hidden", "true");
        els.push(sep);
      }
      const b = document.createElement(clickable ? "button" : "span");
      b.className = "dk-crumb";
      b.textContent = c.name;
      b.title = c.path;
      if (clickable) {
        b.type = "button";
        b.onclick = () => dkCall(dkCrumbOn.reveal, c.path);
      }
      if (i === crumbs.length - 1) b.setAttribute("aria-current", "page");
      els.push(b);
    });
    trail.replaceChildren(...els);
  }
  if (ed.textContent !== (edited || "")) ed.textContent = edited || "";
  ed.hidden = !edited;
}

// dockStripInit: a mouse wheel over a strip that overflows scrolls it sideways (a
// plain wheel only turns vertically), so a tab off the edge can be reached without a
// trackpad. A sideways wheel or Shift+wheel is the browser's own.
function dockStripInit() {
  const host = typeof document !== "undefined" ? document.getElementById("dock-tabs") : null;
  if (!host || !host.addEventListener) return;
  host.addEventListener("wheel", (e) => {
    if (host.scrollWidth <= host.clientWidth || Math.abs(e.deltaY) <= Math.abs(e.deltaX)) return;
    host.scrollLeft += e.deltaY;
    e.preventDefault();
  }, { passive: false });
}

dockStripInit();
