"use strict";

// The environment allowlist, as rows.
//
// One table sits over two differently-shaped things. A literal pair is stored in
// the profile and shown in full. A secret is a NAME only: the value is resolved
// when the agent starts, from wherever the box is configured to look, and never
// touches the profile. That is why a secret row has no value box — an editable
// box would say the value lives here, and the whole point is that it does not.

const NAME_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;

function envRows(cfg) {
  const c = cfg || {};
  const rows = Object.entries(c.env || {}).map(([key, value]) => ({ key, value, kind: "literal" }));
  for (const key of c.secrets || []) rows.push({ key, value: "", kind: "secret" });
  return rows;
}

function rowsToConfig(rows) {
  const env = {};
  const secrets = [];
  for (const r of rows || []) {
    const key = (r.key || "").trim();
    if (!key) continue;
    if (r.kind === "secret") secrets.push(key);
    else env[key] = (r.value || "").trim();
  }
  return { env, secrets };
}

// rowError returns the first problem as a sentence, or "" when the list is
// clean. It refuses what the server refuses rather than a stricter rule of its
// own: a name that is not a shell identifier, and one name claimed twice —
// including once as a literal and once as a secret, which the server rejects
// because the resolved value would be shadowed by the literal at spawn time.
function rowError(rows) {
  const seen = new Set();
  for (const r of rows || []) {
    const key = (r.key || "").trim();
    if (!key) {
      if ((r.value || "").trim()) return "A variable needs a name.";
      continue;
    }
    if (!NAME_RE.test(key)) {
      return 'Not a valid variable name: "' + key + '". Letters, digits and underscore, not starting with a digit.';
    }
    if (seen.has(key)) return 'Listed twice: "' + key + '".';
    seen.add(key);
  }
  return "";
}

// renderEnvRows paints the rows into a container. It rebuilds rather than
// patching: the list is small, and a rebuild cannot leave a stale row behind.
// The remove control is a real button — in the design it is a bare glyph a
// keyboard cannot reach and a finger cannot hit, and it deletes a line.
function renderEnvRows(container, rows, onChange) {
  const mk = (r, i) => {
    const row = document.createElement("div");
    row.className = "kvrow" + (r.kind === "secret" ? " ref" : "");
    const k = document.createElement("input");
    k.className = "fld k"; k.value = r.key; k.placeholder = "NAME";
    k.setAttribute("aria-label", "Variable name");
    k.oninput = () => { rows[i].key = k.value; onChange(); };
    let v;
    if (r.kind === "secret") {
      v = document.createElement("span");
      v.className = "v ref";
      v.textContent = "→ by reference";
      v.title = "The value is resolved when the agent starts and is not stored in this profile.";
    } else {
      v = document.createElement("input");
      v.className = "fld v"; v.value = r.value; v.placeholder = "value";
      v.setAttribute("aria-label", "Value");
      v.oninput = () => { rows[i].value = v.value; onChange(); };
    }
    const rm = document.createElement("button");
    rm.type = "button"; rm.className = "btn gh rm"; rm.textContent = "×";
    rm.setAttribute("aria-label", "Remove " + (r.key || "this variable"));
    rm.onclick = () => { rows.splice(i, 1); renderEnvRows(container, rows, onChange); onChange(); };
    row.append(k, v, rm);
    return row;
  };
  if (!rows.length) {
    const p = document.createElement("p");
    p.className = "kvempty";
    p.textContent = "No variables. The agent gets the base environment for its profile.";
    container.replaceChildren(p);
    return;
  }
  container.replaceChildren(...rows.map(mk));
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { envRows, rowsToConfig, rowError, renderEnvRows };
}
