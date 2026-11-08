"use strict";

// The New profile page and Duplicate (spec 5.2, 5.8), and the "Profile created" next steps. Both pages ask for the same
// three things, in the same card: Name ("You can't rename a profile later."), Where it runs (Box, recommended, with
// "Docker found" or the reason it is off; On this machine) and Workspace (the shared folder picker, wspicker.js).
// Duplicate fills them from the profile it copies and lists, under the card, everything else the copy will get, in the
// words of the read view (profileedit.js pfvCopyList of profileview.js's pfvReadView: one builder, so the list cannot
// drift from the page it leads to). Nothing is written until Create: then the profile is POSTed (profileedit.js
// pfvNewDoc / pfvDuplicateDoc build it), a host profile is asked about first (the C3 question, spec 5.8), and the page
// lands on the new profile's read view with its next steps.
//
// The page is one record in pfvState.edits, under the id "draft", registered through the editor seam
// (pfvRegisterEditor): rec.values = {name, tier, folder, dflt} is what the three controls hold, so the page's own
// dirty test (pfvEditDirty), its leave question (pfvLeaveGuard, for every way out: another profile, a Settings tab, Esc,
// the close buttons, New profile and Duplicate), the release of the folder picker (pfvDropEdits) and the Esc layering
// (pfvEscTarget reads rec.ui.folderPicker) all work for it as they do for a block. pfvState.page says which page it is
// ("new" or "dup"; "" is a profile), pfvState.src what a copy is made from, pfvState.back where Cancel goes and
// pfvState.created the profile whose next steps the overview shows. This file only draws; the profile's words are
// profileview.js's and what is written is profileedit.js's.
//
// Every top-level name here starts with pfn/PFN_, except the three seams the page calls (pfvNew, pfvDuplicate,
// pfvNextSteps; the page scripts share one scope). Classic script: profilepage.js's helpers (pfvMk, pfvReg,
// pfvFieldBox, pfvCard, ...), profileedit.js, profileform.js (saveTarget, withSchema), the factory createWsPicker and the
// globals of app.js (api, askConfirm, profiles, loadProfiles, pfSchema) are read at call time.

const PFN_NAME_HINT = "You can't rename a profile later.";
const PFN_NO_NAME = "Give the profile a name.";
const PFN_HOST_HOLDS = "On this machine a profile cannot hold AWS, GitHub or tokens.";
const PFN_COPY_NEEDS_BOX = "A private copy needs a box.";
const PFN_FOLDER_HINT = "The folder it works in. In a box, the only place it can write.";
const PFN_FOLDER_PLACEHOLDER = "~/work/payments-api";
const PFN_NEW_SUB = "Three choices to start. Everything else is added on the profile's page.";
const PFN_NEW_AFTER = "It starts as Claude Code with its default model. You can change that, and add cloud access, on the next page.";
const PFN_DUP_NOTE = "Nothing is saved until you click Create copy.";

const pfnHome = () => (typeof window !== "undefined" && window.HOME_DIR) || "";
const pfnTaken = () => (typeof profiles !== "undefined" && Array.isArray(profiles) ? profiles : []).map((p) => p.default_cwd).filter(Boolean);
const pfnIsCopy = (doc) => !!doc && typeof doc.clone_of === "string" && doc.clone_of !== "";
// The profile field the Workspace control is: the folder to copy for a private copy, else the folder it works in.
const pfnFolderKey = (copy) => (copy ? "clone_of" : "default_cwd");

// --- the record ----------------------------------------------------------------------------------------------------

// pfnValues: what the three controls start with. New: no name, Box when there are boxes (host when there are not: the
// founder's rule, so the only usable choice is lit), no folder. Duplicate: no name (names cannot change later, so the user
// picks it), the source's tier and folder; a private copy's folder is the folder it copies, and it runs in a box.
// dflt is the tier the page offers on its own, so a pick that is the same as it is no change.
function pfnValues(c) {
  const src = pfvState.page === "dup" ? pfvState.src : null;
  if (!src) {
    const tier = c.boxAvailable ? "box" : "host";
    return { name: "", tier, folder: "", dflt: tier };
  }
  const d = src.doc;
  const copy = pfnIsCopy(d);
  const tier = !copy && pfvTierOf(src.view, d) === "host" ? "host" : "box";
  const folder = copy ? d.clone_of : typeof d.default_cwd === "string" ? d.default_cwd : "";
  return { name: "", tier, folder, dflt: tier };
}

// pfnSettled: the values as they count when a change is looked for: the tier only when it is not the page's own.
function pfnSettled(v) {
  return { name: v.name, tier: v.tier === v.dflt ? "" : v.tier, folder: v.folder };
}

// pfnKey: what the page's draw depends on that is not in the record: whether there are boxes and why not, and for a copy
// the lists its rows are named from. A refetch that answers the same changes nothing on screen.
function pfnKey(c) {
  const inp = pfvInputs();
  return JSON.stringify([c.boxAvailable, c.reason, inp.meta, pfvCredsKey(inp.creds), inp.bundleImports]);
}

// pfnRefresh: /api/meta or /api/credentials answered (pfvMetaChanged). A New profile page that has not been given a tier
// follows whether there are boxes; every page draws its list again from the new lists. What was typed stays.
function pfnRefresh(rec, c) {
  const key = pfnKey(c);
  if (key === rec.ui.key) return;
  if (pfvState.page === "new") {
    const want = c.boxAvailable ? "box" : "host";
    const v = rec.values;
    if (v.tier === v.dflt) v.tier = want;
    v.dflt = want;
  }
  pfnRedraw(rec);
}

// pfnRedraw: draw the page again from the record, keeping focus on the control that has it.
function pfnRedraw(rec) {
  const root = document.getElementById("pfv-in");
  if (!root || pfvState.edits.draft !== rec) return;
  const at = document.activeElement;
  const key = Object.keys(rec.fields).find((k) => rec.fields[k] === at && k !== "folder");
  root.replaceChildren(...pfnDraw(rec, pfvEditCtx()));
  const el = key ? rec.fields[key] : null;
  if (el && typeof el.focus === "function") el.focus();
}

// pfnChanged: a control changed a value that alters what the page shows (the tier): it is drawn again, and the header
// note and the leave question read the record.
function pfnChanged(rec, key) {
  if (key) rec.ferrs = rec.ferrs.filter((e) => !pfvFieldIs(e.field, key));
  pfnRedraw(rec);
  pfvPaint();
}

// --- the page ------------------------------------------------------------------------------------------------------

function pfnNameField(rec, src) {
  const v = rec.values;
  const input = pfvMk("input", "pfv-ctl");
  input.type = "text";
  input.id = "pfv-f" + ++pfvUid;
  input.value = v.name;
  input.setAttribute("autocomplete", "off");
  input.setAttribute("spellcheck", "false");
  input.setAttribute("placeholder", src ? src.name + "-copy" : "e.g. payments-readonly");
  pfvReg(rec, "name", input);
  const box = pfvFieldBox(rec, "name", "Name", input, [PFN_NAME_HINT]);
  input.oninput = () => { v.name = input.value; box.clear(); pfvPaint(); };
  // Enter in the one text field that is not a picker is "Create".
  input.onkeydown = (ev) => {
    if (ev.key !== "Enter") return;
    ev.preventDefault();
    pfnCreate(rec);
  };
  return box;
}

// pfnTierField: where it runs, the two cards of the Runs in block (pfvTierCards). With no box layer the Box card is off and says why
// (the reason is /api/meta's, in the read view's words), as it is for a profile that starts as host. A copy of a box profile
// keeps its Box: the card stays selectable, with that line under it as the read view shows it for a box profile. A private copy
// has no host card, and its absence is said in a sentence.
function pfnTierField(rec, c, copy) {
  const v = rec.values;
  const cards = pfvTierCards(rec, c, {
    tier: v.tier, group: "pfn_tier", host: !copy, boxOff: !c.boxAvailable && v.dflt !== "box",
    pick: (tier) => { v.tier = tier; pfnChanged(rec, "runtime"); },
  });
  const fs = pfvMk("fieldset", "pfv-field pfv-choice");
  fs.append(pfvMk("legend", "pfv-flabel", "Where it runs"), ...cards);
  if (copy) fs.append(pfvMk("p", "pfv-hint", PFN_COPY_NEEDS_BOX));
  else if (v.tier === "host") fs.append(pfvMk("p", "pfv-hint", PFN_HOST_HOLDS));
  return fs;
}

// pfnFolderHint: what is said under Workspace.
function pfnFolderHint(src, copy) {
  if (!src) return PFN_FOLDER_HINT;
  if (copy) return "Copied from " + src.name + ". It works on a new private copy of this folder.";
  return src.doc.default_cwd ? "Copied from " + src.name + ". In a box, the only place it can write." : PFN_FOLDER_HINT;
}

// pfnFolderField: Workspace, the Workspace block's folder control (pfwFolderBox: the same picker, Esc wiring and release). The error key
// is the control's own: clone_of for a private copy, whose Workspace is the folder it copies.
function pfnFolderField(rec, src, copy) {
  const v = rec.values;
  return pfwFolderBox(rec, {
    errKey: pfnFolderKey(copy), label: "Workspace", hint: [pfnFolderHint(src, copy)], placeholder: PFN_FOLDER_PLACEHOLDER,
    onPick: (path) => { v.folder = path; rec.ui.typed = undefined; rec.ui.clearFolder(); pfvPaint(); },
  });
}

// pfnHasBack: Cancel has somewhere to go: the profile this was started from, or any profile at all.
const pfnHasBack = () => !!pfvState.back || (typeof profiles !== "undefined" && Array.isArray(profiles) && profiles.length > 0);

// pfnCancel: back to the profile the page was started from, else the first one there is. What was typed is the user's own to
// drop: no question.
function pfnCancel() {
  const list = typeof profiles !== "undefined" && Array.isArray(profiles) ? profiles : [];
  return pfvShow(pfvState.back || (list.find((p) => p.editable) || list[0] || {}).name || "");
}

// pfnCopyItems: "This copy will also get" for the profile as it will be created with the tier and folder the page holds
// now: the read model of that profile (a copy that runs on this machine has none of the cloud blocks, so it lists none),
// listed by pfvCopyList.
function pfnCopyItems(rec, c) {
  const src = pfvState.src;
  const v = rec.values;
  const tier = v.tier === "host" ? "host" : "box";
  let doc = pfvDuplicateDoc(src.doc, "", { tier, folder: v.folder, home: pfnHome(), taken: pfnTaken(), boxAvailable: c.boxAvailable });
  if (tier === "host") doc = pfvToHost(doc);
  // The copy is a profile you own, whatever the source was (config.json's included).
  const view = Object.assign({}, src.view, { name: "", runtime: tier, editable: true, source: "runtime" });
  return { items: pfvCopyList(pfvReadView(Object.assign({}, pfvInputs(), { view, doc }))), doc };
}

// pfnItemBlock: one block of the list: its label and what the copy gets, drawn as the read view draws it, without the
// Edit and Remove a list has no use for.
function pfnItemBlock(item, known) {
  const sec = pfvMk("section", "pfv-block");
  sec.dataset.block = item.id;
  const body = pfvMk("div", "pfv-body");
  item.lines.forEach((line, i) => {
    const p = pfvMk("p", i === 0 ? "pfv-main" : "pfv-sub");
    p.append(...pfvMonoNodes(line, known));
    body.append(p);
  });
  if (item.rows.length && item.id === "advanced") {
    const dl = pfvMk("dl", "pfv-dl");
    for (const r of item.rows) {
      const dd = pfvMk("dd");
      if (PFV_MONO_KINDS.includes(r.kind)) dd.append(pfvMk("span", "pfv-mono", r.name));
      else dd.append(...pfvMonoNodes(r.name, known));
      if (r.scope) dd.append(" ", pfvMk("span", "pfv-muted", r.scope));
      dl.append(pfvMk("dt", "", r.kind), dd);
    }
    body.append(dl);
  } else if (item.rows.length) {
    const rows = pfvMk("div", "pfv-rows");
    for (const r of item.rows) rows.append(pfvDrawRow(r, known, "", { edit: true }));
    body.append(rows);
  }
  if (item.note) body.append(pfvMk("p", "pfv-tnote", item.note));
  sec.append(pfvMk("h4", "pfv-label", item.label), body);
  return sec;
}

// pfnDraw: the whole page, from the record: the nodes #pfv-in holds (pfvRender asks for them).
function pfnDraw(rec, c) {
  pfvDisposeRec(rec);
  rec.fields = {};
  rec.first = null;
  rec.invalid = [];
  rec.consumed = new Set();
  rec.ui.key = pfnKey(c);
  const dup = pfvState.page === "dup";
  const src = dup ? pfvState.src : null;
  const copy = dup && pfnIsCopy(src.doc);

  const errors = pfvDrawErrors();
  rec.ui.errNode = errors[0] || null;
  const kids = [...errors];
  const head = pfvMk("div", "pfv-head");
  const main = pfvMk("div", "pfv-head-main");
  const title = pfvMk("div", "pfv-title");
  title.append(pfvMk("h2", "pfv-title-name", dup ? "Copy of " + src.name : "New profile"));
  main.append(title, pfvMk("p", "pfv-hsub", dup ? "A new profile with the same settings as " + src.name + ". Give it a name and check the rest." : PFN_NEW_SUB));
  // The head ends as every Settings head does, Esc ✕ (the page has no other way to close, and the first-run New profile page is the only page
  // there is); it has no action of its own (Create is in the card's foot), so there is no divider before it.
  const acts = pfvMk("div", "page-actions");
  acts.append(...pfvHeadTail(false));
  head.append(main, acts);
  kids.push(head);

  const card = pfvMk("div", "pfv-newcard");
  const fields = pfvMk("div", "pfv-fields");
  fields.append(pfnNameField(rec, src), pfnTierField(rec, c, copy), pfnFolderField(rec, src, copy));
  card.append(fields);
  const create = pfvKitBtn(dup ? "Create copy" : "Create profile", "btn-primary", () => pfnCreate(rec));
  rec.ui.createBtn = create;
  const foot = pfvMk("div", "pfv-newfoot" + (dup ? " pfv-dupfoot" : ""));
  foot.append(create);
  if (pfnHasBack()) foot.append(pfvKitBtn("Cancel", "", pfnCancel));

  if (!dup) {
    card.append(foot);
    kids.push(card, pfvMk("p", "pfv-newafter", PFN_NEW_AFTER));
    return kids;
  }
  kids.push(card, pfvMk("h3", "pfv-sheet-h pfv-dup-h", "This copy will also get"));
  const list = pfnCopyItems(rec, c);
  const o = list.doc.options && typeof list.doc.options === "object" ? list.doc.options : {};
  const known = typeof o.model === "string" && o.model !== "" ? [o.model] : [];
  const sheet = pfvMk("div", "pfv-sheet");
  for (const item of list.items) sheet.append(pfnItemBlock(item, known));
  foot.append(pfvMk("p", "pfv-dup-note", PFN_DUP_NOTE));
  kids.push(sheet, foot);
  return kids;
}

// --- create --------------------------------------------------------------------------------------------------------

// pfnCheck: what the client refuses before anything is sent: {field, text} or null. A name that is empty or taken (the
// list on screen may be behind: the server says the same, below), a Workspace that is half typed (the picker's value is the
// folder, not what a search left in the field), a private copy with no folder to copy.
function pfnCheck(rec, name, copy) {
  const v = rec.values;
  if (name === "") return { field: "name", text: PFN_NO_NAME };
  const taken = saveTarget("", name, (typeof profiles !== "undefined" && Array.isArray(profiles) ? profiles : []).map((p) => p.name));
  if (taken.error) return { field: "name", text: taken.error };
  const field = pfnFolderKey(copy);
  const ui = rec.ui;
  if (ui.folderInput && ui.folderInput.value.trim() !== v.folder) {
    ui.typed = ui.folderInput.value;
    return { field, text: PFE_RELATIVE_PATH };
  }
  // A private copy needs the folder it copies (today's sentence); a profile may have no folder at all.
  if (copy && v.folder.trim() === "") return { field, text: wmFormError("copy", { folder: "", copyDir: "" }) };
  return null;
}

// pfnRefuse: show a refusal on the page, with everything typed kept. One about Name or about the Workspace control (its own key: the
// folder to copy for a private copy) goes under that control and takes its focus; every other, the copy's own folder (a private
// copy has no control for it) and any the server words without a field included, is said at the top of the page and brought into
// view, so the click is never left looking dead.
function pfnRefuse(rec, bad, copy) {
  const mine = pfvFieldIs(bad.field, "name") || pfvFieldIs(bad.field, pfnFolderKey(copy));
  rec.ferrs = mine ? [bad] : [];
  pfvState.errors = mine ? [] : [bad.text];
  pfnRedraw(rec);
  if (mine) {
    pfvFocusInvalid(rec);
    return;
  }
  const at = rec.ui.errNode;
  if (at && typeof at.scrollIntoView === "function") at.scrollIntoView({ block: "nearest" });
}

// pfnCreate: Create profile / Create copy. Resolves true when the profile was written.
async function pfnCreate(rec) {
  if (pfvState.edits.draft !== rec || rec.ui.creating) return false;
  const c = pfvEditCtx();
  const v = rec.values;
  const src = pfvState.page === "dup" ? pfvState.src : null;
  const copy = !!src && pfnIsCopy(src.doc);
  const name = v.name.trim();
  pfvState.errors = [];
  const bad = pfnCheck(rec, name, copy);
  if (bad) {
    pfnRefuse(rec, bad, copy);
    return false;
  }
  rec.ferrs = [];
  let doc = src
    ? pfvDuplicateDoc(src.doc, name, { tier: v.tier, folder: v.folder, home: pfnHome(), taken: pfnTaken(), boxAvailable: c.boxAvailable })
    : pfvNewDoc({ name, tier: v.tier, cwd: v.folder, boxAvailable: c.boxAvailable });
  // A host profile gives up the container: say so, and say what a copy drops. Cancel writes nothing and leaves the page as it is.
  if (v.tier === "host") {
    if (!(await pfvConfirmHost(doc))) return false;
    if (pfvState.edits.draft !== rec) return false; // the page moved on while it was asked
    doc = pfvToHost(doc);
  }
  const body = withSchema(doc, pfSchema.get());
  const post = () => api("/api/profiles", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  rec.ui.creating = true;
  let created;
  try {
    created = rec.ui.createBtn ? await withPending(rec.ui.createBtn, "Creating…", post) : await post();
  } catch (e) {
    const taken = e && (e.code === "profile_exists" || (e.body && e.body.code === "profile_exists"));
    // The list on screen was behind: refetch it, so the next check knows the name too.
    if (taken) await loadProfiles(false);
    if (pfvState.edits.draft !== rec) return false;
    if (taken) {
      pfnRefuse(rec, { field: "name", text: saveTarget("", name, [name]).error }, copy);
      return false;
    }
    const r = pfvSaveError(e && typeof e.status === "number" ? e.status : 0, e && e.body && typeof e.body === "object" ? e.body : null);
    pfnRefuse(rec, { field: r.kind === "field" ? r.field : "", text: r.text }, copy);
    return false;
  } finally {
    rec.ui.creating = false;
  }
  await loadProfiles(false);
  if (pfvState.edits.draft !== rec) return true; // the user moved on: nothing here to draw
  const warnings = pfvWarnLines(created);
  pfvDropEdits();
  pfvState.page = "";
  pfvState.src = null;
  pfvState.created = name;
  pfvState.createdBy = src ? "dup" : "new";
  await pfvShow(name);
  if (warnings.length && pfvState.name === name) {
    pfvState.warnings = warnings;
    pfvRender();
  }
  return true;
}

// --- the entry points ------------------------------------------------------------------------------------------------

// pfnOpen: show the page. The way out of whatever was on screen has been asked about (pfvLeaving) by now; an older
// pfvShow that is still loading must not draw over this (pfvPageGen), nor an older fetch (pfvSeq). back is where Cancel goes,
// read before that way out ended what was on screen.
function pfnOpen(kind, src, back) {
  pfvPageGen++;
  pfvSeq++;
  pfvDropEdits();
  pfvClearSaved();
  Object.assign(pfvState, { page: kind, src, back, name: "", doc: null, view: null, errors: [], warnings: [], created: "" });
  pfvState.edits.draft = pfvNewEdit("draft", false);
  pfvSyncOpen();
  pfvRenderNav();
  pfvRender();
  pfvFocusBlock("draft");
  return true;
}

// pfvNew: "+ New profile" (the nav, the old button's entry points): the New profile page. Asks first when the page holds
// changes, as every way out does; the page it is already on is left as it is.
async function pfvNew() {
  if (!pfvOnPage()) return false;
  if (pfvState.page === "new") {
    pfvFocusBlock("draft");
    return true;
  }
  // The profile this was started from: the one on screen, or the one the copy page was made from.
  const back = pfvState.name || (pfvState.page ? pfvState.back : "");
  const go = pfvLeaving();
  if (go !== true && !(await go)) return false;
  return pfnOpen("new", null, back);
}

// pfvDuplicate: Duplicate: the New profile page filled in from the profile on screen. A config.json profile copies the same way:
// the copy is created as a profile you own.
async function pfvDuplicate() {
  if (!pfvOnPage()) return false;
  const from = pfvState.name;
  if (!from) return false;
  const go = pfvLeaving();
  if (go !== true && !(await go)) return false;
  // Ending the open blocks (pfvLeaving) leaves the stored profile; it is read again only when the page holds none.
  let doc = pfvState.name === from ? pfvState.doc : null;
  if (!doc) {
    try { doc = await api("/api/profiles/" + encodeURIComponent(from)); }
    catch (e) {
      if (pfvState.name === from) { pfvState.errors = ["Load failed: " + e.message]; pfvRender(); }
      return false;
    }
  }
  const view = (typeof profiles !== "undefined" && Array.isArray(profiles) ? profiles : []).find((p) => p.name === from);
  // The user went somewhere else meanwhile, or the profile is gone: nothing to copy.
  if (!view || pfvState.name !== from) return false;
  return pfnOpen("dup", { name: from, doc: JSON.parse(JSON.stringify(doc)), view: Object.assign({}, view) }, from);
}

pfvRegisterEditor("draft", { values: pfnValues, settled: pfnSettled, draw: pfnDraw, refresh: pfnRefresh });

// --- the created state ------------------------------------------------------------------------------------------------

// pfvNextSteps: under the header of a profile that was just created: it can start sessions (not on a machine with no Docker, for a
// profile in a box). After New profile, and what it may need next, each a button that opens what it says (cloud access and a
// folder with the "+ Add" choice open); a host profile has no cloud and no folder to add, so only its model is offered. After
// Create copy, only the status line: the copy already has what its source had (spec 5.8, the b-created state is New profile's).
// rv is the read model of the page.
function pfvNextSteps(rv) {
  const host = rv.header.tier === "host";
  const wrap = pfvMk("div", "pfv-next");
  wrap.setAttribute("role", "status");
  const head = pfvMk("div", "pfv-next-head");
  head.append(pfvMk("p", "pfv-next-p", pfvEditCtx().boxAvailable || host ? "Profile created. It can start sessions now." : "Profile created."));
  wrap.append(head);
  if (pfvState.createdBy === "dup") return wrap;
  head.append(pfvMk("span", "pfv-next-h", "Next, if this agent needs them"));
  const list = pfvMk("div", "pfv-next-list");
  const step = (title, text, go) => {
    const b = pfvMk("button", "pfv-next-btn");
    b.type = "button";
    b.append(pfvMk("span", "pfv-next-t", title), pfvMk("span", "pfv-next-d", text));
    b.onclick = go;
    list.append(b);
  };
  if (!host) step("Add cloud access", "AWS, GitHub or a token", () => pfvOpenAdd("cloud"));
  step("Pick a model", "Claude Code's default until you do", () => pfvOpenBlock("agent"));
  if (!host) step("Add a folder it can read", "Read only, outside the workspace", () => pfvOpenAdd("reachable"));
  wrap.append(list);
  return wrap;
}
