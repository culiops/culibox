"use strict";

// The optional chime for Blocked (UI plan 7 #177, call 9): a short two-tone blip, played once per NEW blocked
// cause whose session is on no visible pane, or at any time while the tab is unfocused. Off by default
// (readPrefs().chime, the Alerts switch). The decision is notifymodel.js's chimeStep; this file is the thin
// WebAudio shell around it.
//
// Browsers keep an AudioContext suspended until a user gesture, so the context is created or resumed by a
// one-time pointerdown/keydown listener that is armed only while the pref is on and no context is running.
// Without a running context a play is a silent no-op (the blip is lost, never an error). With the pref off no
// context is ever created. Lint's globals have no AudioContext, so it is reached through window.

let chimeCtx = null;
let chimePrev = null;
let chimeArmed = false;

function chimeCtor() {
  return window.AudioContext || window.webkitAudioContext || null;
}

function chimeGesture() {
  chimeDisarm();
  try {
    const C = chimeCtor();
    if (!C) return;
    if (!chimeCtx) chimeCtx = new C();
    if (chimeCtx.state !== "running") chimeCtx.resume();
  } catch (_) { /* no audio here: stays silent */ }
}

function chimeArm() {
  if (chimeArmed) return;
  chimeArmed = true;
  document.addEventListener("pointerdown", chimeGesture, true);
  document.addEventListener("keydown", chimeGesture, true);
}

function chimeDisarm() {
  if (!chimeArmed) return;
  chimeArmed = false;
  document.removeEventListener("pointerdown", chimeGesture, true);
  document.removeEventListener("keydown", chimeGesture, true);
}

// chimeSync: keep the gesture listener in step with the pref. Cheap; called on every session poll.
function chimeSync(on) {
  if (!on || !chimeCtor() || (chimeCtx && chimeCtx.state === "running")) { chimeDisarm(); return; }
  chimeArm();
}

// chimePlay: two tones, about 180 ms in all. false (and nothing happens) unless a context is running.
function chimePlay() {
  if (!chimeCtx || chimeCtx.state !== "running") return false;
  try {
    const t = chimeCtx.currentTime;
    const tones = [[880, 0, 0.08], [1175, 0.09, 0.18]];
    for (const [hz, from, to] of tones) {
      const osc = chimeCtx.createOscillator();
      const gain = chimeCtx.createGain();
      osc.type = "sine";
      osc.frequency.setValueAtTime(hz, t + from);
      gain.gain.setValueAtTime(0.0001, t + from);
      gain.gain.linearRampToValueAtTime(0.12, t + from + 0.01);
      gain.gain.exponentialRampToValueAtTime(0.0001, t + to);
      osc.connect(gain);
      gain.connect(chimeCtx.destination);
      osc.start(t + from);
      osc.stop(t + to);
    }
    return true;
  } catch (_) { return false; }
}

// chimeTransitions: one successful session poll. Always advances the state, so switching the chime on later
// never replays old causes.
function chimeTransitions() {
  const on = !!readPrefs().chime;
  chimeSync(on);
  const focused = typeof document.hasFocus === "function" ? document.hasFocus() : true;
  const st = chimeStep(chimePrev, blockedCauses(lastSessions), visibleSessionIds(), { now: Date.now(), focused, on });
  chimePrev = st.next;
  if (st.play) chimePlay();
}
