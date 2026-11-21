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
//   pane   the scroller that holds the rendered document while Preview is in the body, else null;
//   text   the buffer the pane shows (a render of the same text is skipped);
//   timer  the re-render that is 300 ms away, after an edit of the buffer;
//   sub    the subscription to the buffer's changes, held only while the tab is on screen in Preview;
//   top    where the pane was read, in px, kept for the way back (Source, Changes, another tab) and read from the pane while it is live;
//   live   the pane is on screen and being read.
//
// editorMdAttach(tab): the tab is on screen with its model and is not in Changes (EditorTab.show): Preview takes the body, or not.
// editorMdDetach(tab) / editorMdDrop(tab): the tab is hidden / Preview is left for another view or the tab is closed.

const EMD_DEBOUNCE_MS = 300; // the preview follows an edit of the buffer this long after its last change

function editorMdOf(tab) {
  if (!tab.md) tab.md = { view: "preview", pane: null, text: null, timer: null, sub: null, top: tab.lastScroll || 0, live: false };
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

// editorMdBody draws the buffer: the document, or a line saying why it could not be (the libraries did not load, the markup could not
// be made). Relative image paths are rewritten to the read route of the file's own directory, as the phone's viewer does.
function editorMdBody(tab, text) {
  const say = (msg) => {
    const d = document.createElement("div");
    d.className = "viewer-msg";
    d.textContent = msg;
    return d;
  };
  if (typeof marked === "undefined" || !marked.parse || typeof DOMPurify === "undefined") return say("The preview needs marked and DOMPurify, which did not load. Source shows the file.");
  const dir = tab.path.slice(0, tab.path.lastIndexOf("/") + 1);
  const wrap = document.createElement("div");
  wrap.className = "md-body";
  try {
    wrap.appendChild(mdRenderFragment(text, {
      marked,
      purify: DOMPurify,
      hljs: typeof hljs === "undefined" ? null : hljs,
      where: tab.path.slice(tab.path.lastIndexOf("/") + 1), // the link gate's dialog says which file the link is in
      rewrite: (rel) => (tab.box ? boxFileURL(tab.box, dir + rel) : "/api/fs/read?path=" + encodeURIComponent(dir + rel)),
    }));
  } catch (_) {
    return say("The preview could not be drawn. Source shows the file.");
  }
  return wrap;
}

// editorMdRender draws the buffer into the pane, unless the pane already shows this text. The pane is the scroller and stays, so a
// redraw keeps the place the document was read at (the browser only clamps it when the document got shorter).
function editorMdRender(tab) {
  const m = tab.md;
  const text = tab.model.getValue();
  if (m.text === text) return;
  m.text = text;
  m.pane.replaceChildren(editorMdBody(tab, text));
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

// editorMdAttach puts Preview in the body when the tab is a markdown file whose view is Preview, and returns true; otherwise it
// returns false and the caller gives the body to the editor (Source, any other file). A tab that comes back from being hidden
// draws the buffer again only if it changed meanwhile, and is read at the place it was left.
function editorMdAttach(tab) {
  if (tab.disposed || !tab.model || tab.inner) return false;
  if (editorMdView(tab) !== "preview") { editorMdDrop(tab); return false; }
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
    editorMdRender(tab);
    tab.bodyEl.replaceChildren(m.pane);
    m.pane.scrollTop = m.top;
    m.live = true;
  } else {
    editorMdRender(tab);
  }
  if (!m.sub) m.sub = tab.model.onDidChangeContent(() => editorMdSoon(tab));
  tab.takeWantedFocus(m.pane);
  return true;
}

// editorMdDetach: the tab is not on screen. Nothing runs for it until it is shown again; the pane stays with the place it was read at.
function editorMdDetach(tab) {
  const m = tab.md;
  if (!m || !m.live) return;
  m.top = m.pane.scrollTop;
  m.live = false;
  editorMdStop(m);
}

// editorMdDrop: Preview is left (Source, Changes) or the tab is closed. The pane goes; the place it was read at is kept.
function editorMdDrop(tab) {
  const m = tab.md;
  if (!m || !m.pane) return;
  if (m.live) m.top = m.pane.scrollTop;
  editorMdStop(m);
  m.pane = null;
  m.text = null;
  m.live = false;
}

// editorMdPick: a press on Preview or on Source (Code's place for a markdown file). Leaving Changes shows the view just chosen; a
// tab that is not on screen shows it when it is.
function editorMdPick(tab, view) {
  if (tab.disposed || tab.inner) return;
  const m = editorMdOf(tab);
  if (!tab.diff && m.view === view) return; // already there
  m.view = view;
  if (tab.diff) editorDiffSet(tab, false);
  else if (tab.attached && tab.model) tab.show();
  else tab.paint();
}

// editorMdDispose: the tab is closed. Before its model goes: the pane holds a subscription to it.
function editorMdDispose(tab) {
  editorMdDrop(tab);
}
