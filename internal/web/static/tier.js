"use strict";

// The session tier badge: which containment a session is actually running under.
//
// This is a security signal, so it is deliberately fail-loud. An unknown or
// missing tier reads as trusted-host, never as boxed — a badge that claims
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
    // just wraps the session name — it lives in the tooltip instead.
    return {
      text: "boxed",
      tone: "neutral",
      title:
        "Contained (" + tierRuntimeLabel(boxRuntime) + "): only this project directory is mounted. " +
        "No host home, no ~/.ssh, no docker socket.",
    };
  }
  return {
    text: "trusted-host",
    tone: "danger",
    title:
      "NOT contained: this session runs on the host with your user's access. Use only for work that needs the host.",
  };
}

// spawnMenuEntries describes the ▾ menu: every profile boxed, then — when
// containment is actually available — the same profiles again as uncontained
// trusted-host spawns.
//
// Each entry carries its OWN agent. A single generic "run on trusted host" item
// had to fall back to the default profile, so the one thing the escape hatch
// exists for (running *claude* with host access to aws/ssh) was the one thing it
// could not do; you got whichever profile happened to be first.
function spawnMenuEntries(profiles, boxRuntime) {
  const list = (profiles || []).filter((p) => p && p.name);
  if (!list.length) return [];

  const entries = list.map((p) => ({
    kind: "item",
    agent: p.name,
    label: p.name,
    runtime: TIER_BOX,
    tone: "neutral",
  }));
  // No containment → every session is already host tier, so a "trusted host"
  // section would be a distinction without a difference.
  if (!boxRuntime) return entries;

  entries.push({ kind: "heading", text: "Run on trusted host (uncontained)" });
  for (const p of list) {
    entries.push({
      kind: "item",
      agent: p.name,
      label: p.name,
      runtime: TIER_HOST,
      tone: "danger",
      title:
        "Spawn " + p.name + " OUTSIDE the box: full access to your host — your files, " +
        "keys and network. Use only for work that needs the host.",
    });
  }
  return entries;
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
      label: blocked ? w.label + " — " + (w.reason || "cannot be contained") : w.label,
      disabled: blocked,
    };
  });
}

// defaultWorkspace picks the first entry usable on this tier, so the form never
// opens pre-set to something the server will refuse. Empty when nothing
// qualifies — better a blank the user must fill than a doomed default.
function defaultWorkspace(workspaces, runtime) {
  const usable = workspaceOptions(workspaces, runtime).filter((o) => !o.disabled);
  return usable.length ? usable[0].value : "";
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    tierBadge, tierRuntimeLabel, spawnMenuEntries,
    workspaceOptions, defaultWorkspace, TIER_BOX, TIER_HOST,
  };
}
