"use strict";

// The image comparer of the Changes view (spec "Review loop (P3)"): a changed image is shown as two pictures, not as text, in one of
// three modes the reader chooses (and viewerprefs.js keeps as imgDiffMode, for every image):
//   Side by side  the old and the new image next to each other, each with its width x height under it;
//   Swipe         the two stacked, the new one cut at a draggable divider (swipeClip, reviewmodel.js): the old one shows to its left;
//   Onion skin    the two stacked, the new one blended over the old by a slider (onionOpacity, reviewmodel.js).
// An added image (the base has none) has only its new side, a deleted one (a D row: gone from the working tree) only its old side, and
// a side that cannot be had says why in its place; none of those has a mode to choose, so the control is not shown.
//
// imageDiffMount(el, path, scope, opts) fills `el` and returns {key, dispose}. It is for ONE place that shows an image's change: the dock
// tab's Changes view (below) and, later, each image section of the All changes view. `scope` is "head" or "branch" (dockScope);
// opts = {key, deleted}: `key` is what the caller compares to know the picture is out of date (editorDiffKey's, with the row's code),
// `deleted` is true for a D row. The caller decides WHEN to mount: a mount asks the server at once, so none is made for a dock that is
// not on screen (editorSyncShown).
//
// The two sides.
//   new  today's file, as the viewer shows it: <img src="/api/fs/read?path="> (the server's raw image route, with nosniff and a
//        sandboxing CSP). A deleted file is not asked for.
//   old  POST /api/git/show {path, scope, raw: true} (gitapi.go handleGitShow): the blob's bytes with an image type, which becomes a
//        data: URL (FileReader.readAsDataURL) for an <img>; JSON instead for the other outcomes ({state: "ok", missing: true} for a file the base does not have,
//        notrepo, unavailable); 413 for a blob over 10 MiB. The request is aborted when the mount is disposed (or after IMG_FETCH_MS),
//        and an unfinished read is aborted: a mount that is gone leaves nothing behind.
// The old side's bytes are NEVER given a blob: URL. A blob: URL has the page's own origin and does not carry the response's
// `Content-Security-Policy: sandbox`, so opening one of an SVG in a tab would run it as the app. A data: URL has an opaque origin and
// browsers refuse a top-level navigation to it, for every image type alike.
// Both sides only ever go through an <img>: an SVG is never read as text or put into the page, so a script in one cannot run (an image
// loaded by <img> runs none).
//
// The tab's glue (the EditorTab of an image has no Monaco and so no Code | Changes toggle): an Image | Changes toggle in the dock's bar for
// an image the Changes section lists, the comparer in place of the viewer, and F7 / Shift+F7 stepping across the session's changed files
// as in a text file's Changes view (editorreview.js). Everything the comparer shows is kept while the tab is hidden (nothing is asked
// again when it comes back); it is made again when the base moves (a commit, the scope), and let go when the tab is closed.

const IMG_FETCH_MS = 20000; // the old image's request is given this long (editordiff.js DG_FETCH_MS: the same patience)
const IMG_MODES = [
  { id: "side", label: "Side by side", icon: [["path", { d: "M2 2.5h10v9H2zM7 2.5v9" }]] },
  { id: "swipe", label: "Swipe", icon: [["path", { d: "M2 2.5h10v9H2zM7 1.5v11M5.5 5.8L4.2 7l1.3 1.2M8.5 5.8L9.8 7 8.5 8.2" }]] },
  { id: "onion", label: "Onion skin", icon: [["path", { d: "M2 2.5h6.5V9H2zM5.5 5h6.5v6.5H5.5z" }]] },
];
const IMG_ICON_IMAGE = [["path", { d: "M2 2.5h10v9H2zM2.5 10l3-3.2 2.2 2.2 1.6-1.6 3 3" }]];
const IMG_KEY_STEP = 1;      // an arrow key moves the Swipe divider this many percent, with Shift
const IMG_KEY_STEP_BIG = 10; // this many

// imageDiffIs: whether `path` is an image the comparer shows (the same set the viewer and the tree's icons treat as one: copyfile.js).
function imageDiffIs(path) {
  return IMAGE_EXTS.has(extOf(path));
}

// imageDiffDims: the text under an image: "640 × 480", "" until it has loaded, and "no fixed size" for one the browser gives no
// natural size (an SVG with no width and height).
function imageDiffDims(s) {
  if (!s.loaded) return "";
  return s.w > 0 && s.h > 0 ? s.w + " × " + s.h : "no fixed size";
}

function imageDiffEl(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

// imageDiffMount: see the top. `opts.deleted` has no new side; an old side that is missing has no old side.
function imageDiffMount(el, path, scope, opts) {
  const o = opts || {};
  const name = path.split("/").pop() || path;
  const oldWord = scope === "branch" ? "merge base" : "HEAD";
  // A side: state "loading" | "ok" | "missing" (the base has none: added) | "none" (not asked for: deleted) | "error" (msg says why).
  const oldS = { state: "loading", msg: "", loaded: false, w: 0, h: 0 };
  const newS = { state: o.deleted ? "none" : "ok", msg: "", loaded: false, w: 0, h: 0 };
  let disposed = false;
  let ac = null;
  let timer = null;
  let reader = null; // the FileReader turning the old side's bytes into a data: URL, while it runs
  let swipeX = 50; // the divider, percent of the stage's width
  let blend = 50;  // the Onion skin slider, 0 (old) to 100 (new)

  const oldImg = imageDiffEl("img", "dk-imgd-img");
  oldImg.alt = name + " (old, in " + oldWord + ")";
  const newImg = imageDiffEl("img", "dk-imgd-img");
  newImg.alt = name + " (new, in the working tree)";
  const loaded = (s, img) => () => { s.loaded = true; s.w = img.naturalWidth || 0; s.h = img.naturalHeight || 0; paint(); };
  oldImg.onload = loaded(oldS, oldImg);
  newImg.onload = loaded(newS, newImg);
  oldImg.onerror = () => { oldS.state = "error"; oldS.msg = "the browser could not show it"; paint(); };
  newImg.onerror = () => { newS.state = "error"; newS.msg = "it is not in the working tree, or the browser could not show it"; paint(); };

  const root = imageDiffEl("div", "dk-imgd");
  const bar = imageDiffEl("div", "dk-imgd-bar");
  const seg = editorDiffSeg("Image comparison");
  seg.classList.toggle("dk-imgmode", true);
  const items = {};
  for (const m of IMG_MODES) {
    items[m.id] = editorDiffSegItem(m.label, m.icon, () => { viewerPrefs.setImgDiffMode(m.id); paint(); });
    seg.append(items[m.id]);
  }
  bar.append(seg);
  const stage = imageDiffEl("div", "dk-imgd-stage");
  root.append(bar, stage);
  el.replaceChildren(root);

  // The two sides can be compared on top of each other only while both are, or are about to be, pictures.
  const comparable = () => (oldS.state === "ok" || oldS.state === "loading") && (newS.state === "ok" || newS.state === "loading");

  function paint() {
    if (disposed) return;
    const cmp = comparable();
    bar.hidden = !cmp;
    const want = viewerPrefs.getImgDiffMode();
    for (const m of IMG_MODES) items[m.id].setAttribute("aria-pressed", m.id === want ? "true" : "false");
    // Swipe and Onion need the old picture to be there; until it is the sides are laid out one beside the other.
    const mode = cmp && oldS.state === "ok" ? want : "side";
    stage.dataset.mode = mode;
    newImg.style.opacity = "";
    if (mode === "swipe") stage.replaceChildren(...swipeNodes());
    else if (mode === "onion") stage.replaceChildren(...onionNodes());
    else stage.replaceChildren(...sideNodes());
  }

  // One side, as a figure: its label, the picture or what stands in its place, the size.
  function figure(label, s, img, why) {
    const f = imageDiffEl("div", "dk-imgd-fig");
    const frame = imageDiffEl("div", "dk-imgd-frame");
    if (s.state === "ok") frame.append(img);
    else frame.append(imageDiffEl("div", "dk-imgd-msg" + (s.state === "error" ? " err" : ""), s.state === "loading" ? "Loading the old version…" : why + (s.msg ? ": " + s.msg : "")));
    f.append(imageDiffEl("div", "dk-imgd-lab", label), frame, imageDiffEl("div", "dk-imgd-cap", imageDiffDims(s)));
    return f;
  }

  function sideNodes() {
    const figs = [];
    // An added image has no old side and a deleted one no new side: the one that is left is shown alone.
    if (oldS.state !== "missing") figs.push(figure("Old · " + oldWord, oldS, oldImg, "Could not load the old version"));
    if (newS.state !== "none") figs.push(figure("New · working tree", newS, newImg, "Could not load the new version"));
    if (!figs.length) return [imageDiffEl("div", "dk-imgd-msg", "There is no image on either side.")];
    const wrap = imageDiffEl("div", "dk-imgd-side" + (figs.length === 1 ? " one" : ""));
    wrap.append(...figs);
    return [wrap];
  }

  function stackCaption() {
    const t = (word, s) => word + " " + (imageDiffDims(s) || "…");
    return imageDiffEl("div", "dk-imgd-cap dk-imgd-stackcap", t("Old", oldS) + " · " + t("New", newS));
  }

  function swipeNodes() {
    const wrap = imageDiffEl("div", "dk-imgd-stack dk-imgd-swipe");
    const div = imageDiffEl("div", "dk-imgd-div");
    div.tabIndex = 0;
    div.setAttribute("role", "slider");
    div.setAttribute("aria-label", "Swipe divider: old image on the left, new image on the right");
    div.setAttribute("aria-orientation", "horizontal");
    div.setAttribute("aria-valuemin", "0");
    div.setAttribute("aria-valuemax", "100");
    div.append(imageDiffEl("span", "dk-imgd-knob"));
    // The new image is cut by a layer that spans the whole stage (the larger of the two pictures), not by its own box: the divider's
    // percent is of the stage, and the two pictures need not be the same size.
    const oldLayer = imageDiffEl("div", "dk-imgd-layer");
    const newLayer = imageDiffEl("div", "dk-imgd-layer");
    oldLayer.append(oldImg);
    newLayer.append(newImg);
    wrap.append(oldLayer, newLayer, div);
    const set = (p) => {
      swipeX = Math.min(100, Math.max(0, p));
      newLayer.style.clipPath = "inset(0 0 0 " + swipeX + "%)";
      div.style.left = swipeX + "%";
      const n = Math.round(swipeX);
      div.setAttribute("aria-valuenow", String(n));
      div.setAttribute("aria-valuetext", n + "% of the image is old, " + (100 - n) + "% new");
    };
    set(swipeX);
    // Pointer events: a mouse, a finger and a trackpad drag the same way. The stage holds the pointer while it is down, so a drag
    // that leaves the image goes on (and ends on the edge); touch-action lets the page still scroll up and down.
    let drag = false;
    const to = (e) => {
      const r = wrap.getBoundingClientRect();
      set(swipeClip(e.clientX - r.left, r.width));
    };
    wrap.onpointerdown = (e) => {
      if (e.button) return; // not the primary button
      drag = true;
      try { wrap.setPointerCapture(e.pointerId); } catch (_) { /* a pointer that is already gone */ }
      to(e);
      div.focus({ preventScroll: true });
      if (e.preventDefault) e.preventDefault();
    };
    wrap.onpointermove = (e) => { if (drag) to(e); };
    const end = (e) => {
      if (!drag) return;
      drag = false;
      try { wrap.releasePointerCapture(e.pointerId); } catch (_) { /* released already */ }
    };
    wrap.onpointerup = end;
    wrap.onpointercancel = end;
    div.onkeydown = (e) => {
      if (e.altKey || e.ctrlKey || e.metaKey) return;
      const big = e.shiftKey ? IMG_KEY_STEP_BIG : IMG_KEY_STEP;
      let p = null;
      if (e.key === "ArrowLeft" || e.key === "ArrowDown") p = swipeX - big;
      else if (e.key === "ArrowRight" || e.key === "ArrowUp") p = swipeX + big;
      else if (e.key === "PageDown") p = swipeX - IMG_KEY_STEP_BIG;
      else if (e.key === "PageUp") p = swipeX + IMG_KEY_STEP_BIG;
      else if (e.key === "Home") p = 0;
      else if (e.key === "End") p = 100;
      if (p === null) return;
      set(p);
      e.preventDefault();
    };
    return [wrap, stackCaption()];
  }

  function onionNodes() {
    const wrap = imageDiffEl("div", "dk-imgd-stack dk-imgd-onion");
    wrap.append(oldImg, newImg);
    const row = imageDiffEl("div", "dk-imgd-blend");
    const lo = imageDiffEl("span", "dk-imgd-blend-l", "Old");
    const hi = imageDiffEl("span", "dk-imgd-blend-r", "New");
    const range = imageDiffEl("input", "dk-imgd-range");
    range.type = "range";
    range.min = "0";
    range.max = "100";
    range.step = "1";
    range.value = String(blend);
    range.setAttribute("aria-label", "Onion skin: blend from the old image to the new one");
    const set = () => {
      blend = Number(range.value);
      newImg.style.opacity = String(onionOpacity(blend));
      range.setAttribute("aria-valuetext", Math.round(blend) + "% new");
    };
    set();
    range.oninput = set;
    row.append(lo, range, hi);
    return [wrap, row, stackCaption()];
  }

  // readDataUrl: the blob as a data: URL ("" when the read fails or is aborted). One read at a time; dispose aborts it.
  function readDataUrl(blob) {
    return new Promise((resolve) => {
      if (typeof FileReader !== "function") { resolve(""); return; }
      const r = new FileReader();
      reader = r;
      const done = (v) => { if (reader === r) reader = null; resolve(v); };
      r.onload = () => done(typeof r.result === "string" && r.result.startsWith("data:") ? r.result : "");
      r.onerror = r.onabort = () => done("");
      r.readAsDataURL(blob);
    });
  }

  async function loadOld() {
    ac = typeof AbortController === "function" ? new AbortController() : null;
    let timedOut = false;
    timer = setTimeout(() => { timedOut = true; if (ac) ac.abort(); }, IMG_FETCH_MS);
    const fail = (why) => { oldS.state = "error"; oldS.msg = why; paint(); };
    try {
      const res = await fetch("/api/git/show", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ path, scope, raw: true }),
        signal: ac ? ac.signal : undefined,
      });
      if (disposed) return;
      if (res.status === 413) return fail("it is over 10 MiB, too big to compare");
      if (!res.ok) return fail("git could not give it (HTTP " + res.status + ")");
      const type = (res.headers && res.headers.get("Content-Type")) || "";
      if (/^image\//i.test(type)) {
        const blob = await res.blob();
        if (disposed) return;
        const url = await readDataUrl(blob);
        if (disposed) return;
        if (!url) return fail("the picture could not be read");
        oldS.state = "ok";
        oldImg.src = url;
        paint();
        return;
      }
      // Anything else is the text form's JSON: the base has no such file, or git cannot say.
      const b = await res.json();
      if (disposed) return;
      if (b && b.state === "ok" && b.missing === true) { oldS.state = "missing"; paint(); return; }
      if (b && b.state === "notrepo") return fail("this file is not in a git repository");
      fail(b && typeof b.reason === "string" && b.reason ? b.reason : "git is not available here");
    } catch (_) {
      if (!disposed) fail(timedOut ? "git took too long" : "the request failed");
    } finally {
      clearTimeout(timer);
      timer = null;
    }
  }

  function dispose() {
    if (disposed) return;
    disposed = true;
    if (ac) ac.abort();
    if (timer) { clearTimeout(timer); timer = null; }
    if (reader) { try { reader.abort(); } catch (_) { /* done already */ } reader = null; }
    oldImg.onload = oldImg.onerror = newImg.onload = newImg.onerror = null;
    oldImg.removeAttribute("src");
    newImg.removeAttribute("src");
    if (root.parentNode === el) el.replaceChildren();
  }

  if (newS.state === "ok") newImg.src = "/api/fs/read?path=" + encodeURIComponent(path);
  paint();
  loadOld();
  return { key: typeof o.key === "string" ? o.key : "", dispose };
}

// --- the tab's glue ------------------------------------------------------------------------------------------------------------
// `tab.img` is {seg: {el, image, changes}, on, view}: `on` is whether the tab is in Changes (it may be before it is on screen: the
// view is made when it is), `view` the comparer's elements while it is up: {el, label, body, cmp, key}.

// editorImageOn: the tab is in Changes (an image's).
function editorImageOn(tab) {
  return !!tab && !tab.disposed && !!tab.img && tab.img.on;
}

// editorImageToggle builds the Image | Changes toggle for the dock's bar. paint (editorImagePaint) shows it for an image the Changes
// section lists, and for one that is in Changes.
function editorImageToggle(tab) {
  const el = editorDiffSeg("View");
  const image = editorDiffSegItem("Image", IMG_ICON_IMAGE, () => { editorImageSet(tab, false); tab.focusReader(); });
  const changes = editorDiffSegItem("Changes", DG_ICON_CHANGES, () => { editorImageSet(tab, true); });
  el.append(image, changes);
  el.hidden = true;
  tab.img = { seg: { el, image, changes }, on: false, view: null };
  return el;
}

// editorImagePaint: the toggle is shown for an image tab with git behind it (no box file) whose file the Changes section lists, or that
// is in Changes already (leaving it must stay possible).
function editorImagePaint(tab) {
  const s = tab.img;
  if (!s) return;
  const eligible = tab.mode === "image" && !tab.box && !tab.disposed;
  const show = eligible && (s.on || !!editorReviewRow(tab));
  s.seg.el.hidden = !show;
  s.seg.image.setAttribute("aria-pressed", s.on ? "false" : "true");
  s.seg.changes.setAttribute("aria-pressed", s.on ? "true" : "false");
  const title = "Changes " + editorDiffLabel(typeof dockScope === "function" ? dockScope(tab.host && tab.host.sessionId ? tab.host.sessionId() : null) : "head", "");
  if (s.seg.changes.title !== title) s.seg.changes.title = title;
}

// editorImageSet(tab, on): the Changes view of an image tab, or back to the image. Returns whether it is up or on its way.
function editorImageSet(tab, on) {
  const s = tab.img;
  if (!s || tab.disposed || tab.mode !== "image") return false;
  if (tab.box) {
    if (on) showToast("Changes unavailable: " + DG_BOX_WHY);
    return false;
  }
  if (!on) { editorImageLeave(tab); return false; }
  s.on = true;
  if (tab.attached) editorImageEnter(tab);
  tab.paint();
  editorReviewChanged(); // the Changes section says "reviewed n of m" while a tab is in Changes
  return true;
}

// editorImageEnter puts the comparer's frame in the tab in place of the viewer: a header row (the file, what is compared) over the body
// the comparer fills. Only for a tab on screen; the pictures are asked for by editorImageSync, when the dock is shown and git's status is in.
function editorImageEnter(tab) {
  const s = tab.img;
  if (!s || !s.on || tab.disposed || !tab.inner || !tab.attached) return;
  if (!s.view) {
    const head = imageDiffEl("div", "dk-dhead");
    const name = imageDiffEl("span", "dk-dh-name", tab.name);
    name.title = tab.path;
    const label = imageDiffEl("span", "dk-dh-cmp");
    head.append(name, label);
    const body = imageDiffEl("div", "dk-imgd-body");
    const el = imageDiffEl("div", "dk-ed-cmp dk-imgd-tab");
    el.tabIndex = -1;
    el.append(head, body);
    s.view = { el, label, body, cmp: null };
    tab.el.replaceChildren(el);
    // Where F7 across files landed this tab and asked for the keyboard (editorreview.js): the comparer takes it, once.
    if (editorReviewWantsFocus(tab)) { editorDiffOf(tab).land = null; el.focus({ preventScroll: true }); }
  }
  editorImageSync();
}

// editorImageSync: the tab on screen, if it is an image in Changes, shows the comparer for the key it has now: the header's label, and
// the pictures, made again when the key (the path, the scope, git's HEAD, the merge base) or the row's D has moved. Nothing is asked of
// a dock that is not on screen, or before git's status is known (the key would only move again). Called from every place that can move
// those: the status and the rows landing (editorDiffSync, editorReviewRefresh), a tab coming on screen.
function editorImageSync() {
  const tab = typeof dockAttached === "function" ? dockAttached() : null;
  if (!tab || !tab.img || tab.disposed) return;
  editorImagePaint(tab);
  const v = tab.img.view;
  if (!tab.img.on || !v || !tab.attached) return;
  const k = editorDiffKey(tab);
  editorDiffPaintLabel(v.label, k.scope, k.baseName);
  if (!k.known || !editorSyncShown()) {
    if (!v.cmp && !v.body.firstChild) v.body.append(imageDiffEl("div", "dk-imgd-msg", "Waiting for git…"));
    return;
  }
  const row = editorReviewRow(tab);
  const deleted = !!row && row.code === "D";
  const key = k.key + (deleted ? "\nD" : "");
  if (v.cmp && v.cmp.key === key) return;
  if (v.cmp) v.cmp.dispose();
  v.body.replaceChildren();
  v.cmp = imageDiffMount(v.body, tab.path, k.scope, { key, deleted });
}

// editorImageLeave goes back to the image: the comparer is let go (its request, its read) and the viewer is in the tab again.
function editorImageLeave(tab) {
  const s = tab.img;
  if (!s || !s.on) return;
  s.on = false;
  editorImageDrop(tab);
  if (tab.inner) tab.el.replaceChildren(tab.inner.el);
  tab.paint();
  editorReviewChanged();
}

// editorImageDrop lets go of the comparer's elements and what it holds.
function editorImageDrop(tab) {
  const v = tab.img && tab.img.view;
  if (!v) return;
  tab.img.view = null;
  if (v.cmp) v.cmp.dispose();
  v.el.remove();
}

// editorImageDispose: the tab is closed (before its viewer is).
function editorImageDispose(tab) {
  if (!tab.img) return;
  tab.img.on = false;
  editorImageDrop(tab);
}

// editorImageStep: F7 (dir 1) / Shift+F7 (dir -1) in an image's Changes view: an image has no changes inside it to step through, so
// it is the next changed file of the session, as past the last change of a text file (editorreview.js reviewOpenNext).
function editorImageStep(tab, dir) {
  const sid = editorReviewSid(tab);
  Promise.resolve().then(() => { if (!tab.disposed) reviewOpenNext(sid, tab.path, dir); });
}
