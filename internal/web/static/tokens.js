"use strict";

// Access-token management (the "Access tokens" settings tab). Bearer tokens let
// a client app / CLI authenticate to this box without the browser session; each
// is named and independently revocable. The plaintext is shown exactly once, at
// creation — the server only ever stores its hash.

function setTokErr(msg) {
  const el = document.getElementById("tok-err");
  if (el) el.textContent = msg || "";
}

function tokWhen(unix) {
  if (!unix) return "never";
  const d = new Date(unix * 1000);
  return d.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
}

async function loadTokens() {
  const list = document.getElementById("tok-list");
  if (!list) return;
  setTokErr("");
  // Hide any one-time reveal from a previous create: the secret must not linger
  // in the DOM once the user has moved on (reopened the tab, refreshed the list).
  const reveal = document.getElementById("tok-reveal");
  if (reveal) reveal.hidden = true;
  let items;
  try {
    items = await api("/api/tokens");
  } catch (e) {
    setTokErr("Could not load tokens: " + e.message);
    return;
  }
  list.textContent = "";
  if (!items || items.length === 0) {
    const li = document.createElement("li");
    li.className = "tok-empty";
    li.textContent = "No tokens yet.";
    list.appendChild(li);
    return;
  }
  for (const t of items) {
    const li = document.createElement("li");
    li.className = "tok-item";

    const name = document.createElement("span");
    name.className = "tok-name";
    name.textContent = t.name;

    const meta = document.createElement("span");
    meta.className = "tok-meta";
    meta.textContent = "used " + tokWhen(t.last_used_at);
    meta.title = "Created " + tokWhen(t.created_at);

    const revoke = document.createElement("button");
    revoke.type = "button";
    revoke.className = "tok-revoke";
    revoke.textContent = "Revoke";
    revoke.onclick = () => revokeToken(t.id, t.name);

    li.append(name, meta, revoke);
    list.appendChild(li);
  }
}

function revealToken(plain) {
  const box = document.getElementById("tok-reveal");
  const val = document.getElementById("tok-reveal-val");
  if (!box || !val) return;
  val.textContent = plain;
  box.hidden = false;
}

async function createToken() {
  setTokErr("");
  const name = await askPrompt({
    title: "New access token",
    body: "A name to recognise this token later (e.g. \"macbook\", \"cli\").",
    label: "Name",
    placeholder: "macbook",
    confirmLabel: "Create",
  });
  if (name === null) return; // dismissed
  if (!name) { setTokErr("A name is required."); return; }
  try {
    const res = await api("/api/tokens", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name }),
    });
    revealToken(res.token);
    await loadTokens();
  } catch (e) {
    setTokErr("Create failed: " + e.message);
  }
}

async function revokeToken(id, name) {
  const ok = await askConfirm({
    title: "Revoke token?",
    body: "Any device using \"" + name + "\" will lose access immediately. This cannot be undone.",
    confirmLabel: "Revoke",
    danger: true,
  });
  if (!ok) return;
  setTokErr("");
  try {
    await api("/api/tokens/" + encodeURIComponent(id), { method: "DELETE" });
    await loadTokens();
  } catch (e) {
    setTokErr("Revoke failed: " + e.message);
  }
}

async function logout() {
  // fetch (not api): the handler 303s to /login, which we do explicitly anyway.
  try { await fetch("/logout", { method: "POST" }); } catch (_) {}
  window.location.href = "/login";
}

function initTokens() {
  // The list is (re)loaded whenever the Access-tokens tab is shown — that hook
  // lives in selectSettingsTab (shell.js), so it fires however the tab is
  // reached (button click or openSettings("tokens")), not just on a click here.
  const newBtn = document.getElementById("tok-new");
  if (newBtn) newBtn.onclick = createToken;

  const copyBtn = document.getElementById("tok-copy");
  if (copyBtn) {
    copyBtn.onclick = () => {
      const val = document.getElementById("tok-reveal-val");
      if (val) copyText(val.textContent, (ok) => {
        copyBtn.textContent = ok ? "Copied" : "Copy failed";
        setTimeout(() => { copyBtn.textContent = "Copy"; }, 1500);
      });
    };
  }

  const logoutBtn = document.getElementById("tok-logout");
  if (logoutBtn) logoutBtn.onclick = logout;
}

if (typeof document !== "undefined") {
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", initTokens);
  } else {
    initTokens();
  }
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { tokWhen };
}
