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

    this.el = document.createElement("div");
    this.el.className = "tab-body term-body";
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
    // Fetched async, so re-sync the tree once it arrives (if this is the focused session).
    api("/api/sessions").then((list) => {
      const s = (list || []).find((x) => x.id === id);
      if (s && s.cwd) {
        this.cwd = s.cwd;
        if (typeof syncFocusedTree === "function") syncFocusedTree();
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
  module.exports = { isBrokenMouseReport, hasSize };
}
