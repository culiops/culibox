#!/usr/bin/env bash
# agent-box: optional statusLine hook that harvests Claude Code's account rate
# limits to a file agent-box reads (see config `rate_limits_file`).
#
# WHY: the 5-hour / weekly usage limits are only delivered to Claude Code's
# statusLine command (as JSON on stdin). agent-box is not on the API path, so a
# hook is the only way to surface them. This is OPT-IN and self-contained — a
# drop-in for users who don't already run a tool (e.g. abtop) that writes the file.
#
# WIRE IT (only if you have no other statusLine): in ~/.claude/settings.json
#   { "statusLine": { "type": "command", "command": "/abs/path/statusline-ratelimits.sh" } }
# DO NOT clobber an existing statusLine — Claude Code runs exactly one. If you
# already have one (abtop, a prompt, etc.), keep it: agent-box will read whatever
# rate-limits file that tool produces; point `rate_limits_file` at it instead.
#
# OUTPUT: ${CLAUDE_CONFIG_DIR:-$HOME/.claude}/agent-box-rate-limits.json
#   {"updated_at":<epoch>,"five_hour":{"used_percentage":N,"resets_at":E},"seven_day":{...},
#    "model":"<id>","model_family":"opus","by_model":{"opus":{"updated_at":..,"five_hour":..,"seven_day":..}}}
# The file is replaced atomically (temp file + rename), and nothing is written
# while Claude Code reports no rate limits (before a session's first reply).
# The top-level windows are the newest write; by_model keeps one reading per
# model family (at most 8), because sessions on different models can report
# different quota numbers for one token.
# THEN point agent-box at it — the default rate_limits_file is abtop's path, not
# this one — in the agent-box config:
#   { "rate_limits_file": "/home/you/.claude/agent-box-rate-limits.json" }
#
# FOOTER: the harvest is a side effect; what this script PRINTS is the footer.
# agent-box keeps every account's footer identical to the default account's:
#   1. if $AGENTBOX_STATUSLINE_DELEGATE_FILE (a box: the host's statusLine command,
#      shipped in the config bundle) or else $CLAUDE_CONFIG_DIR/.agentbox-statusline-delegate
#      exists (agent-box writes it for accounts that inherit personal config, holding
#      the default account's statusLine command), run that command with the same stdin and print its output;
#   2. otherwise (or if it fails / prints nothing) print the default format itself:
#        <model> | ~/<cwd basename> | effort <level>
# Must never error or print a traceback: a broken statusLine blanks the footer.
set -euo pipefail

INPUT=""
while IFS= read -r -t 5 line || [ -n "$line" ]; do
  INPUT="${INPUT}${line}
"
done
[ -z "$INPUT" ] && exit 0

# Harvest rate limits (side effect) and compute the fallback footer line.
FALLBACK=$(printf '%s' "$INPUT" | python3 -c '
import sys, json, time, os
try:
    data = json.load(sys.stdin)
except Exception:
    sys.exit(0)
if not isinstance(data, dict):
    sys.exit(0)
rl = data.get("rate_limits")
rl = rl if isinstance(rl, dict) else {}
out = {"source": "agent-box", "updated_at": int(time.time())}
for key in ("five_hour", "seven_day"):
    w = rl.get(key)
    if not isinstance(w, dict) or not w:
        continue
    out[key] = {"used_percentage": w.get("used_percentage", 0), "resets_at": w.get("resets_at", 0)}
# rate_limits is absent until a session'"'"'s first API reply. A snapshot with no
# window would wipe the last good one, so write nothing (the host script does the same).
if "five_hour" in out or "seven_day" in out:
    cfg = os.environ.get("CLAUDE_CONFIG_DIR", os.path.join(os.path.expanduser("~"), ".claude"))
    path = os.path.join(cfg, "agent-box-rate-limits.json")
    # The quota may be reported per model bucket (a haiku and an opus session on
    # one token showed different weekly numbers), so also keep one reading per
    # model family in by_model. The top-level fields stay the newest write, for
    # readers that predate by_model.
    mo = data.get("model")
    mid = mo if isinstance(mo, str) else (mo.get("id") if isinstance(mo, dict) else "")
    mid = mid if isinstance(mid, str) else ""
    fam = ""
    for known in ("opus", "sonnet", "haiku"):
        if known in mid.lower():
            fam = known
            break
    else:
        fam = "".join(c for c in mid.lower() if c.isalnum() or c in "._-")[:40]
    # Read-modify-write without a lock: two sessions finishing at the same moment
    # can each start from the same old file, so one bucket may be lost until
    # that session renders again. Accepted: it self-heals on its next render.
    buckets = {}
    try:
        with open(path) as f:
            old = json.load(f)
        if isinstance(old.get("by_model"), dict):
            buckets = {k: v for k, v in old["by_model"].items() if isinstance(v, dict)}
    except Exception:
        pass
    if fam:
        out["model"] = mid[:80]
        out["model_family"] = fam
        b = {"updated_at": out["updated_at"]}
        for key in ("five_hour", "seven_day"):
            if key in out:
                b[key] = out[key]
        buckets[fam] = b
        # Bounded: keep this write plus the 7 most recently updated others.
        def ts(k):
            t = buckets[k].get("updated_at", 0)
            return t if isinstance(t, (int, float)) and not isinstance(t, bool) else 0
        keep = sorted((k for k in buckets if k != fam), key=ts, reverse=True)[:7] + [fam]
        buckets = {k: buckets[k] for k in keep}
    if buckets:
        out["by_model"] = buckets
    # Temp file in the same directory + rename: a reader never sees a half-written
    # or empty file. Owner-only (0600): it is account usage. The name carries a
    # random suffix because boxes sharing a config dir can have the same pid; the
    # file is removed only if THIS process created it, never on an EEXIST.
    tmp = "%s.%s.tmp" % (path, os.urandom(4).hex())
    try:
        fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    except OSError:
        fd = None
    if fd is not None:
        try:
            with os.fdopen(fd, "w") as f:
                json.dump(out, f)
            os.replace(tmp, path)
        except OSError:
            try:
                os.unlink(tmp)
            except OSError:
                pass
def obj(v):
    return v if isinstance(v, dict) else {}
def text(v):
    return v if isinstance(v, str) else ""
m = data.get("model")
model = (m if isinstance(m, str) else text(obj(m).get("display_name"))) or "unknown"
cwd = text(obj(data.get("workspace")).get("current_dir")) or text(data.get("cwd"))
base = os.path.basename(cwd.rstrip("/")) or cwd
line = "%s | ~/%s" % (model, base)
e = obj(data.get("effort")).get("level")
effort = e if isinstance(e, (str, int)) and not isinstance(e, bool) else ""
if effort != "":
    line += " | effort %s" % effort
print(line)
' 2>/dev/null || true)

CFG="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
# AGENTBOX_STATUSLINE_DELEGATE_FILE (set by a box's bundled settings, where there
# is no writable config dir to hold a delegate) wins over the per-account file.
DELEGATE="${AGENTBOX_STATUSLINE_DELEGATE_FILE:-$CFG/.agentbox-statusline-delegate}"
# AGENTBOX_STATUSLINE_DELEGATING catches indirect recursion; the name check
# catches a delegate that points straight back at this script.
if [ -s "$DELEGATE" ] && [ -z "${AGENTBOX_STATUSLINE_DELEGATING:-}" ]; then
  CMD=$(cat "$DELEGATE" 2>/dev/null || true)
  case "$CMD" in
    "") ;;
    *statusline-ratelimits.sh*) ;;
    *)
      # Keep whatever it printed even on a non-zero exit (the default script's
      # last step can fail after the footer line is already out).
      # In a box (AGENTBOX_STATUSLINE_DELEGATE_FILE is set, and only the bundled
      # command sets it) the host's command may hang on something the box lacks;
      # bound it so it cannot block the footer or pile up against the pid limit.
      # Host accounts keep the unbounded call. The harvest above already ran.
      if [ -n "${AGENTBOX_STATUSLINE_DELEGATE_FILE:-}" ] && command -v timeout >/dev/null 2>&1; then
        OUT=$(printf '%s' "$INPUT" | AGENTBOX_STATUSLINE_DELEGATING=1 timeout 5 sh -c "$CMD" 2>/dev/null) || true
      else
        OUT=$(printf '%s' "$INPUT" | AGENTBOX_STATUSLINE_DELEGATING=1 sh -c "$CMD" 2>/dev/null) || true
      fi
      if [ -n "$OUT" ]; then
        printf '%s\n' "$OUT"
        exit 0
      fi
      ;;
  esac
fi
[ -n "$FALLBACK" ] && printf '%s\n' "$FALLBACK"
exit 0
