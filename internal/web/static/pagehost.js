// pagehost.js: on desktop (> 700px) Settings, Analytics and Audit open as pages
// in the pane area instead of as modals (S4).
//
// A page is the SAME <dialog> element, re-parented: openPage moves it into
// #page-host (a flex sibling of #grid inside #layout), shows it non-modally
// with class `as-page`, and body.page-open hides #grid and #drawer. Closing it
// puts it back where it was, so every id, every `#settings-modal …` selector
// and every logic module keeps working, and mobile, which never takes this
// path, still gets the modal.
//
// The open page is deliberately not persisted: a reload shows the panes.

const PAGE_IDS = ["settings-modal", "analytics-modal", "audit-page"];

// The sidebar footer link that lights up while its page is open.
const PAGE_LINKS = { "audit-page": "side-audit", "analytics-modal": "side-analytics", "settings-modal": "side-settings" };

// What a page focuses when it opens: its selected tab, any tab, else a heading
// (the first one that is on screen: openPage skips what a hidden head holds).
// Tried in this order (a single selector list would return the first match in
// document order, and a page's heading may precede its tabs).
const PAGE_FOCUS = ['[role="tab"][aria-selected="true"]', '[role="tab"]', ".dlg-tabs button", "h1, h2, h3", '[role="heading"]'];

// pageModeFor: does `id` open as a page at viewport `width`? Pure.
function pageModeFor(id, width) {
  if (!PAGE_IDS.includes(id)) return false;
  return typeof mobileActive === "function" && !mobileActive(width);
}

// The open page: { dlg, mark } (mark = the comment holding its home position).
let pageNow = null;

function currentPage() {
  return pageNow ? pageNow.dlg.id : "";
}

function paintPageLinks(id) {
  for (const [pid, linkId] of Object.entries(PAGE_LINKS)) {
    const b = document.getElementById(linkId);
    if (!b) continue;
    const on = pid === id;
    b.classList.toggle("on", on);
    if (on) b.setAttribute("aria-current", "page");
    else b.removeAttribute("aria-current");
  }
}

// openPage: take dialog `id` as a page. false = not a page here (mobile, not a
// page id, missing, or already open as a modal), and the caller opens it
// modally as before.
function openPage(id) {
  if (!pageModeFor(id, window.innerWidth)) return false;
  const dlg = document.getElementById(id);
  const host = document.getElementById("page-host");
  if (!dlg || !host) return false;
  if (pageNow && pageNow.dlg === dlg) return true;
  // Opened modally on a phone, then widened: it stays a modal (Review Focus 2).
  if (dlg.open) return false;
  // Another page is open: it may hold something its leave hook wants to ask about. The question is on screen then, and
  // this page opens when it is answered (Discard); true says "handled", so the caller does not open it as a modal.
  if (closePageHolds(() => openPage(id))) return true;
  const mark = document.createComment("page-home:" + id);
  dlg.before(mark);
  host.appendChild(dlg);
  pageNow = { dlg, mark };
  dlg.classList.add("as-page");
  document.body.classList.add("page-open", "page-" + id);
  host.hidden = false;
  dlg.show();
  paintPageLinks(id);
  // The first match that is on screen: a page may hold the same kind of element in a head it hides (Analytics' .dlg-tabs
  // buttons and legend h2 sit in the phone's .dlg-head), and focus() on a node with no boxes does nothing.
  for (const sel of PAGE_FOCUS) {
    const f = Array.from(dlg.querySelectorAll(sel)).find((n) => n.getClientRects().length > 0);
    if (!f) continue;
    if (!f.matches("button, [tabindex]")) f.setAttribute("tabindex", "-1");
    f.focus({ preventScroll: true });
    break;
  }
  return true;
}

// pageLeaveHook: set by the page that has something to ask before it goes (profilepage.js: unsaved block
// changes). closePage asks it with why: "esc" from the Esc key, "force" when the window crossed to a phone (told, not
// asked), nothing from its other callers. It answers true (may go), false (stay) or a promise of either, and answers
// true for a page that is not its own.
// Unset, a page closes as it always did.
let pageLeaveHook = null;

// closePage: close the open page, if any, unless its leave hook says to stay. Resolves like the hook:
//   true     the page is closed (or none was open); the restore ran right here, before this returned
//   false    it stays
//   promise  the hook is asking the user; the page closes when it answers true
// The restore runs right here rather than waiting for the dialog's `close` event (a queued task), so a caller that
// goes on to open a session lays it out against a visible #grid; the event's own pageClosed then finds nothing to
// do. That is why the answer is not a promise unless the hook asked something: with nothing to ask the page is
// shut by the time this returns. A caller that acts on the panes must read the answer (closePageHolds below does):
// going on after "stay" would act behind a page the user chose to keep. why "force" closes whatever the hook would
// say: the layout is leaving under the page (the window crossed to a phone). The hook is told, so it can end what it
// holds (the profile page ends its edit, releases its pickers and keeps what was typed for when that profile opens
// again), and is never asked.
function closePage(why) {
  if (!pageNow) return true;
  const dlg = pageNow.dlg;
  let verdict = true;
  if (typeof pageLeaveHook === "function") {
    if (why === "force") {
      try { pageLeaveHook(why); } catch (_) { /* a hook that fails must not keep a page up on a phone */ }
    } else {
      verdict = pageLeaveHook(why);
    }
  }
  if (verdict === true) { closePageNow(dlg); return true; }
  if (verdict === false) return false;
  return Promise.resolve(verdict).then((ok) => {
    if (ok === false) return false;
    // It may have gone while the question was open (another route closed it).
    if (pageNow && pageNow.dlg === dlg) closePageNow(dlg);
    return true;
  });
}

function closePageNow(dlg) {
  if (dlg.open) dlg.close();
  pageClosed(dlg);
}

// closePageHolds: the way for a caller that needs the panes on screen (it shows a session, opens a drawer, changes the
// layout) to close the page. false means go on now, in this same tick: no page was open (always so on a phone), or it
// had nothing to ask and is shut. true means the page is asking whether to drop unsaved changes, or has been told to
// stay: the caller stops, and `again` runs only if the answer is Discard, after the page is shut. Keep editing runs
// nothing, so nothing happens behind a page the user kept.
function closePageHolds(again) {
  const left = closePage();
  if (left === true) return false;
  if (left !== false) left.then((ok) => { if (ok) again(); });
  return true;
}

// pageClosed: every dialog's `close` handler (shell.js initDialogs), so ✕, Esc
// and a programmatic closeDialog("settings-modal") all restore the same way.
// A no-op for anything but the open page.
function pageClosed(dlg) {
  if (!pageNow || pageNow.dlg !== dlg) return;
  const { mark } = pageNow;
  pageNow = null;
  if (mark.parentNode) mark.replaceWith(dlg);
  else document.body.appendChild(dlg);
  dlg.classList.remove("as-page");
  document.body.classList.remove("page-open", "page-" + dlg.id);
  const host = document.getElementById("page-host");
  if (host) host.hidden = true;
  paintPageLinks("");
  // The grid was display:none: terminals skipped their fits, so refit now it
  // has a size again, and re-hang an open drawer under the focused pane.
  requestAnimationFrame(() => {
    if (typeof fitAll === "function") fitAll();
    if (typeof positionDrawer === "function") positionDrawer();
  });
}

// Esc closes the page. Bubble phase, and only when nothing above it took the
// key: a modal over the page (bug report, confirm, palette, spawn) closes
// itself, and a field that handles its own Esc calls preventDefault. The page's
// leave hook is asked with why "esc", so a layer inside the page (an open picker)
// can take the key before the page does.
if (typeof document !== "undefined") {
  document.addEventListener("keydown", (ev) => {
    if (ev.key !== "Escape" || ev.defaultPrevented || !currentPage()) return;
    if (typeof dialogsOpen === "function" && dialogsOpen() > 0) return;
    ev.preventDefault();
    closePage("esc");
  });
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { PAGE_IDS, pageModeFor };
}
