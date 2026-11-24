"use strict";

// The page head of every Settings page but Profiles (UI plan 4, #174; frame M2, design digest §B1.4 and §A6-A9):
// an H1, one line of description, the page's one action and "Esc ✕". Profiles' head is the profile page's own
// (profilepage.js), which borrows settingsEscButton from here.
//
// settingsHeadSpec is pure: the copy and the action of each page, so a test can pin every word. paintSettingsHead
// is the DOM half: it fills the empty `.set-head` slot each panel carries in index.html, on the desktop page only.
// A phone's modal never sees a head: the slot is hidden by app.css and nothing paints it there.

// accountsLead: the Agent accounts description is the first sentence of the paragraph index.html carries for the
// phone (founder, 2026-11-08), and the other sentences stay on the page as a note (accounts.js acctDeskNote). A
// dot inside a word ("~/.claude") is not the end of a sentence: only one followed by a space (or the end) is.
function accountsLead(text) {
  const t = String(text == null ? "" : text).replace(/\s+/g, " ").trim();
  const m = /^(.*?[.!?])(?:\s+(.*))?$/.exec(t);
  if (!m) return { lead: t, rest: "" };
  return { lead: m[1], rest: m[2] || "" };
}

// settingsHeadSpec(tab, info): what page `tab` says about itself, or null for a tab with no head here (Profiles).
// info carries the two descriptions only the page knows: `version` (About: the live version line from /api/meta)
// and `accountsLead` (Agent accounts). `mono` names parts of the description set in mono (machine text). Every
// action is a secondary button; `menu: true` marks the one that opens a menu (Credentials' "Add credential ▾").
function settingsHeadSpec(tab, info) {
  const i = info || {};
  switch (tab) {
    case "creds":
      return {
        title: "Credentials",
        desc: "Cloud accounts and tokens a boxed profile can use. Profiles pick from this list.",
        action: { id: "set-act-creds", label: "Add credential", menu: true },
      };
    case "tokens":
      return {
        title: "Device tokens",
        desc: "Let a client app or CLI connect with Authorization: Bearer … and no password. One per device.",
        mono: ["Authorization: Bearer …"],
        action: { id: "set-act-tokens", label: "New device token" },
      };
    case "vmfolders":
      return {
        title: "VM folders",
        desc: "Folders on this Mac the box VM can see. If a box broke out of its container, these are all it could reach.",
        action: { id: "set-act-vmfolders", label: "Add folder" },
      };
    case "wsroots":
      return {
        title: "Workspace roots",
        desc: "Workspace roots are the folders CuliBox's Files, git and search can open. Home is always one. Starting a session in a folder outside them adds that folder here. Removing one does not stop sessions running in it.",
        action: { id: "set-act-wsroots", label: "Add folder" },
      };
    case "linktrust":
      return {
        title: "Trusted hosts",
        desc: "Links an agent shows open without the confirm dialog when they go to one of these hosts.",
      };
    case "settings":
      return {
        title: "Appearance",
        desc: "Text sizes and alerts for this browser. Sizes apply as you change them.",
        action: { id: "set-act-settings", label: "Reset to defaults" },
      };
    case "accounts":
      return { title: "Agent accounts", desc: i.accountsLead || "", action: { id: "set-act-accounts", label: "Add account" } };
    case "about":
      return { title: "About", desc: i.version || "", action: { id: "set-act-about", label: "Report a problem" } };
    default:
      return null;
  }
}

// What each header action does: the handler its old footer button had (+ new account, + new device token, Reset to
// defaults, Report a problem). Reset clicks the footer's own button, which is hidden on the page but still wired
// (app.js initSettings), so there is one reset and not a copy of it. set-act-creds is not here: Task 5's menu
// code takes that button over (credAddMenuAttach).
const SETTINGS_HEAD_RUN = {
  "set-act-accounts": () => acctNew(),
  "set-act-tokens": () => createToken(),
  "set-act-vmfolders": () => openVMFoldersAdd("", "rw", ""),
  "set-act-wsroots": () => openWsRootsAdd(""),
  "set-act-settings": () => { const b = document.getElementById("fs-reset"); if (b) b.click(); },
  "set-act-about": () => openBugReport(),
};

// settingsEscButton: "Esc" and the close glyph, the kit's small ghost button (frame A9). It is not a [data-close] button: the page
// hides every one of those (Done is modal chrome), so this one closes through closeDialogFromUI itself, which asks
// the open page's leave hook first (unsaved profile changes) exactly as the old ✕ did.
function settingsEscButton() {
  const b = document.createElement("button");
  b.type = "button";
  b.className = "btn btn-ghost btn-sm set-esc";
  b.setAttribute("aria-label", "Close (Esc)");
  b.setAttribute("title", "Close (Esc)");
  const key = document.createElement("span");
  key.className = "kbd-hint";
  key.textContent = "Esc";
  b.append(key, uiIcon(ICON_ACTION.close, { size: 12 })); // the shared close glyph at chip size (#168), not the ✕ text
  b.onclick = () => closeDialogFromUI("settings-modal");
  return b;
}

// The Agent accounts paragraph, as index.html carries it: the first .hint of the panel. The desktop note is built
// after it, so it stays the first.
function settingsAccountsHint() {
  const panel = document.getElementById("accounts");
  return panel ? panel.querySelector(".hint") : null;
}

// settingsHeadDesc: the description line, with each `mono` part in its own span (the frame's `mono t-sm`).
function settingsHeadDesc(spec) {
  const p = document.createElement("p");
  p.className = "page-desc";
  let rest = spec.desc;
  for (const m of spec.mono || []) {
    const at = rest.indexOf(m);
    if (at < 0) continue;
    if (at > 0) p.appendChild(document.createTextNode(rest.slice(0, at)));
    const code = document.createElement("span");
    code.className = "mono t-sm";
    code.textContent = m;
    p.appendChild(code);
    rest = rest.slice(at + m.length);
  }
  if (rest) p.appendChild(document.createTextNode(rest));
  return p;
}

// settingsHeadAction: the page's one action, a plain secondary .btn (never a primary: the pages draw none). A
// menu action wears the caret, muted and hidden from the accessible name, and aria-haspopup="menu".
function settingsHeadAction(action) {
  const b = document.createElement("button");
  b.type = "button";
  b.id = action.id;
  b.className = "btn";
  b.appendChild(document.createTextNode(action.label));
  if (action.menu) {
    const caret = document.createElement("span");
    caret.className = "muted";
    caret.setAttribute("aria-hidden", "true");
    caret.textContent = "▾";
    b.appendChild(caret);
    b.setAttribute("aria-haspopup", "menu");
    b.setAttribute("aria-expanded", "false");
  } else if (SETTINGS_HEAD_RUN[action.id]) {
    b.onclick = SETTINGS_HEAD_RUN[action.id];
  }
  return b;
}

// paintSettingsHead(tab): draw page `tab`'s head into its slot. A no-op unless Settings is the desktop page
// (#settings-modal.as-page): on a phone the modal keeps its Done, + new device token and + new account footers.
// selectSettingsTab runs before the dialog becomes the page on the first open, so openSettings paints once more
// afterwards, and loadAboutInfo again once the version has arrived; every call replaces the head.
function paintSettingsHead(tab) {
  const dlg = document.getElementById("settings-modal");
  if (!dlg || !dlg.classList.contains("as-page")) return;
  const panel = document.getElementById(tab);
  const slot = panel ? panel.querySelector(".set-head") : null;
  if (!slot) return;
  const about = document.getElementById("about-version");
  const info = { version: about ? about.textContent.trim() : "", accountsLead: "" };
  let rest = "";
  if (tab === "accounts") {
    const hint = settingsAccountsHint();
    const sentences = accountsLead(hint ? hint.textContent : "");
    info.accountsLead = sentences.lead;
    rest = sentences.rest;
  }
  const spec = settingsHeadSpec(tab, info);
  // The repaint replaces every control of the head, so a focused one (About repaints when /api/meta answers) would drop focus
  // to <body>: note which one it was, and put it back on its equal once the new head is in.
  const refocus = settingsHeadFocusKey(slot);
  if (!spec) { slot.replaceChildren(); return; }
  const text = document.createElement("div");
  text.className = "page-head-text";
  const title = document.createElement("h1");
  title.className = "page-title";
  title.textContent = spec.title;
  // The H1 is one line that ellipsizes in a narrow window (desktop.css); the full text is one hover away.
  title.setAttribute("title", spec.title);
  text.append(title, settingsHeadDesc(spec));
  const actions = document.createElement("div");
  actions.className = "page-actions";
  const trigger = spec.action ? settingsHeadAction(spec.action) : null;
  if (trigger) {
    const sep = document.createElement("span");
    sep.className = "set-sep";
    sep.setAttribute("aria-hidden", "true");
    actions.append(trigger, sep);
  }
  actions.appendChild(settingsEscButton());
  slot.replaceChildren(text, actions);
  if (tab === "accounts" && typeof acctDeskNote === "function") acctDeskNote(rest);
  // Credentials' menu (a later task) takes the trigger over once it is on the page. It is reached as a property of
  // the global object, not by name: a bare `credAddMenuAttach(...)` is a no-undef error for scripts/lint.sh until
  // the menu code exists, and a disable comment would turn into an unused-directive warning once it does. So that
  // function must be a top-level function declaration (which is what makes it a property of the global).
  if (spec.action && spec.action.menu && typeof globalThis.credAddMenuAttach === "function") globalThis.credAddMenuAttach(trigger);
  if (tab === "vmfolders" && typeof vmfSyncHead === "function") vmfSyncHead();
  if (tab === "wsroots" && typeof wsrSync === "function") wsrSync();
  if (refocus) refocus();
}

// settingsHeadFocusKey(slot): when keyboard focus is on a control inside the head `slot`, a function that puts focus on the
// control that replaces it after a repaint: found by id (the action) or, for the id-less Esc ✕, by its class. null when focus is
// elsewhere (or nowhere), so a repaint never takes focus from the rest of the page.
function settingsHeadFocusKey(slot) {
  const a = document.activeElement;
  if (!a || a === slot || typeof slot.contains !== "function" || !slot.contains(a)) return null;
  const id = a.id || "";
  const esc = !id && a.classList && a.classList.contains("set-esc");
  if (!id && !esc) return null;
  return () => {
    const next = id ? document.getElementById(id) : slot.querySelector(".set-esc");
    if (next && typeof next.focus === "function") next.focus();
  };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { accountsLead, settingsHeadSpec };
}
