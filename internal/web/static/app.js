"use strict";

// --- tiny fetch helpers ------------------------------------------------------
async function api(path, opts) {
  const res = await fetch(path, opts);
  if (!res.ok) {
    let msg = res.statusText;
    try { msg = (await res.json()).error || msg; } catch (_) {}
    throw new Error(msg);
  }
  return res.status === 204 ? null : res.json();
}

// --- state -------------------------------------------------------------------
let activeName = null;   // currently opened session
let term = null;         // xterm Terminal
let fit = null;          // FitAddon
let ws = null;           // WebSocket to /ws/<name>
const enc = new TextEncoder();

const $ = (id) => document.getElementById(id);

// --- session list ------------------------------------------------------------
async function refreshSessions() {
  const ul = $("sessions");
  let sessions;
  try { sessions = await api("/api/sessions"); }
  catch (e) { ul.replaceChildren(emptyLi("Error: " + e.message)); return; }

  if (!sessions || sessions.length === 0) {
    ul.replaceChildren(emptyLi("No sessions yet"));
    return;
  }
  ul.replaceChildren(...sessions.map(renderSession));
}

function emptyLi(text) {
  const li = document.createElement("li");
  li.className = "empty";
  li.textContent = text;
  return li;
}

// renderSession builds a list item with DOM methods (no innerHTML) so a
// tmux-supplied session name can never inject markup.
function renderSession(s) {
  const li = document.createElement("li");
  if (s.name === activeName) li.classList.add("active");

  const name = document.createElement("span");
  name.className = "name";
  name.textContent = s.name;
  name.onclick = () => openSession(s.name);

  const kill = document.createElement("button");
  kill.className = "kill";
  kill.title = "Kill session";
  kill.textContent = "×"; // ×
  kill.onclick = (ev) => { ev.stopPropagation(); killSession(s.name); };

  li.append(name, kill);
  return li;
}

// --- spawn / kill ------------------------------------------------------------
async function loadAgents() {
  try {
    const agents = await api("/api/agents");
    const sel = $("agent");
    sel.replaceChildren(...(agents || []).map((a) => {
      const opt = document.createElement("option");
      opt.value = a; opt.textContent = a; // textContent: no markup injection
      return opt;
    }));
  } catch (e) { /* leave empty; spawn will error clearly */ }
}

$("spawn").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const agent = $("agent").value;
  const cwd = $("cwd").value.trim();
  try {
    const s = await api("/api/sessions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ agent, cwd }),
    });
    await refreshSessions();
    openSession(s.name);
  } catch (e) { alert("Spawn failed: " + e.message); }
});

async function killSession(name) {
  try { await api("/api/sessions/" + encodeURIComponent(name), { method: "DELETE" }); }
  catch (e) { alert("Kill failed: " + e.message); return; }
  if (name === activeName) closeTerminal();
  refreshSessions();
}

// --- terminal ----------------------------------------------------------------
function closeTerminal() {
  if (ws) { try { ws.close(); } catch (_) {} ws = null; }
  if (term) { term.dispose(); term = null; }
  activeName = null;
  $("placeholder").style.display = "flex";
  setStatus("");
}

function setStatus(t) { $("status").textContent = t; }

function openSession(name) {
  if (name === activeName && ws && ws.readyState === WebSocket.OPEN) return;
  closeTerminal();
  activeName = name;
  $("placeholder").style.display = "none";
  refreshSessions();

  term = new Terminal({
    cursorBlink: true, fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
    fontSize: 13, theme: { background: "#000000" },
  });
  fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  term.open($("term"));
  fit.fit();

  const proto = location.protocol === "https:" ? "wss" : "ws";
  ws = new WebSocket(`${proto}://${location.host}/ws/${encodeURIComponent(name)}`);
  ws.binaryType = "arraybuffer";
  setStatus("connecting…");

  ws.onopen = () => { setStatus("connected"); sendResize(); term.focus(); };
  ws.onclose = () => setStatus("disconnected");
  ws.onerror = () => setStatus("error");
  ws.onmessage = (ev) => {
    if (typeof ev.data === "string") term.write(ev.data);
    else term.write(new Uint8Array(ev.data));
  };

  // Browser keystrokes -> binary frames (do NOT use AttachAddon: it sends TEXT,
  // which would collide with our resize control channel).
  term.onData((d) => { if (ws && ws.readyState === WebSocket.OPEN) ws.send(enc.encode(d)); });
}

function sendResize() {
  if (!term || !ws || ws.readyState !== WebSocket.OPEN) return;
  ws.send(JSON.stringify({ type: "resize", rows: term.rows, cols: term.cols }));
}

window.addEventListener("resize", () => {
  if (fit) { fit.fit(); sendResize(); }
});

// --- boot --------------------------------------------------------------------
loadAgents();
refreshSessions();
setInterval(refreshSessions, 3000);
