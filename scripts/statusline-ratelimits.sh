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
#   {"updated_at":<epoch>,"five_hour":{"used_percentage":N,"resets_at":E},"seven_day":{...}}
# A statusLine command must also PRINT the status line itself; this one prints a
# tiny summary so wiring it doesn't blank your status line.
set -euo pipefail

INPUT=""
while IFS= read -r -t 5 line || [ -n "$line" ]; do
  INPUT="${INPUT}${line}
"
done
[ -z "$INPUT" ] && exit 0

printf '%s' "$INPUT" | python3 -c '
import sys, json, time, os
data = json.load(sys.stdin)
rl = data.get("rate_limits") or {}
out = {"source": "agent-box", "updated_at": int(time.time())}
line = []
for key in ("five_hour", "seven_day"):
    w = rl.get(key)
    if not w:
        continue
    out[key] = {"used_percentage": w.get("used_percentage", 0), "resets_at": w.get("resets_at", 0)}
    line.append("%s %s%%" % ("5h" if key == "five_hour" else "wk", w.get("used_percentage", 0)))
cfg = os.environ.get("CLAUDE_CONFIG_DIR", os.path.join(os.path.expanduser("~"), ".claude"))
try:
    with open(os.path.join(cfg, "agent-box-rate-limits.json"), "w") as f:
        json.dump(out, f)
except OSError:
    pass
# Print the status line so wiring this does not blank it.
print(" · ".join(line))
' 2>/dev/null || true
