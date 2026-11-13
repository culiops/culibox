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
BIN="${AB_PROD_BIN:-$HOME/bin/agent-box-prod}"
SERVICE="${AB_PROD_SERVICE:-agent-box-prod}"
RELEASE_BRANCH="${AB_PROD_BRANCH:-main}"
STAMP="$BIN.deployed-sha"

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

mkdir -p "$(dirname "$BIN")"
echo "building CuliBox $version → $BIN"
"$GO" build -ldflags "-X main.version=$version -X main.commit=$(git -C "$REPO" rev-parse HEAD)" -o "$BIN" "$REPO/cmd/culibox"
echo "restarting $SERVICE"
systemctl --user restart "$SERVICE"
sleep 1

# Record only after the service is up: a stamp written past a failed restart
# would make the next deploy hide the very commits that broke it. `is-active` is
# that check — the status block below is for the operator's eyes only.
if ! systemctl --user is-active --quiet "$SERVICE"; then
  echo "deployed the binary, but $SERVICE is not active — not recording this commit." >&2
  systemctl --user --no-pager --lines=20 status "$SERVICE" >&2 || true
  exit 1
fi

# sed, not `head`: head closes the pipe early, systemctl dies of SIGPIPE, and
# under `pipefail` that aborts the script. It cost this script its last two lines
# on the first real deploy — the service was up, the stamp was never written, and
# the run reported failure. sed reads its input to the end, so there is no pipe
# to break.
systemctl --user --no-pager --lines=0 status "$SERVICE" | sed -n '1,5p'
git -C "$REPO" rev-parse HEAD > "$STAMP"
echo "deployed $version ($head) → $STAMP"
