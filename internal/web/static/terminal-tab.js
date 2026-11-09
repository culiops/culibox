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

// webglCapFor: Chromium keeps 16 live WebGL contexts per page (8 on Android,
// GetWebglPreferences) and, past that, kills the OLDEST one. MEASURED (#127):
// with 19 tabs open, the three panes on screen went blank while 15 hidden tabs
// kept theirs. At most 8 terminals are on screen (4 panes, each with a docked
// shell), so 12 covers them plus the last few hidden tabs. On Android, 6 stays
// under its 8; a tablet showing more than 6 draws the rest with the DOM renderer.
function webglCapFor(ua) {
  return /Android/i.test(ua || "") ? 6 : 12;
}

// makeWebglPool hands WebGL renderers to the tabs most recently shown. A member
// is { shown(), webglOn() -> bool, webglOff() }. acquire runs whenever a tab is
// shown; past the cap it takes the renderer from the least recently shown member
// that is NOT on screen, which falls back to xterm's DOM renderer (same buffer,
// slower to paint) until it is shown again. With every holder on screen the new
// tab gets the DOM renderer: an on-screen pane never pays for a newer one.
function makeWebglPool(cap) {
  const live = new Set(); // iteration order = least recently shown first
  return {
    acquire(m) {
      if (live.has(m)) { live.delete(m); live.add(m); return true; }
      if (live.size >= cap) {
        let victim = null;
        for (const o of live) if (!o.shown()) { victim = o; break; }
        if (!victim) return false;
        this.release(victim);
      }
      if (!m.webglOn()) return false;
      live.add(m);
      return true;
    },
    release(m) { live.delete(m); m.webglOff(); },
    size() { return live.size; },
  };
}

// loadWebgl loads a WebGL renderer into term, or answers null when the browser
// cannot give one (no WebGL2, GPU blocklisted); the terminal then keeps the DOM
// renderer. The browser drops a context past its cap, on a GPU reset or after
// sleep; the addon waits 3 s for it to come back and then fires onContextLoss.
// Unhandled, the pane stays a blank canvas until the page reloads (#127).
function loadWebgl(term, Addon, onLoss) {
  let a = null;
  try {
    a = new Addon();
    a.onContextLoss(() => onLoss(a));
    term.loadAddon(a);
    return a;
  } catch (_) {
    dropWebgl(a);
    return null;
  }
}

// dropWebgl disposes a WebGL renderer and frees its context slot. dispose()
// alone does not: MEASURED, the context counts against the browser's 16 until
// it is garbage collected, so the next one created kills the oldest live pane.
// _renderer._gl is the vendored addon's private field (xterm/addon-webgl.js);
// without it this is the plain dispose it always was.
function dropWebgl(a) {
  if (!a) return;
  const gl = a._renderer && a._renderer._gl;
  try { a.dispose(); } catch (_) {}
  try {
    const ext = gl && gl.getExtension("WEBGL_lose_context");
    if (ext) ext.loseContext();
  } catch (_) {}
}

// resizeToSend answers the resize message for rows x cols, or null when this
// socket already sent that size. One size change fires the ResizeObserver, the
// font load and fitAll: MEASURED (#126), each change went out three times. No
// trailing debounce: it would widen the window in which xterm's grid and the
// PTY disagree, and the kernel already drops a same-size resize.
function resizeToSend(sent, rows, cols) {
  if (sent && sent.rows === rows && sent.cols === cols) return null;
  return { type: "resize", rows, cols };
}

const webglPool = makeWebglPool(webglCapFor(typeof navigator !== "undefined" ? navigator.userAgent : ""));

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

// termTheme (F05) is the xterm theme. A phone keeps the old pure black, nothing
// else: its options are exactly what they were before the kit tokens existed.
// A desktop builds it from the kit's custom properties (desktop.css :root, only
// defined above the breakpoint): get(name) reads one of them and an empty token
// leaves its key out, so xterm keeps its own default for that colour (and the
// background falls back to the old black). The names are xterm's own option
// names; selectionBackground is what the vendored xterm reads (its theme
// service takes e.selectionBackground, not the older `selection`; see
// xterm/xterm.js).
const TERM_ANSI_NAMES = ["black", "red", "green", "yellow", "blue", "magenta", "cyan", "white",
  "brightBlack", "brightRed", "brightGreen", "brightYellow", "brightBlue", "brightMagenta", "brightCyan", "brightWhite"];
function termTheme(get, mobile) {
  if (mobile) return { background: "#000000" };
  const theme = {};
  const put = (key, token) => {
    const v = String(get(token) || "").trim();
    if (v) theme[key] = v;
  };
  put("background", "--term-bg");
  put("foreground", "--term-fg");
  put("cursor", "--term-cursor");
  put("cursorAccent", "--bg");
  put("selectionBackground", "--term-sel");
  TERM_ANSI_NAMES.forEach((name, i) => put(name, "--ansi-" + i));
  if (!theme.background) theme.background = "#000000";
  return theme;
}

// The desktop theme is read from the stylesheet once per desktop entry and
// kept: a crossing to the phone drops it, so the next desktop entry reads the
// tokens again. Every caller gets its own copy, so no two terminals share an
// options object.
let termThemeDesktop = null;
function currentTermTheme() {
  if (typeof mobileActive === "function" && mobileActive(window.innerWidth)) {
    termThemeDesktop = null;
    return termTheme(null, true);
  }
  if (!termThemeDesktop) {
    // No stylesheet to read (a bare harness): every token is empty, so the theme is the black.
    const cs = typeof getComputedStyle === "function" && typeof document !== "undefined" ? getComputedStyle(document.documentElement) : null;
    termThemeDesktop = termTheme((name) => (cs ? cs.getPropertyValue(name) : ""), false);
  }
  return Object.assign({}, termThemeDesktop);
}

// applyTermThemeAll re-themes every open terminal, pane tabs and docked shells
// alike (tabs.js eachTerminal). The breakpoint listener (app.js) calls it on a
// crossing: a terminal is built once and reparented between pane and dock, never
// rebuilt, so it would otherwise keep the theme of the width it was born at.
//
// It drops the cached desktop theme first, so a crossing always reads the tokens
// again: a read made before the stylesheet applied (every token empty, so the
// plain black) cannot stick, and a terminal built after this copies the fresh one.
function applyTermThemeAll() {
  termThemeDesktop = null;
  if (typeof eachTerminal !== "function") return;
  eachTerminal((t) => { if (t.applyTheme) t.applyTheme(); });
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
      scrollback: 5000, theme: currentTermTheme(),
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
    // WebGL is loaded when the tab is first shown (fit), not here: see webglPool.
    this.webgl = null;
    this.webglBroken = false;
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
    // Any press in the page, in this pane or not, ends a release still pending
    // from an earlier press (see armOsc52). Heard on the document in the capture
    // phase, so a control that cancels its own mousedown cannot hide the press.
    // Removed in dispose(): one listener per live tab, none left behind.
    this.onDocMouseDown = () => this.dropOsc52Release();
    document.addEventListener("mousedown", this.onDocMouseDown, true);
    if (this.term.parser && this.term.parser.registerOscHandler) {
      // true = consumed: xterm has no OSC 52 handler of its own to fall back to.
      this.term.parser.registerOscHandler(52, (data) => { this.onOsc52(data); return true; });
    }
    this.term.attachCustomKeyEventHandler((e) => {
      if (e.type !== "keydown") return true;
      this.armOsc52Key(e);
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
  setStatus(t) {
    const wasTaken = this.statusText === "taken over";
    this.statusText = t;
    this.onUpdate?.();
    // The desktop sidebar row reads "open in another tab" while this tab is parked
    // as taken over (app.js rowLocal, F55): repaint the list on the way in and out
    // rather than at the next 3 s poll. The phone's rows do not read it.
    if (wasTaken !== (t === "taken over") && typeof refreshSessions === "function"
        && !(typeof mobileActive === "function" && mobileActive(window.innerWidth))) refreshSessions();
  }
  activity() { return this.activityState; }
  // needWhy: why the tab reads "waiting" when the agent is not ("sso"), else "".
  needWhy() { return this.needWhyState || ""; }
  setActivity(a, why = "") {
    if (a !== this.activityState || why !== this.needWhy()) {
      const prev = this.activityState;
      this.activityState = a; this.needWhyState = why; this.onUpdate?.();
      // The ended bar reads Crashed only on activity=crashed, which can land a poll after the socket closed.
      if (this.ended && this.troubleEl && this.paneBarDesk()) this.paintPaneBar("ended");
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
  // arm on its behalf. A press in ANOTHER pane or control does the same through
  // the document listener (onDocMouseDown), which runs first on a press here.
  armOsc52(e) {
    this.dropOsc52Release();
    if (this.replaying() || !osc52Gesture(e, this.term.modes.mouseTrackingMode, IS_MAC)) return;
    // The release checks again: a press made before a reconnect can end inside
    // the replay it did not see.
    this.osc52Up = () => { this.osc52Up = null; if (!this.replaying()) this.gestureAt = Date.now(); };
    document.addEventListener("mouseup", this.osc52Up, { once: true, capture: true });
  }

  // dropOsc52Release forgets a release that a press is still waiting on.
  dropOsc52Release() {
    if (this.osc52Up) document.removeEventListener("mouseup", this.osc52Up, true);
    this.osc52Up = null;
  }

  // armOsc52Key is the keyboard half: an exact Ctrl+C opens the window at once.
  armOsc52Key(e) {
    if (!this.replaying() && osc52Gesture(e, this.term.modes.mouseTrackingMode, IS_MAC)) this.gestureAt = Date.now();
  }

  // replaying reports whether this connection's replay (see connect) has not
  // been parsed yet. While it has not, no gesture counts, so an OSC 52 still
  // sitting in it can never be answered by a click the user made during it.
  replaying() { return !!this.replay && this.replay.hold > 0; }

  // holdForReplay says whether a frame is old output that gestures must wait
  // for, and takes the hold when it is. The first frame already has its hold
  // (connect took it). A later frame is old output only when it is a ring
  // resync: a viewer that fell behind the ring is sent RIS (ESC c) plus the
  // whole ring (Session.deliver). Any other frame is live and holds nothing. A
  // live frame that happens to start with ESC c only costs one parse of waiting.
  // A resync also closes a window opened before it: the old output follows.
  holdForReplay(rp, data) {
    if (rp.first) { rp.first = false; return true; }
    const resync = typeof data === "string" ? data.startsWith("\x1bc") : data.length > 1 && data[0] === 0x1b && data[1] === 0x63;
    if (!resync) return false;
    rp.hold++;
    this.gestureAt = 0;
    return true;
  }

  // onOsc52 handles the program asking to write the clipboard (OSC 52): how
  // Claude Code's fullscreen copies its own selection. osc52Decision is the gate;
  // an accepted write spends the gesture, so one drag or Ctrl+C buys one copy.
  // Every copy is announced: the toast is how a user notices a write they did
  // not expect. Drops are silent on screen (an agent must not be able to spam
  // toasts) and logged once per tab, except "format", which spends the gesture
  // and shows one warning.
  onOsc52(data) {
    const v = osc52Decision(data, {
      now: Date.now(),
      armedAt: this.gestureAt,
      focused: document.hasFocus() && document.activeElement === this.term.textarea,
      visible: document.visibilityState === "visible" && hasSize(this.termEl.clientWidth, this.termEl.clientHeight),
    });
    if (v.action !== "write") {
      if (v.reason !== "read" && !this.warnedOsc52) { this.warnedOsc52 = true; console.warn("dropped an OSC 52 clipboard write:", v.reason); }
      // The one drop that is shown. "format" is only reached once the gesture
      // gate has passed, so showing it hands the agent nothing it could not get
      // by asking right after a click anyway, as long as it costs the gesture:
      // one click buys at most one toast, and the next sequence is "no-gesture",
      // silent as before. The text is fixed; nothing the agent sent is echoed.
      if (v.reason === "format") {
        this.gestureAt = 0;
        if (typeof showToast === "function") showToast("Not copied: the agent's text has hidden characters." + osc52SelectHint(), { warn: true });
      }
      return;
    }
    this.gestureAt = 0;
    const n = osc52Lines(v.text);
    copyText(v.text, (ok) => {
      this.term.focus();
      if (typeof showToast !== "function") return;
      if (ok) { showToast(`Copied ${n} line${n === 1 ? "" : "s"} from the agent`); return; }
      showToast("The browser blocked the agent's copy." + osc52SelectHint(), { warn: true });
    });
  }

  connect() {
    this.term.reset();
    // The server answers a new socket with its output ring, which can hold an old
    // OSC 52 (a copy from hours ago, or one the agent planted). It arrives as ONE
    // frame, the first on the socket when the ring is not empty (Session.Subscribe
    // and deliver), and nothing marks where it ends, so the first frame is taken
    // for the replay whether or not it is: gestures do not count from here until
    // xterm has parsed it, and a gesture made before the reconnect is forgotten.
    // xterm queues a frame and parses it later, so a click can land between the
    // frame arriving and its OSC 52 being parsed; that interval is what this
    // closes. The state belongs to this connection: a parse callback that
    // outlives it must not release the next one.
    this.gestureAt = 0;
    this.sentSize = null; // a new socket sends its size once: the server repaints on it
    const rp = { hold: 1, first: true };
    this.replay = rp;
    this.clearRetry();
    this.signedParked = false;
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
      // Like onclose: only the current socket is this tab's output.
      if (this.ws !== ws) return;
      const data = typeof ev.data === "string" ? ev.data : new Uint8Array(ev.data);
      if (this.holdForReplay(rp, data)) this.term.write(data, () => { rp.hold--; });
      else this.term.write(data);
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
    if (this.paneSignedOut()) { this.openedAt = 0; this.parkSignedOut(); return; }
    if (this.openedAt && Date.now() - this.openedAt > 3000) this.retries = 0; // a long-lived
    this.openedAt = 0;                                                        // link resets backoff
    const delay = Math.min(1500 * 2 ** this.retries, 10000);
    this.retries++;
    this.retryAt = Date.now() + delay;
    this.retryTimer = setTimeout(() => {
      this.retryTimer = null;
      if (this.closed) return;
      // Signed out since this was armed: no socket, the bar says so (resumeAfterSignIn is the way back).
      if (this.paneSignedOut()) { this.retryAt = 0; this.parkSignedOut(); return; }
      this.connect();
    }, delay);
    this.showTrouble("offline");
  }

  // paneSignedOut: the page's ONE signed-out flag (app.js sys, set by any 401, cleared by the next OK
  // session poll). `sys` is a const in a later script, so a read before it exists is "not signed out".
  paneSignedOut() {
    try { return !!sys.signedOut; } catch (_) { return false; }
  }

  // parkSignedOut: stop retrying and say so. A 401 will not heal by reconnecting; each attempt was one more
  // rejected WebSocket. resumeAfterSignIn (called when the flag clears) reconnects the parked pane.
  parkSignedOut() {
    this.clearRetry();
    this.signedParked = true;
    this.showTrouble("expired");
  }

  resumeAfterSignIn() {
    if (!this.signedParked || this.closed || this.ended) return;
    this.signedParked = false;
    this.retryNow();
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
    if (probe === "unauthorized") {
      // E9: the retry used to stay armed and keep reconnecting a stale login. Tell the page (the one flag the
      // banner, the title and every pane read), stop the loop, show the bar.
      try { sysNoteSignedOut(); } catch (_) { /* before app.js defines it */ }
      this.parkSignedOut();
      return;
    }
    if (probe === "unreachable") { this.showTrouble("offline"); return; }
    const status = await sessionStatus(this.id);
    if (this.closed || !this.retryTimer) return;
    if (status === null || status === "exited") { this.clearRetry(); this.markEnded(status === "exited"); return; }
    if (status === "detached") { this.showTrouble("detached"); return; }
    this.showTrouble("offline"); // server is up, socket is not: keep retrying
  }

  // applyTheme gives this terminal the theme of the current width (a desktop's
  // graphite, a phone's black). xterm repaints on the option change; the pane's
  // padding is CSS, so the size is untouched and no refit is needed.
  applyTheme() {
    if (this.term) this.term.options.theme = currentTermTheme();
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
    // A sized fit is the one signal every show path gives (pane tab, dock,
    // restore, phone): the ResizeObserver fires when the pane gains a size.
    // Asked only when a renderer could start: a full pool evicts a hidden tab
    // before it learns the newcomer would refuse.
    if (this.webglPossible()) webglPool.acquire(this);
    try { this.fitAddon.fit(); this.sendResize(); } catch (_) {}
  }

  // webglPool member. Off = xterm's DOM renderer. A renderer that cannot start
  // (no WebGL2) is not retried; a lost one is retried the next time it shows.
  shown() { return hasSize(this.termEl.clientWidth, this.termEl.clientHeight); }

  webglPossible() { return !this.closed && !this.webglBroken && typeof WebglAddon !== "undefined"; }

  webglOn() {
    if (this.webgl) return true;
    if (!this.webglPossible()) return false;
    this.webgl = loadWebgl(this.term, WebglAddon.WebglAddon, (a) => { if (this.webgl === a) webglPool.release(this); });
    if (!this.webgl) this.webglBroken = true;
    return !!this.webgl;
  }

  webglOff() {
    const a = this.webgl;
    this.webgl = null;
    dropWebgl(a);
  }

  // Font size: setFontSize applies a size and re-fits (xterm recomputes cell
  // geometry from options.fontSize). zoom/resetZoom drive transient live zoom.
  setFontSize(px) { this.term.options.fontSize = px; this.fit(); }
  zoom(delta) { this.setFontSize(fontPrefs.clamp(this.term.options.fontSize + delta)); }
  resetZoom() { this.setFontSize(fontPrefs.get("terminal")); }

  sendResize() {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return;
    const msg = resizeToSend(this.sentSize, this.term.rows, this.term.cols);
    if (!msg) return;
    this.ws.send(JSON.stringify(msg));
    this.sentSize = { rows: msg.rows, cols: msg.cols };
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
    if (this.paneBarDesk()) { this.paintPaneBar(kind === "expired" ? "signedout" : kind); return; }
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

  // paneBarDesk: the desktop's neutral .pane-bar replaces the legacy bars; the phone keeps .pane-trouble and
  // .pane-ended exactly as they were.
  paneBarDesk() {
    return !(typeof mobileActive === "function" && mobileActive(window.innerWidth));
  }

  // paintPaneBar draws the bar paneBarModel (notifymodel.js) describes, and only draws: every word, dot and button
  // comes from the model. It is rebuilt when the model's key moves and otherwise left alone (the countdown text
  // ticks in tickTrouble). A signed-out page shows the signed-out bar whatever the tab was told.
  paintPaneBar(kind) {
    const k = kind !== "ended" && this.paneSignedOut() ? "signedout" : kind;
    let model = paneBarModel(k, this.activity(), k === "ended" ? this.endedStatus : "");
    if (!model) return;
    // Not designed (no frame words): after three failed probes today's line and its Reload page stay.
    if (k === "offline" && (this.probeFails || 0) >= 3) {
      const text = "Still cannot reach the server. Your login may have expired.";
      model = Object.assign({}, model, { key: "stuck", text, base: text, countdown: false,
        buttons: [{ id: "reload", label: "Reload page", tone: "secondary" }].concat(model.buttons) });
    }
    if (!this.troubleEl || this.troubleKey !== model.key) {
      this.hideTrouble();
      const bar = document.createElement("div");
      bar.className = "pane-bar";
      bar.setAttribute("role", "status");
      if (model.dot) {
        const dot = document.createElement("span");
        dot.className = "dot dot-" + model.dot;
        bar.appendChild(dot);
      }
      const msg = document.createElement("span");
      msg.className = "pane-bar-msg";
      bar.appendChild(msg);
      for (const b of model.buttons) {
        const btn = document.createElement("button");
        btn.type = "button";
        btn.className = "btn btn-sm" + (b.tone === "primary" ? " btn-primary" : b.tone === "ghost" ? " btn-ghost" : "");
        btn.textContent = b.label;
        btn.onclick = () => this.paneBarAct(b.id, btn);
        bar.appendChild(btn);
      }
      this.troubleEl = bar;
      this.troubleMsg = msg;
      this.troubleKey = model.key;
      this.el.insertBefore(bar, this.el.firstChild);
      this.fit();
    }
    this.barModel = model;
    this.tickTrouble();
    if (model.countdown && this.retryAt && !this.troubleTimer) this.troubleTimer = setInterval(() => this.tickTrouble(), 500);
    if (!model.countdown && this.troubleTimer) { clearInterval(this.troubleTimer); this.troubleTimer = null; }
  }

  paneBarAct(id, btn) {
    if (id === "retry" || id === "takeback") this.retryNow();
    else if (id === "reload") location.reload();
    else if (id === "close") closeTabEverywhere(this);
    else if (id === "restart") restartSession({ id: this.id }, btn);
  }

  tickTrouble() {
    if (this.barModel && this.troubleMsg && this.troubleEl) {
      const m = this.barModel;
      const left = this.retryAt ? Math.max(0, Math.ceil((this.retryAt - Date.now()) / 1000)) : 0;
      const t = m.countdown ? (this.retryAt ? m.text.replace("{n}", String(left)) : m.base) : m.text;
      if (this.troubleMsg.textContent !== t) this.troubleMsg.textContent = t;
      return;
    }
    if (!this.troubleCount) return;
    const left = this.retryAt ? Math.max(0, Math.ceil((this.retryAt - Date.now()) / 1000)) : 0;
    this.troubleCount.textContent = this.retryAt ? "retrying in " + left + "s" : "";
  }

  hideTrouble() {
    if (this.troubleTimer) { clearInterval(this.troubleTimer); this.troubleTimer = null; }
    if (this.troubleEl) {
      this.troubleEl.remove(); this.troubleEl = null; this.fit();
      this.troubleKey = null; this.barModel = null; this.troubleMsg = null;
    }
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
    if (this.paneBarDesk()) {
      this.endedStatus = restartable ? "exited" : null;
      this.paintPaneBar("ended");
      return;
    }
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
    if (this.onDocMouseDown) document.removeEventListener("mousedown", this.onDocMouseDown, true);
    this.dropOsc52Release();
    if (this.resizeObserver) { try { this.resizeObserver.disconnect(); } catch (_) {} }
    if (this._fitRaf) { try { cancelAnimationFrame(this._fitRaf); } catch (_) {} this._fitRaf = 0; }
    this.hideTrouble();
    try { this.ws && this.ws.close(); } catch (_) {}
    if (this.disposeTouchScroll) { try { this.disposeTouchScroll(); } catch (_) {} }
    if (this.disposeSwipe) { try { this.disposeSwipe(); } catch (_) {} }
    webglPool.release(this); // frees the context slot now, not at GC
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

// osc52HintText is the sentence that names the way out of a refused or blocked
// agent copy: the terminal's own selection, as the help screen names it (a
// help-only row, so shortcutHint has no chord to render). "" when the page has no
// such row, and "" on a touch screen: Shift+drag / Option+drag is a mouse gesture
// and the terminal has no touch selection to name instead (#111).
function osc52HintText(entry, mac, touch) {
  if (touch || !entry) return "";
  return ` Select with ${entry.text[mac ? "mac" : "other"]} to copy instead.`;
}

function osc52SelectHint() {
  const sel = typeof scEntry === "function" ? scEntry("term.select") : null;
  if (!sel) return "";
  const touch = typeof touchPrimary === "function" && typeof window !== "undefined" && touchPrimary(window.matchMedia);
  return osc52HintText(sel, IS_MAC, touch);
}

// OSC52_FORMAT matches what a selection off a terminal screen never holds and a
// paste can hide a command behind:
//   - a CR that is not half of a CRLF: it returns the cursor to column 0 of
//     the shell it is pasted into, so what follows overwrites what was shown
//   - any Unicode format character (general category Cf): the bidi embeddings
//     and overrides U+202A-202E, the isolates U+2066-2069, LRM/RLM U+200E/200F
//     and ALM U+061C (they reorder neighbouring punctuation the way an embedding
//     does), zero-width space/non-joiner/joiner U+200B-200D, word joiner U+2060,
//     BOM U+FEFF, soft hyphen, the invisible math operators and the tag
//     characters. This is the category, not "everything invisible": it still
//     lets through what Unicode files elsewhere (variation selectors, U+034F,
//     U+180B-180D, the Hangul fillers) and it also refuses a few visible Cf
//     marks (the Arabic number signs from U+0600). The category is used instead
//     of a list so a format character added to Unicode later is covered too.
//   - the line and paragraph separators U+2028 and U+2029 (category Zl and Zp):
//     a renderer may break a line there that a shell does not
// Cost, accepted: emoji ZWJ sequences (a family, a profession) and the tag
// flags are refused as a whole. Shift+drag (Option+drag on macOS) still copies
// them: that is the user's own selection, which this screen does not touch.
const OSC52_FORMAT = /\r(?!\n)|[\p{Cf}\u2028\u2029]/u;

// osc52Lines counts the lines in a copied text, for the toast. A trailing line
// break ends the last line instead of starting an empty one, so a selection that
// runs to the end of a row reads as the rows the user saw.
function osc52Lines(text) {
  return text.replace(/\r?\n$/, "").split(/\r?\n/).length;
}

// osc52Decision decides what to do with an OSC 52 sequence (`Pc;Pd`): the
// program asking to write, or read, the user's clipboard. The program is the
// agent, which is untrusted, so a write is honoured only inside the window
// after a gesture, in a pane that is visible and focused at the moment it
// arrives; st = {now, armedAt, focused, visible}. Returns
//   {action: "write", text}  copy text
//   {action: "drop", reason} ignore it: read | hidden | unfocused | no-gesture |
//                            empty | too-large | invalid | control | format
// A read (`Pd = ?`) is refused before anything else: answering it would hand
// the clipboard (a password, a token) to the agent.
//
// Decoding is strict where atob() forgives: unpadded input, spaces, bytes that
// are not UTF-8. Text with control characters other than tab and line breaks is
// refused: a selection off a screen never has them, and ESC is how a pasted
// payload breaks out of bracketed paste in a terminal that does not filter it.
// Text with a lone CR, a Unicode format character or a line/paragraph separator
// (OSC52_FORMAT) is refused too, as "format": the one refusal the page shows,
// and only after the gate.
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
  if (OSC52_FORMAT.test(text)) return { action: "drop", reason: "format" };
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
  module.exports = { TerminalTab, termTheme, currentTermTheme, applyTermThemeAll, resizeToSend, makeWebglPool, webglCapFor, webglPool, loadWebgl, dropWebgl, termKeyAction, osc52Decision, osc52Gesture, osc52Lines, osc52HintText, isBrokenMouseReport, resolveTermPath, classifyTermPath, hasSize, cellHeightOf, measureCellHeight, touchScrollStep, installTouchScroll, appOwnsMouse, emitWheel, installSwipe };
}
