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

// Which token's row, if any, is showing its in-row revoke confirm.
let armedToken = "";

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
  // A refresh can never leave a row armed against a token that is gone —
  // revoked from another tab, or the "gone" case of the list being empty.
  if (armedToken && !(items || []).some((t) => t.id === armedToken)) armedToken = "";
  list.textContent = "";
  if (!items || items.length === 0) {
    const li = document.createElement("li");
    li.className = "tokempty";
    li.textContent = "No tokens yet.";
    list.appendChild(li);
    return;
  }
  for (const t of items) {
    const li = document.createElement("li");
    li.className = "tokrow" + (t.id === armedToken ? " arm" : "");

    const name = document.createElement("span");
    name.className = "nm";
    name.textContent = t.name;

    const meta = document.createElement("span");
    meta.className = "meta";
    if (t.id === armedToken) {
      meta.textContent = "Revoke? That device is signed out on its next request. This cannot be undone.";
    } else {
      meta.textContent = "used " + tokWhen(t.last_used_at);
      meta.title = "Created " + tokWhen(t.created_at);
    }

    li.append(name, meta);
    if (t.id === armedToken) {
      const cancel = document.createElement("button");
      cancel.type = "button"; cancel.className = "btn"; cancel.textContent = "Cancel";
      cancel.onclick = () => { armedToken = ""; loadTokens(); };
      const go = document.createElement("button");
      // No ellipsis: this IS the question, not the promise of one.
      go.type = "button"; go.className = "btn dgr"; go.textContent = "Revoke";
      go.onclick = () => revokeToken(t.id);
      li.append(cancel, go);
    } else {
      const arm = document.createElement("button");
      arm.type = "button"; arm.className = "btn dgr"; arm.textContent = "Revoke…";
      arm.setAttribute("aria-label", "Revoke " + t.name);
      arm.onclick = () => { armedToken = t.id; loadTokens(); };
      li.append(arm);
    }
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
    // The list refreshes first, so the new row is on screen; the reveal is the
    // last thing painted and the first thing the next refresh takes away.
    await loadTokens();
    revealToken(res.token);
  } catch (e) {
    setTokErr("Create failed: " + e.message);
  }
}

// The confirm lives in the row rather than in the shared dialog. That is a
// deliberate divergence from every other destructive action here, and it earns
// it: a modal that names a token is a worse identification of WHICH token than
// the row it is drawn in. It also means this path is not subject to the shared
// dialog's one-question-at-a-time rule.
async function revokeToken(id) {
  setTokErr("");
  armedToken = "";
  try {
    await api("/api/tokens/" + encodeURIComponent(id), { method: "DELETE" });
    await loadTokens();
  } catch (e) {
    setTokErr("Revoke failed: " + e.message);
  }
}

async function logout() {
  const ok = await askConfirm({
    title: "Sign out of this box",
    body: "This browser will need the password again. Your running agents keep " +
          "running, and any access tokens you have created keep working — revoke " +
          "those individually if you want them gone.",
    confirmLabel: "Sign out",
    danger: true,
  });
  if (!ok) return;
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
  // The desktop Settings nav's foot item (hidden on a phone): the same sign-out, the same confirm.
  const navLogout = document.getElementById("set-signout");
  if (navLogout) navLogout.onclick = logout;

  const done = document.getElementById("tok-done");
  if (done) done.onclick = () => {
    const box = document.getElementById("tok-reveal");
    if (box) box.hidden = true;
  };
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
