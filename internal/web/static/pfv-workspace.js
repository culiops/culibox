"use strict";

// The Workspace block's editor (spec 5.5): the folder (the shared folder picker, wspicker.js), how the
// agent works on it ("Work in this folder" or "Work on a private copy") and, for a private copy, where
// the copy is kept, which files of the repo it shares read-only and whether finished work comes home by
// itself. What a save writes is profileedit.js's (pfvMergeBlock "workspace", through wmFieldsToSend);
// this file draws the form, keeps what the user has typed and says what the client refuses before the
// PUT. It registers itself with profilepage.js (pfvRegisterEditor), so it loads after it.
//
// Every top-level name here starts with pfw/PFW_ (the page scripts share one scope). Classic script:
// profilepage.js's helpers (pfvMk, pfvReg, pfvCard, pfvChanged, pfvPaint, ...), workmode.js's rules (wm*),
// profileform.js's contextPathsToText and the factory createWsPicker are globals read at call time.
//
// The picker belongs to the form's nodes: each draw of the form builds a new one on the new input and the
// framework releases the old one (rec.disposers) when the form is drawn again, closed or dropped. So a
// change that does not alter what the form shows (a pick, typing a path, the copy's folder) never draws it
// again: that would replace the input the picker is bound to, and the dropdown with it.

const PFW_HINT_BOX = "A box cannot use your whole home or system folders.";
const PFW_HINT_HOST = "Where sessions start. A private copy needs a box.";
const PFW_HINT_SHARE = "Relative to the folder, one per line, at most 8.";

// What the copy fields hold while the block is on "Work in this folder": the profile's own, as opened, so
// "a private copy and back" is no change (see pfwPickMode).
const pfwCopyFields = (v) => ({ copyDir: v.copyDir, shareText: v.shareText, autoReturn: v.autoReturn });

const pfwHome = () => (typeof window !== "undefined" && window.HOME_DIR) || "";
const pfwUnavailable = () => (typeof cloneUnavailable === "string" ? cloneUnavailable : "");

// pfwValues: the controls' starting values from the stored profile (the contract at the top of
// profileedit.js). A private copy's folder is clone_of and its own folder is default_cwd; for a profile
// that works in its folder, default_cwd is the folder.
function pfwValues(c) {
  const d = c.doc;
  const copy = wmModeOf(d) === "copy";
  const cwd = typeof d.default_cwd === "string" ? d.default_cwd : "";
  return {
    mode: copy ? "copy" : "folder",
    folder: copy ? d.clone_of : cwd,
    copyDir: copy ? cwd : "",
    shareText: contextPathsToText(d.clone_share),
    autoReturn: d.clone_no_auto_return !== true,
  };
}

// pfwSuggestion: where a copy of `folder` could live: ~/box-clones/<name>, never a folder another profile
// already writes (two profiles on one clone would share a .git).
function pfwSuggestion(folder) {
  const me = pfvState.doc ? pfvState.doc.name : "";
  const rows = typeof profiles !== "undefined" && Array.isArray(profiles) ? profiles : [];
  const taken = rows.filter((p) => p.name !== me).map((p) => p.default_cwd).filter(Boolean);
  return wmCopyDirSuggestion(folder, pfwHome(), taken);
}

// pfwFollow: a private copy whose own folder is still the one suggested (or empty) follows the folder: it is
// written for the user, so it is theirs to keep only once they have typed in it. A stored copy's folder is
// never replaced.
function pfwFollow(rec) {
  const v = rec.values;
  const ui = rec.ui;
  if (v.mode !== "copy" || (v.copyDir !== "" && v.copyDir !== ui.autoDir)) return;
  const dir = pfwSuggestion(v.folder);
  if (!dir) return;
  v.copyDir = dir;
  ui.autoDir = dir;
  if (ui.copyInput) ui.copyInput.value = dir;
}

// pfwPickMode: Work in this folder / Work on a private copy. The copy fields are kept for the way back,
// and the values go back to what they were opened with meanwhile, so a copy and back is no change. The
// refusals were about the other way's fields.
function pfwPickMode(rec, mode) {
  const v = rec.values;
  const ui = rec.ui;
  if (v.mode === mode) return;
  if (mode === "folder") {
    ui.copyStash = pfwCopyFields(v);
    Object.assign(v, ui.copyInit);
  } else {
    Object.assign(v, ui.copyStash || ui.copyInit);
  }
  v.mode = mode;
  pfwFollow(rec);
  rec.ferrs = [];
  pfvChanged(rec, "");
}

// pfwFolderChanged: the picker's value changed (a pick, a resolved or typed path, an emptied field).
function pfwFolderChanged(rec, path) {
  rec.values.folder = path;
  rec.ui.typed = undefined;
  pfwFollow(rec);
  rec.ui.clearFolder();
  pfvPaint();
}

// pfwBox: a label, the control and one line under it: the hint, or in its place the refusal for errKey.
// clear() takes the refusal away at once, without drawing the form again.
function pfwBox(rec, errKey, label, control, hint) {
  const w = pfvMk("div", "pfv-field");
  const lab = pfvMk("label", "pfv-flabel", label);
  lab.setAttribute("for", control.id);
  const below = pfvMk("p", "pfv-hint");
  w.append(lab, control, below);
  const paint = () => {
    const err = pfvFerr(rec, errKey);
    below.className = err ? "pfv-ferr" : "pfv-hint";
    below.textContent = err || hint;
    control.classList.toggle("pfv-bad", err !== "");
  };
  paint();
  const clear = () => {
    rec.ferrs = rec.ferrs.filter((e) => !pfvFieldIs(e.field, errKey));
    rec.nudge = false;
    paint();
  };
  return { w, clear };
}

// pfwText: a mono text control (a textarea when `area`), with the browser's helpers off.
function pfwText(area, id) {
  const e = pfvMk(area ? "textarea" : "input", "pfv-ctl pfv-ctl-mono" + (area ? " pfv-area" : ""));
  e.id = id;
  e.setAttribute("autocomplete", "off");
  e.setAttribute("spellcheck", "false");
  return e;
}

// pfwFolderField: the folder and its picker. The error key is the folder's own: clone_of when the
// folder is the one to copy, default_cwd when the agent works in it.
function pfwFolderField(rec, c, copy) {
  const v = rec.values;
  const ui = rec.ui;
  const input = pfwText(false, "pfv-f" + ++pfvUid);
  input.type = "text";
  input.setAttribute("role", "combobox");
  input.setAttribute("aria-expanded", "false");
  input.setAttribute("aria-autocomplete", "list");
  const list = pfvMk("div", "pfv-wslist");
  list.id = input.id + "-list";
  list.setAttribute("role", "listbox");
  list.setAttribute("aria-label", "Directories");
  list.hidden = true;
  input.setAttribute("aria-controls", list.id);
  const note = pfvMk("p", "pfv-wsnote");
  note.hidden = true;
  const picker = createWsPicker({
    input, list,
    // An Esc that closes the list is not the page's to act on.
    keepEsc: true,
    // Opening the block puts focus here; the dropdown is for the user to ask for.
    openOnFocus: () => !ui.quiet,
    onPick: (path) => pfwFolderChanged(rec, path),
    // The steady-state caption ("N project roots...") is for a dialog with room to spare; a problem is not.
    onNote: (text, idle) => { note.textContent = idle ? "" : text; note.hidden = idle || !text; },
  });
  rec.disposers.push(() => picker.destroy());
  picker.set(v.folder);
  // Text left over from a Save that refused it is still there to fix.
  if (ui.typed !== undefined) input.value = ui.typed;
  pfvReg(rec, "folder", input);
  const box = pfwBox(rec, copy ? "clone_of" : "default_cwd", copy ? "Folder to copy" : "Folder", input, c.tier === "host" ? PFW_HINT_HOST : PFW_HINT_BOX);
  input.addEventListener("input", () => { ui.typed = undefined; box.clear(); pfvPaint(); });
  box.w.append(note, list);
  ui.folderInput = input;
  ui.clearFolder = () => box.clear();
  return box.w;
}

// pfwModeField: the two ways of working on the folder, as cards.
function pfwModeField(rec, copy) {
  const card = (key, mode, title, text) => pfvCard(rec, key, {
    group: "pfv_mode", checked: copy === (mode === "copy"), disabled: false, title, chip: "", pick: () => pfwPickMode(rec, mode),
    lines: [pfvMk("span", "pfv-card-d", text)],
  });
  const fs = pfvMk("fieldset", "pfv-field pfv-choice");
  fs.append(
    pfvMk("legend", "pfv-flabel", "How it works on the folder"),
    card("mode-folder", "folder", "Work in this folder", "It changes your files, as you would."),
    card("mode-copy", "copy", "Work on a private copy", "It gets its own git copy. Your folder is untouched until work comes home."),
  );
  return fs;
}

// pfwCopyFieldNodes: where the copy is kept, the files it shares read-only, and bring-work-home.
function pfwCopyFieldNodes(rec, storedCopy) {
  const v = rec.values;
  const ui = rec.ui;
  const dir = pfwText(false, "pfv-f" + ++pfvUid);
  dir.type = "text";
  dir.value = v.copyDir;
  pfvReg(rec, "copy-dir", dir);
  const dirBox = pfwBox(rec, "default_cwd", "Where the copy is kept", dir, storedCopy ? "CuliBox created it on the first session." : "CuliBox creates it on the first session.");
  dir.oninput = () => { v.copyDir = dir.value; dirBox.clear(); pfvPaint(); };
  ui.copyInput = dir;
  const share = pfwText(true, "pfv-f" + ++pfvUid);
  share.value = v.shareText;
  share.setAttribute("placeholder", "CLAUDE.md");
  pfvReg(rec, "share", share);
  const shareBox = pfwBox(rec, "clone_share", "Files to share from your repo, read-only", share, PFW_HINT_SHARE);
  share.oninput = () => { v.shareText = share.value; shareBox.clear(); pfvPaint(); };
  const lab = pfvMk("label", "pfv-check");
  const cb = pfvMk("input");
  cb.type = "checkbox";
  cb.checked = v.autoReturn === true;
  cb.onchange = () => { v.autoReturn = cb.checked; rec.nudge = false; pfvPaint(); };
  pfvReg(rec, "auto-return", cb);
  lab.append(cb, pfvMk("span", "pfv-check-t", "Bring work home automatically"));
  const out = [dirBox.w, shareBox.w, lab];
  const err = pfvFerr(rec, "clone_no_auto_return");
  if (err) out.push(pfvMk("p", "pfv-ferr", err));
  return out;
}

function pfwDraw(rec, c) {
  const v = rec.values;
  const ui = rec.ui;
  const copy = v.mode === "copy";
  const storedCopy = wmModeOf(c.doc) === "copy";
  const card = wmCardState({ runtime: c.tier, mode: v.mode, unavailable: pfwUnavailable(), locked: false });
  if (!ui.copyInit) ui.copyInit = pfwCopyFields(v);
  const out = [pfwFolderField(rec, c, copy)];
  // A control the user cannot use is not drawn, and its absence is said in one sentence. A profile that already is a
  // copy keeps both ways and the warning: switching it away is the user's to do.
  if (!card.copyDisabled || storedCopy) {
    out.push(pfwModeField(rec, copy));
    if (card.hint) out.push(pfvMk("p", "pfv-hint", card.hint));
  } else if (c.tier !== "host") {
    out.push(pfvMk("p", "pfv-hint", "Private copies are not available on this instance: " + pfwUnavailable() + "."));
  }
  const gone = wmSwitchNote(storedCopy ? "copy" : "folder", v.mode, storedCopy ? c.doc.default_cwd : "", pfwHome());
  if (gone) out.push(pfvMk("p", "pfv-hint", gone));
  if (copy) out.push(...pfwCopyFieldNodes(rec, storedCopy));
  // Focus goes to the folder when the block opens, without opening the dropdown.
  rec.first = { focus() { ui.quiet = true; try { ui.folderInput.focus(); } finally { ui.quiet = false; } } };
  return out;
}

// pfwCheck: what the client refuses before the PUT. The folder field must show the folder it holds (a
// half-typed search is not one), a private copy needs both folders, and the copy's folder is a full path.
// {field, text} or null; the text is the words of today's form (wmFormError) or the spec's for a path.
function pfwCheck(rec) {
  const v = rec.values;
  const ui = rec.ui;
  const copy = v.mode === "copy";
  const folderField = copy ? "clone_of" : "default_cwd";
  if (ui.folderInput && ui.folderInput.value.trim() !== v.folder) {
    ui.typed = ui.folderInput.value;
    return { field: folderField, text: PFE_RELATIVE_PATH };
  }
  const bad = wmFormError(v.mode, { folder: v.folder, copyDir: v.copyDir });
  if (bad) return { field: v.folder.trim() === "" ? "clone_of" : "default_cwd", text: bad };
  if (copy && !v.copyDir.trim().startsWith("/")) return { field: "default_cwd", text: PFE_RELATIVE_PATH };
  return null;
}

pfvRegisterEditor("workspace", {
  values: pfwValues,
  draw: pfwDraw,
  check: pfwCheck,
  // The next session of a private copy is built from a new box; a folder in a box is drawn without the line (the mockup).
  note: (rec, c) => c.tier === "box" && rec.values.mode === "copy",
  fields: true,
});
