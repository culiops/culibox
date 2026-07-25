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
    this.statusText = "…";
    this.onUpdate = null;

    this.el = document.createElement("div");
    this.el.className = "tab-body term-body";
    const termEl = document.createElement("div");
    termEl.className = "pane-term";
    this.el.append(termEl);

    this.term = new Terminal({
      cursorBlink: true, fontFamily: "ui-monospace, Menlo, monospace",
      fontSize: (typeof fontPrefs !== "undefined" ? fontPrefs.get("terminal") : 13),
      scrollback: 5000, theme: { background: "#000000" },
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
    ws.onclose = () => {
      if (this.closed) return;
      this.setStatus("reconnecting…");
      sessionStatus(this.id).then((status) => {
        if (this.closed) return;
        if (status === null || status === "exited") { this.markEnded(); return; }
        if (status === "detached") { this.setStatus("detached"); return; }
        if (this.openedAt && Date.now() - this.openedAt > 3000) this.retries = 0;
        this.openedAt = 0;
        const delay = Math.min(1500 * 2 ** this.retries, 10000);
        setTimeout(() => { if (!this.closed) this.connect(); }, delay);
        this.retries++;
      });
    };
    ws.onerror = () => this.setStatus("error");
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

  markEnded() {
    if (this.ended) return;
    this.ended = true;
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
    try { this.ws && this.ws.close(); } catch (_) {}
    try { this.term.dispose(); } catch (_) {}
  }
}
