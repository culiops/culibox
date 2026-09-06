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
    this.term.attachCustomKeyEventHandler((e) => {
      if (e.type !== "keydown") return true;
      if (e.code === "KeyC" && !e.altKey && ((e.ctrlKey && e.shiftKey) || e.metaKey)) {
        const sel = this.term.getSelection();
        if (!sel) return true;
        e.preventDefault();
        copyText(sel, () => this.term.focus());
        return false;
      }
      if (e.code === "KeyV" && e.ctrlKey && !e.altKey && !e.shiftKey && !e.metaKey) return false;
      return true;
    });

    // cwd: used to resolve relative clicked paths AND to root the file tree.
    // Fetched async, so re-sync the tree once it arrives (if this is the focused
    // session). The steer rail draws the same cwd, so it is repainted here too —
    // without this a freshly opened session shows an empty cwd row in the rail
    // until some later focus change happens to rebuild it.
    api("/api/sessions").then((list) => {
      const s = (list || []).find((x) => x.id === id);
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
    // join the session's initial cwd (best-effort: may miss if the agent cd'd).
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
              else openFile(this.resolvePath(l.text));
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
  setActivity(a) { if (a !== this.activityState) { this.activityState = a; this.onUpdate?.(); } }
  usage() { return this.usageData; }
  setUsage(u) {
    // Re-render only when the readout actually moved, so a poll that returns the
    // same numbers doesn't rebuild the tab strip.
    const key = u ? `${u.contextTokens}|${u.pct ?? ""}|${u.model}|${u.effort}` : "";
    if (key !== this._usageKey) { this._usageKey = key; this.usageData = u; this.onUpdate?.(); }
  }

  onShow() { this.fit(); this.term.focus(); }
  onHide() {}

  resolvePath(tok) {
    if (tok.startsWith("/")) return tok;
    if (tok.startsWith("~/")) return window.HOME_DIR ? window.HOME_DIR + tok.slice(1) : tok; // ~/foo → <home>/foo
    const base = (this.cwd || "").replace(/\/$/, "");
    return base ? base + "/" + tok.replace(/^\.\//, "") : tok;
  }

  connect() {
    this.term.reset();
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
    if (status === null || status === "exited") { this.clearRetry(); this.markEnded(); return; }
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

  markEnded() {
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
    bar.append(msg, btn);
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

if (typeof module !== "undefined" && module.exports) {
  module.exports = { isBrokenMouseReport, hasSize, cellHeightOf, measureCellHeight, touchScrollStep, installTouchScroll, appOwnsMouse, emitWheel, installSwipe };
}
