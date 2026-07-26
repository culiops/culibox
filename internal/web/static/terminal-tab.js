"use strict";

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
    this.onUpdate = null;

    this.el = document.createElement("div");
    this.el.className = "tab-body term-body";
    const termEl = document.createElement("div");
    termEl.className = "pane-term";
    this.el.append(termEl);

    // Drop a file on the pane → upload into this session's cwd, then type the
    // path at the prompt. The cell outlines while dragging, so the destination
    // is something you can see before letting go.
    if (typeof makeDropZone === "function") {
      makeDropZone(this.el, () => this.cwd, {
        highlight: () => this.el.closest(".cell") || this.el,
        onDone: (data) => insertPathsIntoTerminal(this, data),
      });
    }

    // Image paste: upload and insert the path. Capture phase so it runs before
    // xterm's own paste handling, but only when the clipboard actually carries
    // files — a text paste must reach xterm completely untouched.
    this.el.addEventListener("paste", (e) => {
      const files = (typeof clipboardFiles === "function") ? clipboardFiles(e) : [];
      if (!files.length) return;
      e.preventDefault();
      e.stopPropagation();
      const named = files.map((f) => new File([f], pasteName(f), { type: f.type }));
      uploadFiles(named, this.cwd, { onDone: (data) => insertPathsIntoTerminal(this, data) });
    }, true);

    this.term = new Terminal({
      cursorBlink: true, fontFamily: "ui-monospace, Menlo, monospace",
      fontSize: (typeof fontPrefs !== "undefined" ? fontPrefs.get("terminal") : 13),
      scrollback: 5000, theme: { background: "#000000" },
      // xterm defaults to 1 line per wheel notch, which crawls over a 5000-line
      // scrollback. 3 matches vim's default. Alt+wheel multiplies on top of this
      // (fastScrollSensitivity), so the fast gear stays proportional at 15.
      scrollSensitivity: 3,
      // While the agent holds mouse tracking on (claude's fullscreen renderer),
      // xterm forwards drags to the app instead of selecting. The bypass key is
      // Shift on Windows/Linux but Option on macOS, and the macOS branch is gated
      // on this option — without it, no modifier can select on a Mac at all.
      // Cost: Option+drag no longer does a column selection on macOS.
      macOptionClickForcesSelection: true,
    });
    this.fitAddon = new FitAddon.FitAddon();
    this.term.loadAddon(this.fitAddon);
    this.term.open(termEl);
    try { this.term.loadAddon(new WebglAddon.WebglAddon()); } catch (_) {}
    this.fitAddon.fit();
    this.term.onData((d) => { if (this.ws && this.ws.readyState === WebSocket.OPEN) this.ws.send(enc.encode(d)); });
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

    // Make path-like tokens in terminal output clickable → open in a viewer.
    // Absolute paths open directly; relative paths join the session's initial
    // cwd (best-effort — may miss if the agent cd'd elsewhere).
    if (this.term.registerLinkProvider) {
      const re = /(?:\/|~\/|\.{1,2}\/|[\w.\-]+\/)[\w./\-]+\.[A-Za-z0-9]{1,8}/g;
      this.term.registerLinkProvider({
        provideLinks: (line, cb) => {
          const text = this.term.buffer.active.getLine(line - 1)?.translateToString(true) || "";
          const links = [];
          let m;
          re.lastIndex = 0;
          while ((m = re.exec(text)) !== null) {
            const startX = m.index, tok = m[0];
            links.push({
              range: { start: { x: startX + 1, y: line }, end: { x: startX + tok.length, y: line } },
              text: tok,
              activate: () => openFile(this.resolvePath(tok)),
            });
          }
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
    ws.onopen = () => { this.openedAt = Date.now(); this.setStatus("connected"); this.sendResize(); this.term.focus(); };
    ws.onmessage = (ev) => {
      if (typeof ev.data === "string") this.term.write(ev.data);
      else this.term.write(new Uint8Array(ev.data));
    };
    // Schedule the retry FIRST, then diagnose. The loop must never depend on a
    // network call completing: a wedged page (unanswered auth challenge) leaves
    // fetches pending forever, and scheduling from inside that callback meant the
    // terminal stopped retrying entirely and sat on "reconnecting…" for good.
    // diagnose() can only ever CANCEL this retry, never be required to start it.
    ws.onclose = () => {
      if (this.closed) return;
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
    this.showTrouble("offline"); // server is up, socket is not — keep retrying
  }

  fit() { try { this.fitAddon.fit(); this.sendResize(); } catch (_) {} }

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
    this.troubleKind = kind;
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
    this.troubleMsg.textContent =
      expired ? "Login session expired — the terminal cannot authenticate."
      : kind === "detached" ? "Session detached from the daemon."
      : "Lost connection to the server.";
    this.troubleEl.classList.toggle("expired", expired);
    this.troubleBtn.textContent = expired ? "Reload page" : "Retry now";
    this.troubleBtn.onclick = () => (expired ? location.reload() : this.retryNow());
    // A timed-out probe cannot distinguish "server is down" from "the browser is
    // waiting on a login prompt this page can't see" — both just never answer. So
    // after a few failures, offer the reload that fixes the second case too rather
    // than asserting a cause we do not actually know.
    const stuck = !expired && (this.probeFails || 0) >= 3;
    this.troubleAlt.hidden = !stuck;
    if (stuck) this.troubleMsg.textContent = "Still cannot reach the server — your login may have expired.";
    this.tickTrouble();
    if (!this.troubleTimer) this.troubleTimer = setInterval(() => this.tickTrouble(), 500);
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
    this.hideTrouble();
    try { this.ws && this.ws.close(); } catch (_) {}
    try { this.term.dispose(); } catch (_) {}
  }
}
