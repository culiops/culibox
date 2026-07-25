#!/usr/bin/env bash
# Build agent-console and (re)deploy the PRODUCTION systemd --user service.
# The prod service runs this pinned binary (never the dev `air` build), so dev
# work can't affect prod until you deliberately run this.
set -euo pipefail
REPO="$(cd "$(dirname "$0")/.." && pwd)"
GO="${GO:-$HOME/.local/bin/go}"
BIN="${AC_PROD_BIN:-$HOME/bin/agent-console-prod}"
SERVICE="${AC_PROD_SERVICE:-agent-console-prod}"

# Rollout rule: dev auto-updates (air live-reload); PRODUCTION never auto-deploys.
# A prod release requires explicit human approval — either type 'deploy' at the
# prompt, or pass AC_PROD_CONFIRM=yes (only after a human has approved the release).
if [ "${AC_PROD_CONFIRM:-}" != "yes" ]; then
  read -r -p "Deploy to PRODUCTION ($SERVICE)? type 'deploy' to confirm: " ans
  [ "$ans" = "deploy" ] || { echo "aborted — production not deployed."; exit 1; }
fi

mkdir -p "$(dirname "$BIN")"
echo "building → $BIN"
"$GO" build -o "$BIN" "$REPO/cmd/agent-console"
echo "restarting $SERVICE"
systemctl --user restart "$SERVICE"
sleep 1
systemctl --user --no-pager --lines=0 status "$SERVICE" | head -5
