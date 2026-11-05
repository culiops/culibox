"use strict";

// WS_SUPERSEDED mirrors wsSuperseded in internal/terminal/terminal.go: the close
// code the server uses when a newer attach evicted this socket. Private-use range,
// so it can never be a protocol status. Changing it means changing both files.
const WS_SUPERSEDED = 4001;

// isBrokenMouseReport spots an SGR mouse report whose coordinates came out NaN
// (`ESC[<0;NaN;NaNm`). xterm derives those from the renderer's measured cell
// size, and something upstream can leave that unmeasured at click time.
//
// The bug is not this app's: the same junk appears clicking a link in a
// terminal inside code-server, a different embedder of the same library. What IS
// ours is the wire: a program like claude cannot parse the sequence, gives up
// partway, and pastes the remainder into its prompt as "aN;NaNm" for the user to
// delete by hand. Dropping it costs nothing: a report with no coordinates could
// not have told the program anything anyway.
//
// Scoped deliberately to the CSI < prefix. Typing the letters N, a, N arrives as
// three separate one-character chunks, so real input can never match.
function isBrokenMouseReport(data) {
  return typeof data === "string" && data.startsWith("\x1b[<") && data.includes("NaN");
}

// hasSize reports whether an element has a real box to fit into. A zero-size
// element is hidden (an inactive tab) or not yet attached to the document; fitting
// it measures nothing and would send a bogus resize, so callers skip it.
function hasSize(w, h) {
  return w > 0 && h > 0;
}

// TOUCH_SLOP_PX is how far a finger has to travel before the gesture counts as a
// scroll rather than a tap. Under it the touch belongs to the browser: a tap is
// what focuses the terminal and raises the on-screen keyboard.
const TOUCH_SLOP_PX = 8;

// cellHeightOf turns a screen box and its row count into the height of one line.
// 0 means "unmeasurable" — a terminal that has not been rendered yet.
function cellHeightOf(screenPx, rows) {
  if (!(screenPx > 0) || !(rows > 0)) return 0;
  return screenPx / rows;
}

// measureCellHeight reads that line height off the DOM instead of xterm's private
// render service: .xterm-screen is exactly `rows` lines tall by construction, and
// unlike term._core it is not going to move in a version bump.
function measureCellHeight(term) {
  const root = term && term.element;
  const screen = root && root.querySelector ? root.querySelector(".xterm-screen") : null;
  return cellHeightOf(screen ? screen.clientHeight : 0, term ? term.rows : 0);
}

// touchScrollStep converts the distance a finger has dragged (px, positive =
// downward) into whole lines for term.scrollLines, plus the sub-line remainder to
// carry into the next move — without that carry a slow drag rounds to zero
// forever and the terminal never moves. Dragging DOWN drags the content down,
// i.e. back through history, which is scrollLines' negative direction.
function touchScrollStep(dragPx, cellHeight) {
  if (!(cellHeight > 0)) return { lines: 0, rest: dragPx };
  const lines = Math.trunc(dragPx / cellHeight);
  return { lines: -lines, rest: dragPx - lines * cellHeight };
}

// WHEEL_NOTCH_PX is one wheel detent. xterm's mouse path reports one wheel event
// as one button-64/65 report regardless of the delta, so the magnitude only has
// to look like a real notch.
const WHEEL_NOTCH_PX = 120;
// MAX_WHEEL_NOTCHES caps what a single move can put on the wire: one touchmove
// reporting a huge jump (a fling, or events the browser coalesced) must not turn
// into a hundred escape sequences at once.
const MAX_WHEEL_NOTCHES = 20;

// appOwnsMouse reports whether the running program asked for mouse reporting.
// MEASURED on a live claude session: modes.mouseTrackingMode === "any", the
// buffer is "alternate" and buffer.length === rows — there is no scrollback for
// scrollLines to move. The wheel appears to work only because xterm forwards it
// to the app, which scrolls itself.
function appOwnsMouse(term) {
  const m = term && term.modes && term.modes.mouseTrackingMode;
  return !!m && m !== "none";
}

// emitWheel hands the app the same wheel it would get from a mouse: a synthetic
// `wheel` event on the screen element, one per line, which xterm then encodes.
//
// Synthesising the escape sequence here instead would mean reimplementing which
// encoding is active (SGR / X10 / URXVT), the button codes and the cell
// coordinates — the exact surface that produced the NaN mouse report this file
// already has to filter. Let xterm own it.
function emitWheel(term, lines, x, y) {
  const root = term && term.element;
  const screen = root && root.querySelector ? root.querySelector(".xterm-screen") : null;
  if (!screen || typeof WheelEvent !== "function") return;
  const deltaY = lines < 0 ? -WHEEL_NOTCH_PX : WHEEL_NOTCH_PX;
  const n = Math.min(Math.abs(lines), MAX_WHEEL_NOTCHES);
  for (let i = 0; i < n; i++) {
    screen.dispatchEvent(new WheelEvent("wheel", {
      deltaY, deltaMode: 0, clientX: x, clientY: y, bubbles: true, cancelable: true,
    }));
  }
}

// installTouchScroll gives the terminal the one gesture a phone has and xterm
// does not: drag to scroll.
//
// xterm.js registers no touch handler at all, and there is no scrollable box for
// the browser to scroll on its own — its scroller (.xterm-scrollable-element) is
// synthetic, so the element under a finger in the middle of the terminal
// (.xterm-link-layer, MEASURED on a live session) has NO scrollable ancestor: not
// .xterm, not .pane-term, not the cell. The mouse wheel works only because xterm
// listens for `wheel` and scrolls the buffer itself. This is the same thing for a
// finger, and the reason the terminal was frozen on mobile while the sidebar and
// the file tree — ordinary overflow:auto boxes — scrolled fine.
//
// Where the drag goes depends on who owns the screen. A shell scrolls xterm's
// own buffer. An agent TUI (claude) runs in the alternate buffer with mouse
// tracking on and no scrollback at all, and scrolls ITSELF in response to wheel
// reports — so the drag has to become wheel notches or nothing happens, which is
// exactly how this fix first shipped half-working.
//
// Deliberately NOT done here: translating the drag into arrow keys for an
// alt-screen app that did NOT ask for the mouse (vim, less). The wheel does
// that, and it is how `^[[B` ended up typed into a terraform prompt — see the
// preambleTrimmer work. A gesture that types into the agent is worse than one
// that does nothing.
function installTouchScroll(term, el, opts) {
  if (!el || typeof el.addEventListener !== "function") return () => {};
  const measure = (opts && opts.cellHeight) || (() => measureCellHeight(term));
  const wheel = (opts && opts.wheel) || ((lines, x, y) => emitWheel(term, lines, x, y));
  let tracking = false;  // a single finger is down and this gesture is a candidate
  let scrolling = false; // it has passed the slop, so the gesture is ours
  let startY = 0, lastY = 0, carry = 0;
  let cell = 0;          // line height, measured once per gesture (see onMove)

  const onStart = (e) => {
    const t = e.touches;
    if (!t || t.length !== 1) { tracking = false; return; }
    tracking = true;
    scrolling = false;
    carry = 0;
    cell = 0;
    startY = lastY = t[0].clientY;
  };
  const onMove = (e) => {
    if (!tracking) return;
    const t = e.touches;
    // A second finger means a pinch: hand the whole gesture back rather than
    // fighting the browser's zoom for it.
    if (!t || t.length !== 1) { tracking = false; return; }
    const y = t[0].clientY;
    carry += y - lastY;
    lastY = y;
    // Measured from the touchstart point, not from the last event: a slow drag
    // arrives as many sub-slop moves and would otherwise never start.
    if (!scrolling && Math.abs(y - startY) < TOUCH_SLOP_PX) return;
    scrolling = true;
    // Once per gesture: clientHeight forces a layout flush and touchmove fires at
    // frame rate. A pane cannot be resized mid-drag, so one measurement holds.
    if (!cell) cell = measure();
    // Nothing can be scrolled without a line height, so swallowing the gesture
    // would only break the page's own behaviour for no gain.
    if (!(cell > 0)) return;
    const step = touchScrollStep(carry, cell);
    carry = step.rest;
    if (step.lines) {
      if (appOwnsMouse(term)) wheel(step.lines, t[0].clientX, y);
      else term.scrollLines(step.lines);
    }
    if (e.cancelable !== false) e.preventDefault();
  };
  const onEnd = () => { tracking = false; scrolling = false; };

  el.addEventListener("touchstart", onStart, { passive: true });
  // passive:false, or preventDefault is ignored and the page keeps the gesture.
  el.addEventListener("touchmove", onMove, { passive: false });
  el.addEventListener("touchend", onEnd, { passive: true });
  el.addEventListener("touchcancel", onEnd, { passive: true });
  return () => {
    el.removeEventListener("touchstart", onStart);
    el.removeEventListener("touchmove", onMove);
    el.removeEventListener("touchend", onEnd);
    el.removeEventListener("touchcancel", onEnd);
  };
}

// installSwipe adds left/right session switching to the same touch surface as
// installTouchScroll. It is deliberately narrow: it does nothing until the
// gesture is committed horizontal past the slop, it NEVER acts while the running
// program owns the mouse (a horizontal drag there is the app's own drag —
// see the rejected arrow-key hack at :103), and it fires once, on touchend,
// only if the total horizontal travel cleared SWIPE_THRESHOLD_PX. Vertical
// scrolling is left entirely to installTouchScroll.
const SWIPE_THRESHOLD_PX = 60;
function installSwipe(term, el, onSwipe) {
  if (!el || typeof el.addEventListener !== "function") return () => {};
  let startX = 0, startY = 0, axis = null, active = false;
  const onStart = (e) => {
    if (e.touches.length !== 1) { active = false; return; }
    startX = e.touches[0].clientX; startY = e.touches[0].clientY; axis = null; active = true;
  };
  const onMove = (e) => {
    if (!active || e.touches.length !== 1) return;
    if (axis === null) {
      const dx = e.touches[0].clientX - startX, dy = e.touches[0].clientY - startY;
      axis = gestureAxis(dx, dy, TOUCH_SLOP_PX);
      if (axis === "horizontal" && appOwnsMouse(term)) { active = false; axis = null; } // ambiguous: let the app have it
    }
  };
  const onEnd = (e) => {
    if (!active || axis !== "horizontal") { active = false; return; }
    const dx = (e.changedTouches[0] ? e.changedTouches[0].clientX : startX) - startX;
    const intent = swipeIntent(dx, SWIPE_THRESHOLD_PX);
    if (intent && typeof onSwipe === "function") onSwipe(intent);
    active = false; axis = null;
  };
  el.addEventListener("touchstart", onStart, { passive: true });
  el.addEventListener("touchmove", onMove, { passive: true }); // read-only; scroll installer owns preventDefault
  el.addEventListener("touchend", onEnd, { passive: true });
  return () => { el.removeEventListener("touchstart", onStart); el.removeEventListener("touchmove", onMove); el.removeEventListener("touchend", onEnd); };
}

// TerminalTab is one agent session as a tab body: an xterm.js terminal bound to
// /ws/{id}, with self-healing reconnect and copy-on-select / clean-paste. The
// terminal + WebSocket + reconnect logic is unchanged from the original Pane;
// only the surrounding chrome (tab bar, close, status) moved up to Cell.
class TerminalTab {
  constructor(id, name) {
    this.id = id;
    this.kind = "terminal";
    this.name = name || id;
    this.cwd = "";
    this.runtime = ""; // "box" | "host", from /api/sessions
    this.agentCwd = ""; // live transcript cwd (a worktree), set by setUsage
    this.closed = false;
    this.ended = false;
    this.retries = 0;
    this.openedAt = 0;
    this.retryTimer = null;  // pending reconnect; its existence IS the live loop
    this.retryAt = 0;        // wall-clock target, for the countdown
    this.troubleEl = null;
    this.troubleTimer = null;
    this.statusText = "…";
    this.activityState = ""; // server-computed activity (a separate axis from status)
    this.usageData = null;   // per-window transcript usage (claude only)
    this._usageKey = "";     // change-detect key so setUsage doesn't churn renders
    this.onUpdate = null;
    this.ctrlArmed = false;  // mobile key bar: ctrl latched, modifies the NEXT typed char

    this.el = document.createElement("div");
    this.el.className = "tab-body";
    const termEl = document.createElement("div");
    termEl.className = "pane-term";
    this.el.append(termEl);
    this.termEl = termEl;

    // Drop a file on the pane → upload into this session's cwd, then type the
    // path at the prompt. The cell outlines while dragging, so the destination
    // is something you can see before letting go.
    if (typeof makeDropZone === "function") {
      makeDropZone(this.el, () => this.cwd, {
        highlight: () => this.el.closest(".cell") || this.el,
        onDone: (data) => { insertPathsIntoTerminal(this, data); refreshTreeAfterUpload(this.cwd); },
      });
    }

    // Image paste: upload and insert the path. Capture phase so it runs before
    // xterm's own paste handling, but only when the clipboard actually carries
    // files: a text paste must reach xterm completely untouched.
    this.el.addEventListener("paste", (e) => {
      const files = (typeof clipboardFiles === "function") ? clipboardFiles(e) : [];
      if (!files.length) return;
      e.preventDefault();
      e.stopPropagation();
      const named = files.map((f) => new File([f], pasteFileName(f), { type: f.type }));
      uploadFiles(named, this.cwd, {
        onDone: (data) => { insertPathsIntoTerminal(this, data); refreshTreeAfterUpload(this.cwd); },
      });
    }, true);

    this.term = new Terminal({
      // "Symbols Nerd Font" last: agent TUIs (claude, lazygit, starship) emit
      // Nerd Font glyphs, and without a fallback here whether the user sees an
      // icon or a tofu box depended on the machine they happened to browse
      // from, not on this app. The Mono variant keeps every glyph one cell wide,
      // so xterm's measurement is unaffected.
      cursorBlink: true, fontFamily: 'ui-monospace, Menlo, monospace, "Symbols Nerd Font"',
      fontSize: (typeof fontPrefs !== "undefined" ? fontPrefs.get("terminal") : 13),
      scrollback: 5000, theme: { background: "#000000" },
      // xterm defaults to 1 line per wheel notch, which crawls over a 5000-line
      // scrollback. 3 matches vim's default. Alt+wheel multiplies on top of this
      // (fastScrollSensitivity), so the fast gear stays proportional at 15.
      scrollSensitivity: 3,
      // While the agent holds mouse tracking on (claude's fullscreen renderer),
      // xterm forwards drags to the app instead of selecting. The bypass key is
      // Shift on Windows/Linux but Option on macOS, and the macOS branch is gated
      // on this option: without it, no modifier can select on a Mac at all.
      // Cost: Option+drag no longer does a column selection on macOS.
      macOptionClickForcesSelection: true,
      // OSC 8 hyperlinks already work in xterm; installing a linkHandler REPLACES
      // its default, which confirms before opening. Keep that confirmation (an OSC
      // 8 link's visible text is arbitrary and can point anywhere) and add what
      // the default lacks: localhost rewriting and noreferrer.
      linkHandler: { activate: (_e, text) => openTerminalUrl(text, { confirm: true }) },
    });
    this.fitAddon = new FitAddon.FitAddon();
    this.term.loadAddon(this.fitAddon);
    this.term.open(termEl);
    try { this.term.loadAddon(new WebglAddon.WebglAddon()); } catch (_) {}
    this.disposeTouchScroll = installTouchScroll(this.term, termEl);
    if (typeof mobileActive === "function" && mobileActive(window.innerWidth) && typeof onTerminalSwipe === "function") {
      this.disposeSwipe = installSwipe(this.term, termEl, (dir) => onTerminalSwipe(dir));
    }
    this._fitRaf = 0;
    this.fit(); // best-effort now; skipped by the guard until the element is sized
    // Re-fit once the block-display font has loaded — cell metrics can shift.
    if (typeof document !== "undefined" && document.fonts && document.fonts.ready) {
      document.fonts.ready.then(() => this.fit()).catch(() => {});
    }
    // Auto-refit whenever the container settles or changes size. This is the fix
    // for the post-refresh "broken until you resize" race: previously a fit that
    // ran before the grid/sidebar layout settled was never corrected on its own —
    // only window-resize or the sidebar splitter (the two fitAll triggers) fixed it.
    if (typeof ResizeObserver === "function") {
      this.resizeObserver = new ResizeObserver(() => this._scheduleFit());
      this.resizeObserver.observe(termEl);
    }
    this.term.onData((d) => {
      if (isBrokenMouseReport(d)) {
        // Once per tab: enough to diagnose later, not enough to spam.
        if (!this.warnedBadMouse) { this.warnedBadMouse = true; console.warn("dropped a mouse report with NaN coordinates:", JSON.stringify(d)); }
        return;
      }
      // Latched ctrl (mobile key bar): the ctrl key armed the NEXT keystroke.
      // A single typed char becomes its control byte; an unmappable one (digit,
      // multibyte, a paste of >1 char) is sent as typed. Either way ctrl disarms
      // and the key bar repaints. This runs only when armed, so desktop typing
      // and normal mobile typing are on the untouched fast path below.
      if (this.ctrlArmed) {
        this.ctrlArmed = false;
        if (typeof renderMobileKeybar === "function") renderMobileKeybar();
        if (d.length === 1) {
          const b = ctrlByte(d);
          if (b) { if (this.ws && this.ws.readyState === WebSocket.OPEN) this.ws.send(enc.encode(b)); return; }
        }
      }
      if (this.ws && this.ws.readyState === WebSocket.OPEN) this.ws.send(enc.encode(d));
    });
    termEl.addEventListener("mouseup", () => {
      const sel = this.term.getSelection();
      if (sel) copyText(sel, () => this.term.focus());
    });
    // OSC 52: the gesture that may let the program write the clipboard (see
    // osc52Gesture). The window opens on the RELEASE, which is when Claude Code
    // copies, and is heard on the document because a drag can end outside the
    // pane. A press that becomes a long drag would otherwise expire before the
    // copy it asked for.
    this.gestureAt = 0;
    termEl.addEventListener("mousedown", (e) => this.armOsc52(e));
    if (this.term.parser && this.term.parser.registerOscHandler) {
      // true = consumed: xterm has no OSC 52 handler of its own to fall back to.
      this.term.parser.registerOscHandler(52, (data) => { this.onOsc52(data); return true; });
    }
    this.term.attachCustomKeyEventHandler((e) => {
      if (e.type !== "keydown") return true;
      if (osc52Gesture(e, this.term.modes.mouseTrackingMode, IS_MAC)) this.gestureAt = Date.now();
      const act = termKeyAction(e, IS_MAC, this.term.hasSelection());
      if (act === "copy") {
        e.preventDefault();
        copyText(this.term.getSelection(), () => this.term.focus());
        return false;
      }
      if (act === "consume") { e.preventDefault(); return false; }
      // paste: xterm must not handle it, and the browser's own paste event must
      // still fire, so no preventDefault.
      return act !== "paste";
    });

    // cwd: used to resolve relative clicked paths AND to root the file tree.
    // Fetched async, so re-sync the tree once it arrives (if this is the focused
    // session). The steer rail draws the same cwd, so it is repainted here too —
    // without this a freshly opened session shows an empty cwd row in the rail
    // until some later focus change happens to rebuild it.
    api("/api/sessions").then((list) => {
      const s = (list || []).find((x) => x.id === id);
      // runtime ("box" | "host") decides how a clicked path resolves; it is set
      // even when cwd is empty.
      if (s) this.runtime = s.runtime || "";
      if (s && s.cwd) {
        this.cwd = s.cwd;
        if (typeof syncFocusedTree === "function") syncFocusedTree();
        if (typeof renderSteerRail === "function") renderSteerRail();
      }
    }).catch(() => {});

    // Clickable tokens in terminal output. links.js decides what is a URL, what is
    // a file path, which wins when they overlap (the bug that used to send
    // `https://host/a.html` to the file viewer as a relative path), and which
    // links may be returned for this row. Absolute paths open directly; relative paths
    // join the agent's live worktree when it entered one (agentCwd), else the spawn
    // cwd (resolveTermPath). A plain `cd` inside the checkout does not move the base.
    // Guarded like the other cross-file collaborators in this file: without it a
    // missing /links.js would throw on every hover, not merely lose the feature.
    if (this.term.registerLinkProvider && typeof linksForRow === "function") {
      this.term.registerLinkProvider({
        provideLinks: (y, cb) => {
          const links = linksForRow(this.term, y).map((l) => ({
            range: l.range,
            text: l.text,
            activate: () => {
              if (l.kind === "url") openTerminalUrl(l.text);
              else this.openClickedPath(l.text, { line: l.line, col: l.col });
            },
          }));
          cb(links.length ? links : undefined);
        },
      });
    }

    this.connect();
  }

  title() { return this.name; }
  status() { return this.statusText; }
  setTitle(name) { this.name = name || this.id; this.onUpdate?.(); }
  setStatus(t) { this.statusText = t; this.onUpdate?.(); }
  activity() { return this.activityState; }
  // needWhy: why the tab reads "waiting" when the agent is not ("sso"), else "".
  needWhy() { return this.needWhyState || ""; }
  setActivity(a, why = "") {
    if (a !== this.activityState || why !== this.needWhy()) {
      const prev = this.activityState;
      this.activityState = a; this.needWhyState = why; this.onUpdate?.();
      // A finished turn is when "what did the agent change?" is worth asking.
      if (typeof gitStatusOnActivity === "function") gitStatusOnActivity(this, prev, a);
    }
  }
  // setAwsSig: the strip draws the session's AWS chip from the session list, not
  // from the tab, so it only learns of a grant/drop through this.
  setAwsSig(k) {
    if (k !== (this._awsSig || "")) { this._awsSig = k; this.onUpdate?.(); }
  }
  usage() { return this.usageData; }
  setUsage(u) {
    // Re-render only when the readout actually moved, so a poll that returns the
    // same numbers doesn't rebuild the tab strip.
    const key = u ? `${u.contextTokens}|${u.pct ?? ""}|${u.model}|${u.effort}` : "";
    if (key !== this._usageKey) { this._usageKey = key; this.usageData = u; this.onUpdate?.(); }
    // Where the agent works NOW (a worktree it entered), for resolvePath and the
    // file tree. Not in the render key: the tab strip does not show it. The
    // tree follows it, so a move re-syncs the tree (treeFollowTarget keys on it;
    // only the focused tab's change can re-root anything). A 204 (u null: no
    // usage line in the transcript tail, e.g. right after /clear) says nothing
    // about where the agent is, so it keeps the last known place; leaving a
    // worktree arrives as usage with an empty cwd.
    if (!u) return;
    const ac = u.cwd || "";
    if (ac !== this.agentCwd) {
      this.agentCwd = ac;
      if (typeof syncFocusedTree === "function") syncFocusedTree();
    }
  }

  onShow() { this.fit(); this.term.focus(); }
  onHide() {}

  // openClickedPath: a boxed session's paths are the box's, so a click opens a
  // host file, a box-viewer file, or says there is nothing to open.
  // opts = {line?, col?} from a `path:12:5` link, forwarded to the viewer.
  openClickedPath(tok, opts) {
    const c = classifyTermPath(tok, this.cwd, this.agentCwd, window.HOME_DIR, this.runtime === "box");
    if (c.kind === "host") {
      // #53: a relative token may name a file that lives in another checkout of
      // the repo (see termAlt). Built into a copy: the caller's opts are theirs.
      const alt = termAlt(tok, this.cwd, this.agentCwd);
      openFile(c.path, alt ? { ...opts, alt } : opts);
    }
    // Looked up on window: openBoxFile lives in a later-loaded script, and a bare
    // reference to it fails lint.sh's cross-script check until that script exists.
    // A top-level function declaration in a classic script IS a window property.
    else if (c.kind === "box") { if (typeof window.openBoxFile === "function") window.openBoxFile(this.id, c.path, opts); }
    else if (typeof showToast === "function") showToast("This file is inside the box only — just /tmp/playwright-mcp is shared out");
  }
  resolvePath(tok) { return resolveTermPath(tok, this.cwd, this.agentCwd, window.HOME_DIR); }

  // armOsc52 is the press half of the gesture: a press osc52Gesture accepts
  // opens the window on its release. Every press first drops a release still
  // pending from an earlier one: a mouseup can be lost (a drag released outside
  // the browser), and the next release anywhere, even a Shift+drag's, must not
  // arm on its behalf.
  armOsc52(e) {
    if (this.osc52Up) document.removeEventListener("mouseup", this.osc52Up, true);
    this.osc52Up = null;
    if (!osc52Gesture(e, this.term.modes.mouseTrackingMode, IS_MAC)) return;
    this.osc52Up = () => { this.osc52Up = null; this.gestureAt = Date.now(); };
    document.addEventListener("mouseup", this.osc52Up, { once: true, capture: true });
  }

  // onOsc52 handles the program asking to write the clipboard (OSC 52): how
  // Claude Code's fullscreen copies its own selection. osc52Decision is the gate;
  // an accepted write spends the gesture, so one drag or Ctrl+C buys one copy.
  // Every copy is announced: the toast is how a user notices a write they did
  // not expect. Drops are silent on screen (an agent must not be able to spam
  // toasts) and logged once per tab.
  onOsc52(data) {
    const v = osc52Decision(data, {
      now: Date.now(),
      armedAt: this.gestureAt,
      focused: document.hasFocus() && document.activeElement === this.term.textarea,
      visible: document.visibilityState === "visible" && hasSize(this.termEl.clientWidth, this.termEl.clientHeight),
    });
    if (v.action !== "write") {
      if (v.reason !== "read" && !this.warnedOsc52) { this.warnedOsc52 = true; console.warn("dropped an OSC 52 clipboard write:", v.reason); }
      return;
    }
    this.gestureAt = 0;
    const n = [...v.text].length;
    copyText(v.text, (ok) => {
      this.term.focus();
      if (typeof showToast !== "function") return;
      if (ok) { showToast(`Copied ${n} character${n === 1 ? "" : "s"} from the agent`); return; }
      // The way out is the terminal's own selection, named as the help screen
      // names it (a help-only row, so shortcutHint has no chord to render).
      const sel = typeof scEntry === "function" ? scEntry("term.select") : null;
      const how = sel ? ` Select with ${sel.text[IS_MAC ? "mac" : "other"]} to copy instead.` : "";
      showToast("The browser blocked the agent's copy." + how, { warn: true });
    });
  }

  connect() {
    this.term.reset();
    // The output ring replays from here, and it can hold an old OSC 52: a
    // gesture made before the reconnect must not let the replay write.
    this.gestureAt = 0;
    this.clearRetry();
    this.hideTrouble();
    this.setStatus("connecting…");
    const proto = location.protocol === "https:" ? "wss" : "ws";
    const ws = new WebSocket(`${proto}://${location.host}/ws/${encodeURIComponent(this.id)}`);
    ws.binaryType = "arraybuffer";
    this.ws = ws;
    // Only the pane the user is actually on may take keyboard focus on connect:
    // a restore opens every pane's socket at once, and whichever answered last
    // would otherwise end up holding the cursor while another pane is outlined.
    ws.onopen = () => {
      this.openedAt = Date.now(); this.setStatus("connected"); this.sendResize();
      if (typeof focusedTerminal !== "function" || focusedTerminal() === this) this.term.focus();
    };
    ws.onmessage = (ev) => {
      if (typeof ev.data === "string") this.term.write(ev.data);
      else this.term.write(new Uint8Array(ev.data));
    };
    // Schedule the retry FIRST, then diagnose. The loop must never depend on a
    // network call completing: a wedged page (unanswered auth challenge) leaves
    // fetches pending forever, and scheduling from inside that callback meant the
    // terminal stopped retrying entirely and sat on "reconnecting…" for good.
    // diagnose() can only ever CANCEL this retry, never be required to start it.
    ws.onclose = (ev) => {
      // Ignore a close from a socket this tab has already replaced: only the
      // current one may drive the tab's state. Unreachable today (connect() runs
      // only when no socket is live): the guard makes it structural, not a
      // property of the call graph.
      if (this.closed || this.ws !== ws) return;
      // A takeover is the one close the client cannot diagnose for itself, and
      // the one where reconnecting is exactly wrong: the server allows a single
      // viewer, so retrying would evict whoever just took the session and start
      // an eviction ping-pong. The server names it (see wsSuperseded).
      if (ev && ev.code === WS_SUPERSEDED) { this.markTakenOver(); return; }
      this.setStatus("reconnecting…");
      this.scheduleRetry();
      this.diagnose();
    };
    ws.onerror = () => this.setStatus("error");
  }

  scheduleRetry() {
    this.clearRetry();
    if (this.openedAt && Date.now() - this.openedAt > 3000) this.retries = 0; // a long-lived
    this.openedAt = 0;                                                        // link resets backoff
    const delay = Math.min(1500 * 2 ** this.retries, 10000);
    this.retries++;
    this.retryAt = Date.now() + delay;
    this.retryTimer = setTimeout(() => {
      this.retryTimer = null;
      if (!this.closed) this.connect();
    }, delay);
    this.showTrouble("offline");
  }

  clearRetry() {
    if (this.retryTimer) { clearTimeout(this.retryTimer); this.retryTimer = null; }
    this.retryAt = 0;
  }

  // retryNow skips the remaining backoff (up to 10s), so a server that came back
  // early is usable immediately instead of after the next scheduled attempt.
  retryNow() {
    if (this.closed) return;
    this.retries = 0;
    this.connect();
  }

  // diagnose refines the trouble message and may end the terminal. It is advisory:
  // every branch that does not definitively end the session leaves the retry armed.
  async diagnose() {
    const probe = await probeServer();
    if (this.closed || !this.retryTimer) return; // reconnected or ended meanwhile
    this.probeFails = probe === "ok" ? 0 : (this.probeFails || 0) + 1;
    if (probe === "unauthorized") { this.showTrouble("expired"); return; }
    if (probe === "unreachable") { this.showTrouble("offline"); return; }
    const status = await sessionStatus(this.id);
    if (this.closed || !this.retryTimer) return;
    if (status === null || status === "exited") { this.clearRetry(); this.markEnded(status === "exited"); return; }
    if (status === "detached") { this.showTrouble("detached"); return; }
    this.showTrouble("offline"); // server is up, socket is not: keep retrying
  }

  // _scheduleFit coalesces a burst of ResizeObserver callbacks (e.g. a splitter
  // drag) into one fit per animation frame.
  _scheduleFit() {
    if (this._fitRaf) return;
    this._fitRaf = requestAnimationFrame(() => { this._fitRaf = 0; this.fit(); });
  }

  fit() {
    // A zero-size element (hidden/inactive tab, or not yet attached) has nothing
    // to fit; measuring it would send a bogus resize. The observer re-fires once
    // it gains a real size.
    if (!hasSize(this.termEl.clientWidth, this.termEl.clientHeight)) return;
    try { this.fitAddon.fit(); this.sendResize(); } catch (_) {}
  }

  // Font size: setFontSize applies a size and re-fits (xterm recomputes cell
  // geometry from options.fontSize). zoom/resetZoom drive transient live zoom.
  setFontSize(px) { this.term.options.fontSize = px; this.fit(); }
  zoom(delta) { this.setFontSize(fontPrefs.clamp(this.term.options.fontSize + delta)); }
  resetZoom() { this.setFontSize(fontPrefs.get("terminal")); }

  sendResize() {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify({ type: "resize", rows: this.term.rows, cols: this.term.cols }));
    }
  }

  // sendText writes text into the session exactly as typing would. It never
  // sends a newline: nothing here can tell whether the terminal is sitting at a
  // prompt, mid-agent-turn, or inside vim, so the user stays the one who
  // presses Enter. Returns false when the socket is not open, so the caller can
  // say so instead of silently dropping the text.
  sendText(text) {
    if (!text) return false;
    // Defense in depth for the "never auto-submit" guarantee: a \n anywhere in
    // this string submits the line, and C1 bytes are read as escape sequences
    // once echoed. Callers validate too; this is the last gate before the PTY.
    if (typeof UNSAFE_TEXT !== "undefined" && UNSAFE_TEXT.test(text)) return false;
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return false;
    this.ws.send(enc.encode(text));
    return true;
  }

  // sendKey writes raw wire bytes the user explicitly pressed on the mobile key
  // bar or the ^C snippet — esc, tab, arrows, Ctrl-<x>, and Return. It is the
  // deliberate counterpart to sendText: sendText refuses control bytes and never
  // submits, because it inserts *text*; sendKey sends exactly the bytes given,
  // INCLUDING \r and \x03, because tapping ↵ or ^C IS the user pressing that key.
  // Bytes come from mobileterm.js (all <0x80) — do not pass user text here.
  // Returns false when the socket is not open so the caller can surface it.
  sendKey(bytes) {
    if (!bytes) return false;
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return false;
    this.ws.send(enc.encode(bytes));
    return true;
  }

  // armCtrl toggles the ctrl latch. The next single char typed on the soft
  // keyboard is then converted to its control byte in onData. Toggling lets a
  // second tap cancel an accidental arm. Returns the new armed state.
  armCtrl() { this.ctrlArmed = !this.ctrlArmed; return this.ctrlArmed; }
  ctrlIsArmed() { return this.ctrlArmed; }

  // showTrouble renders one line above the terminal naming WHY it is disconnected
  // and what the user can do about it. "reconnecting…" alone left a stale login
  // indistinguishable from a restarting server, with no hint that only a reload
  // could fix it.
  showTrouble(kind) {
    if (this.ended || this.closed) return;
    if (!this.troubleEl) {
      this.troubleEl = document.createElement("div");
      this.troubleEl.className = "pane-trouble";
      this.troubleMsg = document.createElement("span");
      this.troubleCount = document.createElement("span");
      this.troubleCount.className = "pane-trouble-count";
      this.troubleBtn = document.createElement("button");
      this.troubleAlt = document.createElement("button");
      this.troubleAlt.className = "alt";
      this.troubleAlt.textContent = "Reload page";
      this.troubleAlt.onclick = () => location.reload();
      this.troubleEl.append(this.troubleMsg, this.troubleCount, this.troubleAlt, this.troubleBtn);
      this.el.insertBefore(this.troubleEl, this.el.firstChild);
      this.fit();
    }
    const expired = kind === "expired";
    const taken = kind === "taken-over";
    this.troubleMsg.textContent =
      expired ? "Login session expired. The terminal cannot authenticate."
      : taken ? "Another tab or window took over this session."
      : kind === "detached" ? "Session detached from the daemon."
      : "Lost connection to the server.";
    this.troubleEl.classList.toggle("expired", expired);
    this.troubleBtn.textContent = expired ? "Reload page" : taken ? "Take back" : "Retry now";
    this.troubleBtn.onclick = () => (expired ? location.reload() : this.retryNow());
    // A timed-out probe cannot distinguish "server is down" from "the browser is
    // waiting on a login prompt this page can't see": both just never answer. So
    // after a few failures, offer the reload that fixes the second case too rather
    // than asserting a cause we do not actually know.
    // "stuck" is about not reaching the server. A takeover means we reached it
    // fine, so it must never inherit that message. markTakenOver already zeroes
    // the counter; !taken keeps that true no matter who else calls in here.
    const stuck = !expired && !taken && (this.probeFails || 0) >= 3;
    this.troubleAlt.hidden = !stuck;
    if (stuck) this.troubleMsg.textContent = "Still cannot reach the server. Your login may have expired.";
    this.tickTrouble();
    // The ticker exists to count a retry down. A parked pane (taken over) has no
    // retry, so arming it would spin at 2 Hz forever writing the same empty string.
    if (!this.troubleTimer && this.retryAt) this.troubleTimer = setInterval(() => this.tickTrouble(), 500);
  }

  tickTrouble() {
    if (!this.troubleCount) return;
    const left = this.retryAt ? Math.max(0, Math.ceil((this.retryAt - Date.now()) / 1000)) : 0;
    this.troubleCount.textContent = this.retryAt ? "retrying in " + left + "s" : "";
  }

  hideTrouble() {
    if (this.troubleTimer) { clearInterval(this.troubleTimer); this.troubleTimer = null; }
    if (this.troubleEl) { this.troubleEl.remove(); this.troubleEl = null; this.fit(); }
  }

  // markTakenOver parks the pane instead of reconnecting: the session is alive
  // and well, it just belongs to another viewer now. "Take back" re-attaches,
  // which evicts that viewer in turn: it will say the same thing, not fight.
  markTakenOver() {
    this.clearRetry();
    this.probeFails = 0;
    this.setStatus("taken over");
    this.showTrouble("taken-over");
  }

  // markEnded parks the pane on its "session ended" bar. restartable = the
  // session still exists as exited (a removed one has nothing to restart), so the
  // bar offers Restart too: on a phone it is the only restart within reach (#2).
  markEnded(restartable) {
    if (this.ended) return;
    this.ended = true;
    this.clearRetry();
    this.hideTrouble();
    this.setStatus("ended");
    const bar = document.createElement("div");
    bar.className = "pane-ended";
    const msg = document.createElement("span");
    msg.textContent = "session ended";
    const btn = document.createElement("button");
    btn.textContent = "Close";
    btn.onclick = () => closeTabEverywhere(this);
    bar.append(msg);
    if (restartable) {
      const rb = document.createElement("button");
      rb.textContent = "Restart";
      rb.className = "pane-ended-restart";
      // The button shows the wait itself; its re-entry guard replaces the old
      // disabled/finally pair.
      rb.onclick = () => { restartSession({ id: this.id }, rb); };
      bar.append(rb);
    }
    bar.append(btn);
    this.el.insertBefore(bar, this.el.firstChild);
    this.fit();
  }

  dispose() {
    this.closed = true;
    this.clearRetry();
    if (this.resizeObserver) { try { this.resizeObserver.disconnect(); } catch (_) {} }
    if (this._fitRaf) { try { cancelAnimationFrame(this._fitRaf); } catch (_) {} this._fitRaf = 0; }
    this.hideTrouble();
    try { this.ws && this.ws.close(); } catch (_) {}
    if (this.disposeTouchScroll) { try { this.disposeTouchScroll(); } catch (_) {} }
    if (this.disposeSwipe) { try { this.disposeSwipe(); } catch (_) {} }
    try { this.term.dispose(); } catch (_) {}
  }
}

// openTerminalUrl is the single gate for every URL a terminal can open: the regex
// provider and the OSC 8 linkHandler both funnel through it. Terminal output is
// semi-trusted (agent output, cloned repos), so the scheme check is here rather
// than in the regex: OSC 8 text never passes through the regex. noopener denies
// the opened page a handle back into the console; noreferrer keeps the console's
// host out of a third party's logs.
async function openTerminalUrl(raw, opts) {
  if (!/^https?:\/\//i.test(raw)) return;
  const url = rewriteLocalhost(raw, location.hostname);
  // Only OSC 8 confirms. A plain-text URL is its own label: you read the target
  // and then clicked it, but an OSC 8 link's visible text can say anything.
  //
  // The await moves window.open out of the click's own task. It stays inside the
  // transient activation the confirm button's click grants, so the popup blocker
  // does not fire; the visual gate exercises this path to keep it honest.
  if (opts && opts.confirm) {
    const ok = await askConfirm({ title: "Open this link?", body: url, confirmLabel: "Open" });
    if (!ok) return;
  }
  window.open(url, "_blank", "noopener,noreferrer");
  // The toast is the ONLY signal that the URL opened differs from the one on
  // screen, so it is guarded by existence rather than wrapped in a catch that would
  // swallow the failure silently. new URL() cannot throw here: url !== raw only
  // when rewriteLocalhost already parsed it.
  if (url !== raw && typeof showToast === "function") {
    showToast("opened " + new URL(url).host + " instead of localhost");
  }
}

// resolveTermPath turns a path token clicked in terminal output into an absolute
// path. A relative one joins onto agentCwd — the agent's live directory from its
// transcript, which differs from the spawn cwd once Claude Code enters a worktree
// (it moves without a chdir) — and onto the spawn cwd when that is unknown.
function resolveTermPath(tok, cwd, agentCwd, home) {
  if (tok.startsWith("/")) return tok;
  if (tok.startsWith("~/")) return home ? home + tok.slice(1) : tok; // ~/foo → <home>/foo
  const base = (agentCwd || cwd || "").replace(/\/$/, "");
  return base ? base + "/" + tok.replace(/^\.\//, "") : tok;
}

// termAlt: what the viewer needs to look for a clicked relative token in the
// repo's OTHER checkouts when the first read is a 404 (#53): {base, rel}, where
// base is the directory the token was actually resolved against (agentCwd, else
// the spawn cwd, exactly as resolveTermPath picked it) and rel is the token
// without a leading `./` or a `:line:col` suffix. Whichever checkout holds the
// file depends on where the agent stood when it printed the path, which the click
// cannot know, so every relative token gets it, whether or not agentCwd is set.
// null for an absolute or `~/` token (nothing to re-base) and when no cwd is
// known (the endpoint needs an absolute base).
function termAlt(tok, cwd, agentCwd) {
  if (tok.startsWith("/") || tok.startsWith("~/")) return null;
  const base = (agentCwd || cwd || "").replace(/\/+$/, "");
  if (!base.startsWith("/")) return null;
  const rel = tok.replace(/^\.\//, "").replace(/:\d+(?::\d+)?$/, "");
  return rel ? { base, rel } : null;
}

// BOX_HOME / BOX_OUT_DIR are the box's own paths (Dockerfile: user "agent",
// playwright-mcp --output-dir). Neither exists on the host.
const BOX_HOME = "/home/agent";
const BOX_OUT_DIR = "/tmp/playwright-mcp";

// classifyTermPath decides where a clicked path lives for THIS session, so a
// boxed session's `~` and /tmp are not read as the host's. Returns
//   host    — open it as a host file at .path (also every host session)
//   box     — a file in the box's allowlisted output dir (served by /boxfile)
//   boxonly — exists only inside the box; there is nothing to open
// Order matters: the workspace is mounted at the SAME path in the box and a
// clone-mode workspace lives under /tmp/ab-*, so the workspace check must beat
// the "other /tmp is box-only" rule.
function classifyTermPath(tok, cwd, agentCwd, home, boxed) {
  if (!boxed) return { kind: "host", path: resolveTermPath(tok, cwd, agentCwd, home) };
  let p;
  if (/^(?:\.\.\/)+tmp\/playwright-mcp\//.test(tok)) {
    // The agent's live cwd can be deeper than the MCP server's (inside a
    // worktree), so a relative `../../tmp/playwright-mcp/x` resolves to the wrong
    // place by depth. The out dir is a fixed absolute path: take it as such.
    p = "/" + tok.replace(/^(?:\.\.\/)+/, "");
  } else if (tok.startsWith("~/")) {
    p = BOX_HOME + tok.slice(1);
  } else {
    p = resolveTermPath(tok, cwd, agentCwd, "");
  }
  p = normalizePath(p); // also collapses `/tmp/playwright-mcp/../x` out of the out dir
  if (p.startsWith(BOX_OUT_DIR + "/")) return { kind: "box", path: p };
  for (const base of [cwd, agentCwd]) {
    const b = base ? normalizePath(base) : "";
    if (b && (p === b || p.startsWith(b === "/" ? "/" : b + "/"))) return { kind: "host", path: p };
  }
  if (p === BOX_HOME || p.startsWith(BOX_HOME + "/") || p.startsWith("/tmp/")) return { kind: "boxonly", path: p };
  return { kind: "host", path: p };
}

// OSC52_WINDOW_MS is how long after a gesture the program may write the
// clipboard. The copy it answers is one round trip (browser, server, PTY, agent
// and back): milliseconds locally, a few hundred over a tunnel. 1.5 s leaves a
// busy agent room without leaving the clipboard open while the user types.
const OSC52_WINDOW_MS = 1500;
// OSC52_MAX_BYTES caps one write, decoded. A full screen is ~30 KB and a long
// scrolled selection a few hundred; 1 MiB is past both and well under xterm's
// own 10 MB limit on a sequence.
const OSC52_MAX_BYTES = 1024 * 1024;

// osc52Decision decides what to do with an OSC 52 sequence (`Pc;Pd`): the
// program asking to write, or read, the user's clipboard. The program is the
// agent, which is untrusted, so a write is honoured only inside the window
// after a gesture, in a pane that is visible and focused at the moment it
// arrives; st = {now, armedAt, focused, visible}. Returns
//   {action: "write", text}  copy text
//   {action: "drop", reason} ignore it: read | hidden | unfocused | no-gesture |
//                            empty | too-large | invalid | control
// A read (`Pd = ?`) is refused before anything else: answering it would hand
// the clipboard (a password, a token) to the agent.
//
// Decoding is strict where atob() forgives: unpadded input, spaces, bytes that
// are not UTF-8. Text with control characters other than tab and line breaks is
// refused: a selection off a screen never has them, and ESC is how a pasted
// payload breaks out of bracketed paste in a terminal that does not filter it.
// The gate runs before decoding, so a flood of writes costs a comparison each.
function osc52Decision(data, st) {
  const i = data.indexOf(";");
  if (i < 0 || !/^[cpqs0-7]*$/.test(data.slice(0, i))) return { action: "drop", reason: "invalid" };
  const pd = data.slice(i + 1);
  if (pd === "?") return { action: "drop", reason: "read" };
  if (!st.visible) return { action: "drop", reason: "hidden" };
  if (!st.focused) return { action: "drop", reason: "unfocused" };
  const age = st.now - st.armedAt;
  if (!(st.armedAt > 0) || age < 0 || age > OSC52_WINDOW_MS) return { action: "drop", reason: "no-gesture" };
  if (!pd) return { action: "drop", reason: "empty" };
  if (pd.length > Math.ceil(OSC52_MAX_BYTES / 3) * 4) return { action: "drop", reason: "too-large" };
  if (pd.length % 4 || !/^[A-Za-z0-9+/]*={0,2}$/.test(pd)) return { action: "drop", reason: "invalid" };
  const bin = atob(pd);
  if (bin.length > OSC52_MAX_BYTES) return { action: "drop", reason: "too-large" };
  let text;
  try {
    text = new TextDecoder("utf-8", { fatal: true }).decode(Uint8Array.from(bin, (c) => c.charCodeAt(0)));
  } catch (_) {
    return { action: "drop", reason: "invalid" };
  }
  if (/[\x00-\x08\x0b\x0c\x0e-\x1f\x7f-\x9f]/.test(text)) return { action: "drop", reason: "control" };
  return { action: "write", text };
}

// osc52Gesture reports whether a user input opens the OSC 52 window. Claude
// Code copies through OSC 52 only for its own selection, which exists only while
// it has mouse reporting on (mouseMode, from term.modes), and only at two
// moments: the release of the drag and Ctrl+C. So the set is exactly
//   - a left press xterm forwards to the program: not a forced selection
//     (Shift, or Option on macOS), which is CuliBox's own copy-on-select and
//     must not be overwritten by the agent a moment later
//   - an exact Ctrl+C, matched on keyCode as xterm does when it sends ^C
// Anything wider is exposure for nothing: Cmd+C and Ctrl+Shift+C send no bytes
// to the PTY, and opening on every keydown would let the agent time a write to
// each keystroke of a reply.
function osc52Gesture(e, mouseMode, mac) {
  if (!mouseMode || mouseMode === "none") return false;
  if (e.type === "mousedown") return e.button === 0 && !(mac ? e.altKey : e.shiftKey);
  if (e.type === "keydown") return e.keyCode === 67 && e.ctrlKey && !e.shiftKey && !e.altKey && !e.metaKey;
  return false;
}

// termKeyAction: what the xterm key handler does with a keydown, decided from
// the shortcut registry (term.copy / term.paste). Returns
//   "copy"    a copy chord with a selection: copy it, swallow the key
//   "consume" off-mac Ctrl+Shift+C with no selection: swallow it, never ^C
//   "paste"   off-mac exact Ctrl+V: xterm ignores it, the browser pastes
//   "pass"    everything else reaches the PTY, including mac Ctrl+V and a
//             bare mac Cmd+C (nothing selected, so Cmd+C is not a copy)
function termKeyAction(e, mac, hasSelection) {
  const matches = typeof shortcutMatches === "function" ? shortcutMatches : require("./shortcuts.js").shortcutMatches;
  if (matches("term.copy", e, mac)) {
    if (hasSelection) return "copy";
    return mac ? "pass" : "consume";
  }
  if (!mac && !e.shiftKey && matches("term.paste", e, mac)) return "paste";
  return "pass";
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { TerminalTab, termKeyAction, osc52Decision, osc52Gesture, isBrokenMouseReport, resolveTermPath, classifyTermPath, hasSize, cellHeightOf, measureCellHeight, touchScrollStep, installTouchScroll, appOwnsMouse, emitWheel, installSwipe };
}
