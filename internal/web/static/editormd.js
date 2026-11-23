"use strict";

// The Markdown Preview of the Files dock's editor (spec "Editor (P2: Monaco)" → "Markdown"). For a .md or .markdown file the bar's
// toggle reads Preview | Source | Changes: Source is the Monaco editor (Code, for any other file), Changes is the inline diff vs git
// (editordiff.js, which builds the toggle and gives it this file's Preview button), and Preview, the one a markdown tab opens in, is
// a read view of the BUFFER, so an unsaved edit shows. It is the render the phone's viewer uses (mdRenderFragment, mdsafe.js:
// marked, DOMPurify and a walk), so it runs no script and loads nothing remote: a remote image is a notice that links to it.
//
// Preview is a third thing a tab's body can hold, beside the shared editor and the Changes view, and while it is up it has the
// body to itself: the editor keeps its model and its place (stashView) for the way back, and nothing in Preview needs it. The dirty
// dot, Mod+S (the tab's own keydown, so the pane takes the keyboard) and the conflict bar are the tab's and keep working: a buffer
// that is replaced under Preview (the agent's text coming in, Discard) is drawn again.
//
// What a tab keeps here, as `tab.md`:
//   view   "preview" | "source": which of the two the tab shows when it is not in Changes (Changes is `tab.diff`);
//   pane   the scroller that holds the rendered document, kept while the tab shows Source or Changes, null until the first Preview and
//          after the close;
//   text   the text the pane shows or is being drawn from (a render of the same text is skipped);
//   gen, job, drawn   the number of the latest draw, the timer of its next slice (a big file), the document complete in the pane;
//   restore, restoreAt   the place a first draw puts the pane back at after each slice, and where it last put it (a user's scroll stops it);
//   timer  the re-render that is 300 ms away, after an edit of the buffer;
//   sub    the subscription to the buffer's changes, held only while the tab is on screen in Preview;
//   top    where the pane was read, in px, kept for the way back (Source, Changes, another tab) and read from the pane while it is live;
//   live   the pane is on screen and being read.
//
// editorMdAttach(tab): the tab is on screen with its model and is not in Changes (EditorTab.show): Preview takes the body, or not.
// editorMdEarly(tab): the same for a tab whose file is read while Monaco is still loading: Preview from the read text, or "loading…".
// editorMdDetach(tab) / editorMdDrop(tab) / editorMdDispose(tab): the tab is hidden / Preview is left for another view / the tab is closed.

const EMD_DEBOUNCE_MS = 300; // the preview follows an edit of the buffer this long after its last change
const EMD_CHUNK_CHARS = 8192;  // the source a chunk of the document holds, about (mdRenderFragment's chunkChars)
const EMD_SLICE_MS = 30;       // how long a slice of a big file's draw runs before it lets the page breathe

function editorMdOf(tab) {
  if (!tab.md) tab.md = { view: "preview", pane: null, text: null, timer: null, sub: null, top: tab.lastScroll || 0, live: false, gen: 0, job: null, drawn: null, restore: null, restoreAt: null };
  return tab.md;
}

// editorMdIs: a file that has a Preview. An image or a binary file (the tab's inner viewer) has none.
function editorMdIs(tab) {
  return !tab.inner && MD_EXTS.has(extOf(tab.path));
}

// editorMdUp: Preview is in the tab's body (it is not while the tab shows Source or Changes).
function editorMdUp(tab) {
  const m = tab.md;
  return !!m && !!m.pane && m.pane.parentNode === tab.bodyEl;
}

// editorMdView: which of Preview and Source a markdown tab shows when it is not in Changes ("" for any other file).
function editorMdView(tab) {
  return editorMdIs(tab) ? editorMdOf(tab).view : "";
}

// editorMdScroll: where Preview is read, in px.
function editorMdScroll(tab) {
  const m = tab.md;
  return m.live ? m.pane.scrollTop : m.top;
}

// editorMdSay: a line in the pane in place of the document, saying why there is none.
function editorMdSay(msg) {
  const d = document.createElement("div");
  d.className = "viewer-msg";
  d.textContent = msg;
  return d;
}

// editorMdBody starts the render of `text`: {it}, the chunks mdRenderFragment hands back for the dock to draw a slice at a time
// (editorMdPump), or {el}, the whole of what goes in the pane: the document, or a line saying why it could not be drawn (the libraries
// did not load, the markup could not be made). Relative image paths are rewritten to the read route of the file's own directory, as
// the phone's viewer does.
function editorMdBody(tab, text) {
  if (typeof marked === "undefined" || !marked.parse || typeof DOMPurify === "undefined") return { el: editorMdSay("The preview needs marked and DOMPurify, which did not load. Source shows the file.") };
  const dir = tab.path.slice(0, tab.path.lastIndexOf("/") + 1);
  let out;
  try {
    out = mdRenderFragment(text, {
      marked,
      purify: DOMPurify,
      hljs: typeof hljs === "undefined" ? null : hljs,
      hlLabelledOnly: true,         // an unlabelled fence stays plain, as on GitHub: auto-detection was most of a long file's render
      hlMaxChars: MD_HL_MAX_CHARS,  // and a block over 100 KB is not highlighted
      chunkChars: EMD_CHUNK_CHARS,  // the document in chunks, so a big file never holds the page in one long task
      where: tab.path.slice(tab.path.lastIndexOf("/") + 1), // the link gate's dialog says which file the link is in
      rewrite: (rel) => (tab.box ? boxFileURL(tab.box, dir + rel) : "/api/fs/read?path=" + encodeURIComponent(dir + rel)),
    });
  } catch (_) {
    return { el: editorMdSay("The preview could not be drawn. Source shows the file.") };
  }
  if (out && typeof out.next === "function") return { it: out };
  const wrap = document.createElement("div");
  wrap.className = "md-body";
  wrap.appendChild(out);
  return { el: wrap };
}

// editorMdNow: a clock in ms for the pump's slices.
function editorMdNow() {
  return typeof performance !== "undefined" && performance && typeof performance.now === "function" ? performance.now() : Date.now();
}

// editorMdRender draws `text` (the buffer's, unless given) into the pane, unless the pane already shows this text. The pane is the
// scroller and stays, so a redraw keeps the place the document was read at (the browser only clamps it when the document got shorter).
// The document comes in chunks (editorMdBody): a file of one chunk is drawn here and now, as it always was; a bigger one is drawn
// a slice at a time (editorMdPump). A first draw goes into the pane as it is made, so the top of a long file shows at once; a redraw
// over a document already drawn is built aside and swapped in once complete, so the pane never shows half of a new text over the old.
// Each draw has a number (m.gen): a newer one, or the tab's close, stops the one under way.
function editorMdRender(tab, text) {
  const m = tab.md;
  if (text === undefined) text = tab.model.getValue();
  if (m.text === text) return;
  m.text = text;
  const gen = ++m.gen;
  if (m.job) { clearTimeout(m.job); m.job = null; }
  const body = editorMdBody(tab, text);
  if (body.el) { m.pane.replaceChildren(body.el); m.drawn = body.el; m.restore = null; m.restoreAt = null; return; }
  const wrap = document.createElement("div");
  wrap.className = "md-body";
  const job = { it: body.it, wrap, swap: !!m.drawn && m.drawn.parentNode === m.pane, held: [], count: 0 };
  if (!job.swap) { m.pane.replaceChildren(wrap); m.drawn = null; }
  editorMdPump(tab, gen, job);
}

// editorMdPump runs the draw's steps for EMD_SLICE_MS (at least one), puts what they made in the document, and leaves the rest to a
// next slice (setTimeout 0, so the page handles input and paints in between). A document of more than one chunk holds each in a
// .md-chunk (desktop.css: content-visibility, so the browser lays out only the chunks near the view; measured: laying out the whole
// 2 MiB test file at once was a 9.8 s task); a document of one chunk is that fragment alone, as before. While a first draw is under
// way, the pane is put back at the place it was read (m.restore) after each slice, until the user scrolls it themselves.
function editorMdPump(tab, gen, job) {
  const m = tab.md;
  m.job = null;
  if (tab.disposed || !m || m.gen !== gen || !m.pane) return;
  const t0 = editorMdNow();
  let done = false;
  try {
    do {
      const r = job.it.next();
      if (r.done) { done = true; break; }
      if (r.value) { job.held.push(r.value); job.count++; }
    } while (editorMdNow() - t0 < EMD_SLICE_MS);
  } catch (_) {
    const say = editorMdSay("The preview could not be drawn. Source shows the file.");
    m.pane.replaceChildren(say);
    m.drawn = say;
    m.restore = null;
    return;
  }
  if (job.count > 1 || !done) {
    if (job.count > 1) {
      for (const f of job.held) {
        const c = document.createElement("div");
        c.className = "md-chunk";
        c.appendChild(f);
        job.wrap.appendChild(c);
      }
      job.held = [];
    }
  } else {
    for (const f of job.held) job.wrap.appendChild(f);
    job.held = [];
  }
  if (done && job.swap) m.pane.replaceChildren(job.wrap);
  if (!job.swap && m.restore !== null && m.live) { // a pane off screen reads 0: leave the place to put back alone until it is shown
    if (m.restoreAt !== null && m.pane.scrollTop !== m.restoreAt) m.restore = null; // the user scrolled: their place wins
    else { m.pane.scrollTop = m.restore; m.restoreAt = m.pane.scrollTop; }
  }
  if (done) { m.drawn = job.wrap; m.restore = null; m.restoreAt = null; return; }
  m.job = setTimeout(() => editorMdPump(tab, gen, job), 0);
}

// editorMdSoon: the buffer changed. The preview follows 300 ms after the last change, never on each one.
function editorMdSoon(tab) {
  const m = tab.md;
  if (!m || !m.live) return;
  if (m.timer) clearTimeout(m.timer);
  m.timer = setTimeout(() => {
    m.timer = null;
    if (!tab.disposed && tab.model && m.live) editorMdRender(tab);
  }, EMD_DEBOUNCE_MS);
}

// editorMdStop lets go of what only a tab on screen in Preview holds: the pending redraw and the subscription to the buffer.
function editorMdStop(m) {
  if (m.timer) { clearTimeout(m.timer); m.timer = null; }
  if (m.sub) { m.sub.dispose(); m.sub = null; }
}

// editorMdShow puts the pane in the body showing `text` (drawn only if it is not what the pane already shows), at the place it was read.
function editorMdShow(tab, text) {
  const m = tab.md;
  if (!m.pane) {
    m.pane = document.createElement("div");
    m.pane.className = "dk-md";
    m.pane.tabIndex = 0; // the keyboard can be here: Mod+S saves through the tab's keydown, and the arrow keys scroll the document
    m.pane.setAttribute("role", "document");
    m.pane.setAttribute("aria-label", "Preview of " + tab.name);
  }
  if (!m.live) {
    tab.stashView(); // the editor, if this body has it, keeps its place for the way back
    editorMdRender(tab, text);
    tab.bodyEl.replaceChildren(m.pane);
    m.pane.scrollTop = m.top;
    if (m.job && m.top) { m.restore = m.top; m.restoreAt = m.pane.scrollTop; } // the document is still coming: keep putting it back
    m.live = true;
  } else {
    editorMdRender(tab, text);
  }
  tab.takeWantedFocus(m.pane);
}

// editorMdAttach puts Preview in the body when the tab is a markdown file whose view is Preview, and returns true; otherwise it
// returns false and the caller gives the body to the editor (Source, any other file). A tab that comes back from being hidden
// draws the buffer again only if it changed meanwhile, and is read at the place it was left.
function editorMdAttach(tab) {
  if (tab.disposed || !tab.model || tab.inner) return false;
  if (editorMdView(tab) !== "preview") { editorMdDrop(tab); return false; }
  editorMdShow(tab, tab.model.getValue());
  const m = tab.md;
  if (!m.sub) m.sub = tab.model.onDidChangeContent(() => editorMdSoon(tab));
  return true;
}

// editorMdEarly: a markdown tab on screen whose file is read (tab.earlyText) while Monaco is still loading. Preview shows the read
// text at once (it never needed the editor); Source says "loading…" until the editor is there. When the model is made from the same
// text, editorMdAttach finds it already drawn. Nothing follows the file before the model exists: the poll of the file on disk starts
// with the model (editorsync.js), and a change it finds then is drawn as any other.
function editorMdEarly(tab) {
  if (tab.disposed || tab.model || tab.inner || tab.earlyText === null || !tab.attached) return;
  if (editorMdView(tab) !== "preview" || tab.pendingChanges) { // Source, or Changes pressed (editorDiffSet): wait for the editor
    editorMdDrop(tab);
    tab.setBodyMessage("loading…");
    return;
  }
  editorMdShow(tab, tab.earlyText);
}

// editorMdPlace: where the live pane is read, to keep for the way back. While a first draw is still putting the pane back (m.restore),
// that place, unless the user has scrolled since (the pane is no longer where the draw last put it), in which case theirs.
function editorMdPlace(m) {
  if (m.restore !== null && !(m.restoreAt !== null && m.pane.scrollTop !== m.restoreAt)) return m.restore;
  return m.pane.scrollTop;
}

// editorMdDetach: the tab is not on screen. Its redraw and its subscription stop until it is shown again (a big file's draw under way
// finishes, a slice at a time, so the document is complete when the tab comes back); the pane stays with the place it was read at.
function editorMdDetach(tab) {
  const m = tab.md;
  if (!m || !m.live) return;
  m.top = editorMdPlace(m);
  m.live = false;
  editorMdStop(m);
}

// editorMdDrop: Preview is left for Source or Changes. Its redraw and its subscription go; the pane, the document drawn in it (and a
// big file's draw still under way) and the place it was read at are kept, so coming back with the same text shows it at once instead of
// drawing it again (editorMdRender skips a text it already drew).
function editorMdDrop(tab) {
  const m = tab.md;
  if (!m || !m.pane) return;
  if (m.live) m.top = editorMdPlace(m);
  editorMdStop(m);
  m.live = false;
}

// editorMdPick: a press on Preview or on Source (Code's place for a markdown file). Leaving Changes shows the view just chosen; a
// tab that is not on screen shows it when it is.
function editorMdPick(tab, view) {
  if (tab.disposed || tab.inner) return;
  const m = editorMdOf(tab);
  const asked = !tab.model && tab.pendingChanges; // Changes pressed while the editor loads (editorDiffSet): this press takes it back
  if (!tab.diff && !asked && m.view === view) return; // already there
  if (asked) tab.pendingChanges = false;
  m.view = view;
  if (tab.diff) editorDiffSet(tab, false);
  else if (tab.attached && tab.model) tab.show();
  else {
    editorMdEarly(tab); // read, and Monaco still loading
    tab.paint();
  }
}

// editorMdDispose: the tab is closed. Before its model goes: the pane holds a subscription to it. The pane and its document go too,
// and a draw under way stops.
function editorMdDispose(tab) {
  const m = tab.md;
  if (!m) return;
  editorMdDrop(tab);
  if (m.job) { clearTimeout(m.job); m.job = null; }
  m.gen++;
  m.pane = null;
  m.text = null;
  m.drawn = null;
}
