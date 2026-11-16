#!/usr/bin/env bash
# Build culibox and (re)deploy the PRODUCTION systemd --user service.
# The prod service runs this pinned binary (never the dev `air` build), so dev
# work can't affect prod until you deliberately run this.
#
# This builds the WORKING TREE, not a branch you name. That is deliberate — it
# deploys what you can see — but it means the checkout decides the release, so
# the checks below make the checkout impossible to get wrong silently.
set -euo pipefail
REPO="$(cd "$(dirname "$0")/.." && pwd)"
GO="${GO:-$HOME/.local/bin/go}"
BIN="${AB_PROD_BIN:-$HOME/bin/culibox-prod}"
SERVICE="${AB_PROD_SERVICE:-culibox-prod}"
RELEASE_BRANCH="${AB_PROD_BRANCH:-main}"
STAMP="$BIN.deployed-sha"
# What "came up" means after the restart (see wait_until_up). ADDR is where the daemon's HTTP
# listener answers: it must be the bind_addr of the config the prod service runs with
# (docs/CONFIG.md), since this script has no config of its own to read it from; the default is
# the product's default bind. For a wildcard bind (0.0.0.0, ::) use a loopback address and the
# same port. The windows are whole seconds, one poll each.
ADDR="${AB_PROD_ADDR:-127.0.0.1:8899}"
# `serve` takes the upgrade backup (a VACUUM INTO of audit.db, which the backup itself gives up
# on after 2 minutes: internal/upgradebackup/audit.go) before it listens, and exits fail-closed
# if that fails. So "not listening yet" is normal for a while, but cannot outlast the backup:
# 150 s = that 2 minute limit plus margin. The wait ends at once when the unit dies.
LISTEN_WAIT="${AB_PROD_LISTEN_WAIT:-150}"
# After the first answer the unit must stay active, keep answering and keep its process for
# this long: a build that crashes on its first request or its store unlock dies in this window.
STABLE="${AB_PROD_STABLE_SECS:-15}"
case "$LISTEN_WAIT" in '' | *[!0-9]* | 0) echo "refusing: AB_PROD_LISTEN_WAIT must be a whole number of seconds, 1 or more (got '$LISTEN_WAIT')." >&2; exit 1 ;; esac
case "$STABLE" in '' | *[!0-9]*) echo "refusing: AB_PROD_STABLE_SECS must be a whole number of seconds, 0 or more (got '$STABLE')." >&2; exit 1 ;; esac
command -v curl >/dev/null 2>&1 || { echo "refusing: curl is needed to check that the service answers after the restart." >&2; exit 1; }

branch=$(git -C "$REPO" branch --show-current)
head=$(git -C "$REPO" rev-parse --short HEAD)
subject=$(git -C "$REPO" log -1 --format=%s)

# A binary built from uncommitted changes cannot be rebuilt from git, so it is
# not a release — it is a local experiment wearing production's name. No
# override: commit the work or stash it.
if [ -n "$(git -C "$REPO" status --porcelain)" ]; then
  echo "refusing: the working tree is dirty, so this build could not be reproduced." >&2
  git -C "$REPO" status --short >&2
  exit 1
fi

echo "about to build:  ${branch:-<detached HEAD>} @ $head  $subject"

# The checkout is the release. Deploying from a feature branch ships every
# unmerged commit on it, which is how a UI branch nearly reached production
# while the approved change sat on main.
if [ "$branch" != "$RELEASE_BRANCH" ]; then
  if [ "${AB_PROD_ALLOW_BRANCH:-}" != "yes" ]; then
    echo "refusing: production releases come from '$RELEASE_BRANCH', not '${branch:-<detached HEAD>}'." >&2
    echo "  merge first, or set AB_PROD_ALLOW_BRANCH=yes if you mean to ship this branch." >&2
    exit 1
  fi
  echo "WARNING: shipping '${branch:-<detached HEAD>}', not '$RELEASE_BRANCH' (AB_PROD_ALLOW_BRANCH=yes)"
fi

# What this release actually carries. A deploy ships everything merged since the
# last one, not only the change you had in mind, so name them all before asking.
if [ -f "$STAMP" ] && last=$(cat "$STAMP") && git -C "$REPO" cat-file -e "$last^{commit}" 2>/dev/null; then
  if [ "$last" = "$(git -C "$REPO" rev-parse HEAD)" ]; then
    echo "note: this is the commit already deployed — a rebuild, not a new release."
  else
    echo "shipping since the last deploy ($(git -C "$REPO" rev-parse --short "$last")):"
    git -C "$REPO" log --oneline "$last..HEAD" | sed 's/^/  /'
  fi
else
  echo "no record of the last deploy; recent history:"
  git -C "$REPO" log --oneline -5 | sed 's/^/  /'
fi

# Releases are vX.Y.Z tags on main. An untagged build still deploys (and shows
# as v0.1.0+N in the header), but say so: tag first if this is a release.
version=$("$REPO/scripts/version.sh")
case "$version" in
  v[0-9]*.*.*-[0-9]*-g*) echo "note: HEAD is not tagged — shipping $version. Tag a release first: git tag -a vX.Y.Z -m '...'" ;;
  v[0-9]*) ;;
  *) echo "note: no vX.Y.Z tag yet — shipping $version" ;;
esac

# Rollout rule: dev auto-updates (air live-reload); PRODUCTION never auto-deploys.
# A prod release requires explicit human approval — either type 'deploy' at the
# prompt, or pass AB_PROD_CONFIRM=yes (only after a human has approved the
# release). That variable records an approval; it does not waive the checks above.
if [ "${AB_PROD_CONFIRM:-}" != "yes" ]; then
  read -r -p "Deploy to PRODUCTION ($SERVICE)? type 'deploy' to confirm: " ans
  [ "$ans" = "deploy" ] || { echo "aborted — production not deployed."; exit 1; }
fi

PREV="$BIN.prev"
UNVERIFIED="$BIN.unverified"
# The one line that puts $PREV back, printed after every deploy that has one. It swaps
# the binary only: data the newer build already wrote is not undone (#229).
print_rollback() {
  [ -e "$PREV" ] || return 0
  local stamp_step
  if [ -e "$PREV.deployed-sha" ]; then
    stamp_step="cp $(printf %q "$PREV.deployed-sha") $(printf %q "$STAMP")"
    echo "to roll back to $(cut -c1-7 "$PREV.deployed-sha") (the binary only: data this build wrote is not undone), run:"
  else
    stamp_step="rm -f $(printf %q "$STAMP")"
    echo "to roll back to the previous binary (the binary only: data this build wrote is not undone), run:"
  fi
  echo "rollback: cp -p $(printf %q "$PREV") $(printf %q "$BIN.new") && mv -f $(printf %q "$BIN.new") $(printf %q "$BIN") && $stamp_step && rm -f $(printf %q "$UNVERIFIED") && systemctl --user restart $(printf %q "$SERVICE")"
  echo "data: to also undo data changes, restore the backup taken at upgrade: see docs/UPGRADING.md"
}

mkdir -p "$(dirname "$BIN")"
head_sha=$(git -C "$REPO" rev-parse HEAD)
echo "building CuliBox $version → $BIN"
# Into a side file first: a failed build leaves the running binary and $PREV as they were.
"$GO" build -ldflags "-X main.version=$version -X main.commit=$head_sha" -o "$BIN.new" "$REPO/cmd/culibox"

# Keep the binary being replaced as $PREV, the one-step rollback. Only a binary that
# came up is worth keeping: not a rebuild of the deployed commit (that would replace
# the real previous one with a copy of itself), and not one whose restart failed
# ($UNVERIFIED stays until the service is up), so fixing forward after a failed
# deploy still keeps the last build that ran.
last_sha=$(cat "$STAMP" 2>/dev/null || true)
if [ -e "$BIN" ] && [ ! -e "$UNVERIFIED" ] && [ "$last_sha" != "$head_sha" ]; then
  # The old record goes first: stopped between these lines, $PREV is unlabelled
  # rather than labelled with the commit of the binary it no longer holds.
  rm -f "$PREV.deployed-sha"
  cp -p "$BIN" "$PREV"
  if [ -n "$last_sha" ]; then echo "$last_sha" > "$PREV.deployed-sha"; fi
  echo "kept the previous binary → $PREV"
fi
: > "$UNVERIFIED"
# A rename, not a write into $BIN: the running binary cannot be opened for writing.
mv -f "$BIN.new" "$BIN"
# Whether something answers HTTP on $ADDR. Any status counts, a 403 (the Host guard, when the
# bind is not loopback) included: that is still the listener, which is all this asks.
listener_answers() {
  local code
  code=$(curl -s -o /dev/null --noproxy '*' --connect-timeout 2 --max-time 3 -w '%{http_code}' "http://$ADDR/login" 2>/dev/null) || code=000
  [ -n "$code" ] && [ "$code" != 000 ]
}

# "Came up" is not one look a second after the restart: a build can pass that and die on its
# first request (#274), and serve may still be taking its upgrade backup, listening only after.
# So: poll once a second until the listener has answered (at most LISTEN_WAIT seconds, and at
# once if the unit dies, which is serve's fail-closed exit), then keep polling for STABLE
# seconds, each poll needing the unit active, the same main process (a crash-restart between
# polls shows as a new one) and the listener answering. Sets $why when it returns 1.
wait_until_up() {
  local polls=0 first="" pid0 pid
  pid0=$(systemctl --user show -p MainPID --value "$SERVICE" 2>/dev/null || true)
  [ "$pid0" != 0 ] || pid0=""
  why=""
  while :; do
    sleep 1
    polls=$((polls + 1))
    if ! systemctl --user is-active --quiet "$SERVICE"; then
      if [ -n "$first" ]; then
        why="$SERVICE stopped being active ${polls}s after the restart, after it had been answering on $ADDR"
      else
        why="$SERVICE stopped being active ${polls}s after the restart, before it answered on $ADDR"
      fi
      return 1
    fi
    if [ -n "$pid0" ]; then
      pid=$(systemctl --user show -p MainPID --value "$SERVICE" 2>/dev/null || true)
      if [ -n "$pid" ] && [ "$pid" != "$pid0" ]; then
        why="$SERVICE restarted on its own ${polls}s after the restart (main process $pid0, then $pid): it crashed and systemd started it again"
        return 1
      fi
    fi
    if listener_answers; then
      [ -n "$first" ] || first=$polls
      [ $((polls - first)) -lt "$STABLE" ] || return 0
    elif [ -n "$first" ]; then
      why="$ADDR stopped answering ${polls}s after the restart, after it had answered at ${first}s"
      return 1
    elif [ "$polls" -ge "$LISTEN_WAIT" ]; then
      why="$SERVICE is active but $ADDR did not answer within ${LISTEN_WAIT}s (AB_PROD_ADDR must be the bind_addr the service runs with; AB_PROD_LISTEN_WAIT sets the wait)"
      return 1
    fi
  done
}

echo "restarting $SERVICE"
systemctl --user restart "$SERVICE"
echo "waiting for $SERVICE to answer on $ADDR, then to stay up for ${STABLE}s (up to ${LISTEN_WAIT}s for the first answer)"

# Record only after the service is up: a stamp written past a failed restart
# would make the next deploy hide the very commits that broke it, and a build that
# dies inside the window must not become the next deploy's $PREV either (it stays
# $UNVERIFIED). The status block below is for the operator's eyes only.
if ! wait_until_up; then
  echo "deployed the binary, but $SERVICE did not come up: $why. Not recording this commit." >&2
  systemctl --user --no-pager --lines=20 status "$SERVICE" >&2 || true
  print_rollback >&2
  exit 1
fi

# sed, not `head`: head closes the pipe early, systemctl dies of SIGPIPE, and
# under `pipefail` that aborts the script. It cost this script its last two lines
# on the first real deploy — the service was up, the stamp was never written, and
# the run reported failure. sed reads its input to the end, so there is no pipe
# to break.
systemctl --user --no-pager --lines=0 status "$SERVICE" | sed -n '1,5p'
rm -f "$UNVERIFIED"
echo "$head_sha" > "$STAMP"
echo "deployed $version ($head) → $STAMP"
print_rollback
