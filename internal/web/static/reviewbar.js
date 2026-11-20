"use strict";

// The review bar of the Files dock (spec "Review loop (P3)", mockup B2): "2 notes · sent as one message", Preview, and "Paste to <session>".
// It sits at the top of every dock editor (editortab.js mounts one per EditorTab, so it is there whichever file is shown, and says the
// same on all of them: the notes are the SESSION's, across all its files) while any note is pending. When only sent notes are left it
// shrinks to a slim bar that keeps the "Sent (N)" toggle reachable (notesShowSent: sent notes are drawn muted in the diff, to check a fix
// against them).
//
// What Paste does. This is the one place the page types a block into an agent's terminal, so everything is decided at the click, in
// this order, and a refusal at any step types nothing and sends nothing:
//   1. reviewBarLive: pasteBarState (reviewmodel.js): the agent-state gate (pasteGate: idle, or a finished turn; not working, not asking,
//      bracketed paste on), the socket open, and no open file with pending notes holding an unsaved edit (R12: the lines would not be the
//      lines of the file the agent reads from disk);
//   2. the terminal must be on screen: Full mode covers it, so the click leaves Full mode first through the one flow Insert at prompt uses
//      (editorRunLeavingFull: a forced Full mode with an unsaved edit refuses "Save first", R17), and then insertRefused (insertdecision.js,
//      #337) judges the prompt as it is then;
//   3. composeMessage (notesmodel.js): the pending notes as one text, every control character except "\n" and "\t" removed (ESC included,
//      so no note can end the bracketed paste early), at most 16000 characters;
//   4. term.term.paste(text): xterm's paste, which wraps the block in the bracketed-paste markers when the program asked for them. Never
//      sendText or sendKey, never a key, so Enter is never sent: the user presses it. The block sits at the prompt unexecuted.
// Then the notes it carried are marked sent (notesUpdate: the store, the saved record, the Changes chip and the zones stay in one place),
// a toast says to press Enter, and the session's terminal takes the keyboard so that Enter is the user's next key.
// The gate is read when the click lands, synchronously, right before the paste: no timer, no queue, nothing pastes later.
// The notes are read only through notesFor (editornotes.js), which first syncs their lines with the open buffers.

const rbBars = new Map(); // EditorTab -> its bar's parts {el, count, sub, sent, preview, paste, pasteText, sig}

function reviewBarSid(tab) {
  return tab && tab.host && typeof tab.host.sessionId === "function" ? tab.host.sessionId() : null;
}

function reviewBarRow(sid) {
  return (typeof lastSessions !== "undefined" && Array.isArray(lastSessions) ? lastSessions : []).find((s) => s && s.id === sid) || null;
}

// The socket is open: a paste into a closed one goes nowhere (the terminal drops the bytes), and the notes must not be marked sent for it.
function reviewBarOpen(term) {
  return !!term && !!term.ws && term.ws.readyState === 1;
}

// reviewBarLive: pasteBarState of session `sid` as the page is right now (the session row, its terminal, its notes, its open files).
function reviewBarLive(sid) {
  const term = sid ? terminalTabFor(sid) : null;
  const tabs = [];
  for (const tab of rbBars.keys()) {
    if (!tab.disposed && reviewBarSid(tab) === sid) tabs.push({ path: tab.path, name: tab.name, dirty: !!tab.model && tab.dirty() });
  }
  return pasteBarState({ session: reviewBarRow(sid), term, connected: reviewBarOpen(term), list: notesFor(sid), tabs });
}

// --- the DOM ----------------------------------------------------------------------------------------------------------------

function reviewBarBuild() {
  const el = document.createElement("div");
  el.className = "dk-rbar";
  el.setAttribute("role", "region");
  el.setAttribute("aria-label", "Review notes");
  el.hidden = true;
  const t = document.createElement("span");
  t.className = "dk-rbar-t";
  const count = document.createElement("b");
  const sub = document.createElement("span");
  sub.className = "dk-rbar-sub";
  t.append(count, sub);
  const right = document.createElement("div");
  right.className = "dk-rbar-r";
  const btn = (cls, text) => {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "btn btn-sm " + cls;
    const s = document.createElement("span");
    s.textContent = text;
    b.append(s);
    return b;
  };
  const sent = btn("btn-ghost dk-rbar-sent", "Sent");
  const preview = btn("btn-ghost dk-rbar-preview", "Preview");
  const paste = btn("btn-primary dk-rbar-paste", "Paste");
  right.append(sent, preview, paste);
  el.append(t, right);
  return { el, count, sub, sent, preview, paste, sig: "" };
}

// reviewBarMount: the tab gets its bar, first in its element (above the conflict bar and the editor). Called by the EditorTab's constructor.
function reviewBarMount(tab) {
  if (!tab || !tab.el || rbBars.has(tab)) return;
  const bar = reviewBarBuild();
  rbBars.set(tab, bar);
  tab.el.insertBefore(bar.el, tab.el.firstChild);
  // aria-disabled, not disabled: the button stays focusable and says why in its title; a press on it says it again as a toast.
  bar.paste.onclick = () => {
    if (bar.paste.getAttribute("aria-disabled") === "true") { showToast(bar.paste.title, { error: true }); return; }
    reviewBarPaste(reviewBarSid(tab));
  };
  bar.preview.onclick = () => reviewBarPreview(reviewBarSid(tab));
  bar.sent.onclick = () => {
    const sid = reviewBarSid(tab);
    if (sid) notesShowSent(sid, !notesShowSentOf(sid));
  };
  reviewBarRender(reviewBarSid(tab));
}

function reviewBarUnmount(tab) {
  const bar = rbBars.get(tab);
  if (!bar) return;
  rbBars.delete(tab);
  bar.el.remove();
}

// reviewBarRender(sid): repaint the bars of that session's tabs (every tab's for no sid). Called when the notes change (editornotes.js),
// when a tab's state does (EditorTab.paint) and from every session-list update (app.js). Cheap: the state is computed once per session
// and nothing is written to the DOM when it is what the bar already shows.
function reviewBarRender(sid) {
  const states = new Map();
  for (const [tab, bar] of rbBars) {
    if (tab.disposed) continue;
    const id = reviewBarSid(tab);
    if (sid && id !== sid) continue;
    if (!states.has(id)) states.set(id, id ? reviewBarLive(id) : null);
    reviewBarPaint(bar, states.get(id), id);
  }
}

function reviewBarPaint(bar, st, sid) {
  const shape = st ? st.shape : "none";
  const showSent = !!sid && notesShowSentOf(sid);
  const sig = st ? [shape, st.pending, st.sent, st.label, st.ok, st.why, showSent].join("\u0001") : "";
  if (sig === bar.sig) return;
  bar.sig = sig;
  bar.el.hidden = shape === "none";
  if (shape === "none") return;
  const set = (n, t) => { if (n.textContent !== t) n.textContent = t; };
  const full = shape === "full";
  set(bar.count, full ? st.pending + (st.pending === 1 ? " note" : " notes") : st.sent + (st.sent === 1 ? " note sent" : " notes sent"));
  set(bar.sub, full ? " · sent as one message" : " · kept to check the fix against");
  bar.sent.hidden = st.sent === 0;
  set(bar.sent.firstChild, "Sent (" + st.sent + ")");
  bar.sent.setAttribute("aria-pressed", showSent ? "true" : "false");
  bar.sent.title = showSent ? "Hide the sent notes in the diff" : "Show the sent notes in the diff, muted";
  bar.preview.hidden = bar.paste.hidden = !full;
  if (!full) return;
  set(bar.paste.firstChild, st.label);
  bar.paste.setAttribute("aria-disabled", st.ok ? "false" : "true");
  const name = st.label.replace(/^Paste to /, "");
  const title = st.ok ? "Types the notes at " + name + "'s prompt as one pasted block. You press Enter to send." : st.why;
  if (bar.paste.title !== title) bar.paste.title = title;
  bar.preview.title = "Show the exact message that Paste types";
}

// --- Paste ------------------------------------------------------------------------------------------------------------------

// reviewBarPaste: the click. Refuses at once on the state (no point leaving Full mode for a paste that cannot happen), then leaves Full mode
// when it is on and pastes from where the prompt is on screen. The state is read AGAIN inside reviewBarPasteNow, which is what decides.
function reviewBarPaste(sid) {
  if (!sid) return false;
  const st = reviewBarLive(sid);
  if (!st.ok) { showToast(st.why, { error: true }); return false; }
  editorRunLeavingFull(() => { reviewBarPasteNow(sid); });
  return true;
}

// reviewBarPasteNow: the gate, the visibility decision, the message, the paste. Everything is read now, synchronously: a paste is a thing
// this click does, never something queued. Answers whether it pasted.
function reviewBarPasteNow(sid) {
  const term = sid ? terminalTabFor(sid) : null;
  const st = reviewBarLive(sid);
  if (!st.ok) { showToast(st.why, { error: true }); return false; }
  if (insertRefused(term)) return false;
  const list = notesFor(sid);
  const msg = composeMessage(list);
  if (!msg.ok) { showToast("Nothing was pasted: " + msg.why, { error: true }); return false; }
  const ids = list.filter((n) => n.status === "pending").map((n) => n.id);
  term.term.paste(msg.text);
  notesUpdate(sid, (cur) => markSent(cur, ids));
  const name = st.label.replace(/^Paste to /, "");
  showToast("Pasted at " + name + "'s prompt — press Enter to send");
  // The next key is the user's Enter: the pane that holds the terminal is the focused one, and the terminal has the keyboard.
  const at = insertPlace(term);
  if (at) setFocusedCell(at.cell);
  term.grabFocus();
  return true;
}

// --- Preview ----------------------------------------------------------------------------------------------------------------

// reviewBarPreview: the kit's dialog with the composed message in a <pre> (set as text) and a Copy button. It is exactly what Paste would
// type now: the same composeMessage over the same notes.
function reviewBarPreview(sid) {
  if (!sid) return;
  const msg = composeMessage(notesFor(sid));
  if (!msg.ok) { showToast("Nothing to preview: " + msg.why, { error: true }); return; }
  const pre = document.createElement("pre");
  pre.className = "dk-rbar-pre";
  pre.textContent = msg.text;
  const copy = document.createElement("button");
  copy.type = "button";
  copy.className = "btn btn-sm btn-ghost";
  copy.textContent = "Copy";
  copy.onclick = () => copyText(msg.text, (ok) => showToast(ok ? "Copied the message" : "Could not copy the message — select it and copy by hand", { error: !ok }));
  const row = document.createElement("div");
  row.className = "dk-rbar-copyrow";
  row.append(copy);
  askDialog({
    title: "Message to paste",
    body: "What Paste types at the prompt, as one block.",
    notes: ["Nothing is sent until you press Enter."],
    info: true,
    wide: true,
    confirmLabel: "Close",
    extra: [pre, row],
  });
}
