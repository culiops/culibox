"use strict";

// The session tier badge: which containment a session is actually running under.
//
// This is a security signal, so it is deliberately fail-loud. An unknown or
// missing tier reads as trusted-host, never as boxed: a badge that claims
// containment a session does not have is worse than no badge at all.
//
// Loaded as a classic script, so top-level names are shared globals; they are
// TIER_-prefixed to avoid colliding with another file (two classic scripts
// declaring the same top-level const is a SyntaxError that blanks the page).

const TIER_BOX = "box";
const TIER_HOST = "host";

// Short label for the isolation runtime the daemon detected.
function tierRuntimeLabel(boxRuntime) {
  if (boxRuntime === "sysbox-runc") return "sysbox";
  if (boxRuntime) return boxRuntime;
  return "runc";
}

// tierBadge maps a session + the daemon's detected runtime to what the row shows.
// Returns null when there is nothing worth showing (an exited session already
// carries its own badge, and the tier of a dead session is not actionable).
function tierBadge(session, boxRuntime) {
  if (!session || session.exited) return null;
  if (session.runtime === TIER_BOX) {
    // The row shows the TIER only. The isolation runtime is an instance-wide
    // fact, identical for every session, so spending scarce sidebar width on it
    // just wraps the session name: it lives in the tooltip instead.
    return {
      text: TIER_BOX,
      tone: "neutral",
      title:
        "Contained (" + tierRuntimeLabel(boxRuntime) + "): only this project directory is mounted. " +
        "No host home, no ~/.ssh, no docker socket.",
    };
  }
  return {
    text: TIER_HOST,
    tone: "danger",
    title:
      "NOT contained: this session runs on the host with your user's access. Use only for work that needs the host.",
  };
}

// spawnRuntimeDefault decides which tier the New-session form opens on: the one
// the selected profile actually runs under, as resolved by the server (its own
// setting, else the instance default). The form used to open on "box"
// unconditionally and always send it, which overrode the profile's declared tier
// on every spawn — the profile could never win.
//
// Same fail-loud posture as the badge: anything unrecognised resolves to box,
// never host. Empty when there is no box layer, so the hidden row sends no tier
// at all and the server keeps resolving it.
function spawnRuntimeDefault(profile, boxRuntime) {
  if (!boxRuntime) return "";
  const rt = profile && profile.runtime;
  return rt === TIER_HOST || rt === TIER_BOX ? rt : TIER_BOX;
}

// workspaceOptions renders the workspace picker for a tier. On the boxed tier a
// directory that cannot be contained is disabled and carries its reason: the
// workspace is the box's only writable mount, so this choice IS the containment
// boundary, and offering one that always fails is what made the picker
// confusing in the first place.
function workspaceOptions(workspaces, runtime) {
  const boxed = runtime === TIER_BOX;
  return (workspaces || []).map((w) => {
    const blocked = boxed && !w.boxable;
    return {
      value: w.path,
      label: blocked ? w.label + ": " + (w.reason || "cannot be contained") : w.label,
      disabled: blocked,
    };
  });
}

// defaultWorkspace picks the first entry usable on this tier, so the form never
// opens pre-set to something the server will refuse. Empty when nothing
// qualifies: better a blank the user must fill than a doomed default.
function defaultWorkspace(workspaces, runtime) {
  const usable = workspaceOptions(workspaces, runtime).filter((o) => !o.disabled);
  return usable.length ? usable[0].value : "";
}

// workspaceSelectionForCwd maps a concrete path (a profile's default_cwd) onto
// how the spawn form's picker should show it: the matching option when the tier
// offers a usable one, else the custom path. Returns null for an empty cwd,
// signalling the caller to leave the picker as-is.
function workspaceSelectionForCwd(workspaces, runtime, cwd) {
  cwd = (cwd || "").trim();
  if (!cwd) return null;
  const match = workspaceOptions(workspaces, runtime).find(
    (o) => o.value === cwd && !o.disabled,
  );
  return match ? { value: cwd, custom: "" } : { value: "__custom__", custom: cwd };
}

// unboxableCwdReason is the spawn dialog's own client-side echo of the
// server's refusal rule (box.go's unboxableWorkspace / workspaces.go's
// UnboxableDefaultCwd): home itself, an ancestor of home, or the filesystem
// root can never be a box's one writable mount. It exists because the spawn
// dialog's WORKSPACE field (D5) is now free text fed by a fuzzy project-root
// search, not a <select> whose disabled options already carried this — a path
// the search resolves can be anywhere under $HOME, not just one of the
// depth-1 entries /api/workspaces enumerates with its own boxable/reason.
//
// This is a HEURISTIC, not the safety boundary: it does not resolve symlinks
// (impure — the server does, via filepath.EvalSymlinks) and it does not know
// about any workspace the caller already has richer data for (see
// updateRuntimeWarning, which checks workspacesCache first and only falls
// back to this for a path outside that list). The real gate is still
// box.ErrUnboxableWorkspace at spawn time — this only lets the dialog warn
// before the round trip.
function unboxableCwdReason(cwd, home) {
  const c = (cwd || "").trim();
  if (!c) return null;
  if (c === "/") return "it is the filesystem root";
  if (!home) return null;
  if (c === home) return "a box here would mount your whole home, including ~/.ssh and your secrets";
  if (home.indexOf(c + "/") === 0) return "it contains your home directory";
  return null;
}

// pfCwdValue reads the profile editor's working-dir controls into a cwd string:
// "" (no default) when the picker is on the "(no default)" option, the typed
// custom path when on "Custom path…", else the selected workspace root.
function pfCwdValue(selectValue, customText) {
  if (selectValue === "") return "";
  if (selectValue === "__custom__") return (customText || "").trim();
  return selectValue;
}

// profileFitsAsSeg decides whether the spawn dialog's Profile control (D5)
// renders as a segmented row or falls back to the plain <select>: a handful
// of names reads as a row of buttons, but past PROFILE_SEG_MAX the same row
// either overflows the dialog or wraps into an ugly multi-line block — the
// product-owner call was "segmented + dropdown fallback", not "segmented no
// matter how many". Mirrors the profile editor's own <=4 seg threshold for
// enum options (renderPfOptions).
const PROFILE_SEG_MAX = 4;
function profileFitsAsSeg(count) {
  return count > 0 && count <= PROFILE_SEG_MAX;
}

// shellSpawnPlan decides what the per-pane "+sh" button does. A quick shell is
// defined as "a shell beside this session", so it inherits the neighbour's
// directory AND its tier: beside a boxed agent it joins the same box, beside an
// uncontained session it is uncontained too. Inheriting only the directory made
// a boxed shell inherit an uncontainable one and fail.
//
// With no neighbour there is no context to inherit, so the quick path has no
// meaning and the caller should open the form instead.
function shellSpawnPlan(neighbour) {
  if (!neighbour || !neighbour.cwd) return { mode: "form" };
  return { mode: "spawn", cwd: neighbour.cwd, runtime: neighbour.runtime || "" };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    tierBadge, tierRuntimeLabel, spawnRuntimeDefault,
    workspaceOptions, defaultWorkspace, workspaceSelectionForCwd, unboxableCwdReason,
    pfCwdValue, shellSpawnPlan, TIER_BOX, TIER_HOST,
    profileFitsAsSeg, PROFILE_SEG_MAX,
  };
}
