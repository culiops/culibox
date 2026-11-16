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
# same port. It must be a numeric address ([::1] for IPv6): a hostname is never equal to what
# `ss -n` prints, so it fails closed. The windows are whole seconds, one poll each.
# Something answering there is not enough (the dev instance uses the default port on the same
# host): the listening socket must also belong to the service's own cgroup, or the probe does
# not count (port_owned_by_unit).
ADDR="${AB_PROD_ADDR:-127.0.0.1:8899}"
# `serve` takes the upgrade backup (a VACUUM INTO of audit.db, which the backup itself gives up
# on after 2 minutes: internal/upgradebackup/audit.go) before it listens, and exits fail-closed
# if that fails. So "not listening yet" is normal for a while, but cannot outlast the backup:
# 150 s = that 2 minute limit plus margin. The wait ends at once when the unit dies.
LISTEN_WAIT="${AB_PROD_LISTEN_WAIT:-150}"
# After the first answer the unit must stay active, keep answering and keep its process for
# this long. It catches a build that dies a few seconds after it started listening (startup work
# that fails late, a crash loop); the probe only fetches /login, so a crash that needs a later
# request or a login is not in its reach.
STABLE="${AB_PROD_STABLE_SECS:-15}"
case "$LISTEN_WAIT" in '' | *[!0-9]* | 0) echo "refusing: AB_PROD_LISTEN_WAIT must be a whole number of seconds, 1 or more (got '$LISTEN_WAIT')." >&2; exit 1 ;; esac
case "$STABLE" in '' | *[!0-9]* | 0) echo "refusing: AB_PROD_STABLE_SECS must be a whole number of seconds, 1 or more (got '$STABLE'): 0 would accept the first answer with no watch at all." >&2; exit 1 ;; esac
for tool in curl ss; do
  command -v "$tool" >/dev/null 2>&1 || { echo "refusing: $tool is needed to check that the service itself answers after the restart." >&2; exit 1; }
done
# The ownership check reads /proc/<pid>/cgroup as cgroup v2 (`0::<path>`). On a pure cgroup v1
# host there is no such line and it could never match, so the deploy would wait out the whole
# window and blame another process: refuse here instead. A hybrid host does have a `0::` line and
# passes this test; there the check still fails closed, with the "not on that address" message.
grep -q '^0::' /proc/self/cgroup 2>/dev/null || { echo "refusing: this host is not on cgroup v2 (no '0::' line in /proc/self/cgroup), so the check that the service itself answers cannot work." >&2; exit 1; }

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
# Whether a process in $SERVICE's cgroup holds a listening socket on $ADDR itself: its port AND
# its host, because a second socket on the same port at another address (127.0.0.2, [::1], a LAN
# IP) is not what curl reaches on $ADDR (the dev instance may be). The cgroup, not MainPID: an
# ExecStart wrapper makes the daemon a child of the main process. The port from $ADDR, not the
# process's sockets: serve also listens for the egress proxy and the broker.
# /proc/<pid>/cgroup reads `0::<path>` on cgroup v2; a child cgroup of the unit's counts too.
# A socket's local address in `ss -n` output is `host:port`, a v6 host in brackets, possibly
# `host%iface`; it covers $ADDR when it is that host, `*` (dual-stack any), 0.0.0.0 for a v4
# $ADDR or [::] for a v6 one.
port_owned_by_unit() {
  local cg want laddr h procs pid line
  cg=$(systemctl --user show -p ControlGroup --value "$SERVICE" 2>/dev/null || true)
  [ -n "$cg" ] || return 1
  want=${ADDR%:*}
  while read -r _ _ _ laddr _ procs; do
    h=${laddr%:*}
    # ss puts the interface after the bracket of a v6 host: `[::1]%lo:8899`, `127.0.0.1%lo:8899`.
    h=${h%%%*}
    case "$h" in
      "$want" | '*') ;;
      0.0.0.0) case "$want" in *:*) continue ;; esac ;;
      '[::]') case "$want" in *:*) ;; *) continue ;; esac ;;
      *) continue ;;
    esac
    for pid in $(printf '%s\n' "$procs" | grep -o 'pid=[0-9]*' | cut -d= -f2 | sort -u || true); do
      while read -r line; do
        case "$line" in "0::$cg" | "0::$cg"/*) return 0 ;; esac
      done 2>/dev/null <"/proc/$pid/cgroup"
    done
  done < <(ss -Hltnp "sport = :${ADDR##*:}" 2>/dev/null)
  return 1
}

# One look at $ADDR. 0: something answers HTTP and the socket is the service's; 1: nothing
# answers; 2: something answers but the service holds no socket on $ADDR (the dev instance on
# the default port, say), which says nothing about this build. Any HTTP status counts as an
# answer, a 403 from the Host guard (a non-loopback bind) included.
listener_state() {
  local code
  code=$(curl -q -s -o /dev/null --noproxy '*' --connect-timeout 2 --max-time 3 -w '%{http_code}' "http://$ADDR/login" 2>/dev/null) || code=000
  { [ -n "$code" ] && [ "$code" != 000 ]; } || return 1
  port_owned_by_unit || return 2
}

# "Came up" is not one look a second after the restart: a build can pass that and die on its
# first request (#274), and serve may still be taking its upgrade backup, listening only after.
# So: poll once a second until the listener has answered (at most LISTEN_WAIT polls, and at
# once if the unit dies, which is serve's fail-closed exit), then keep polling for STABLE more
# polls, each needing the unit active, the same main process (a crash-restart between polls
# shows as a new one) and the listener answering. Sets $why when it returns 1.
wait_until_up() {
  local polls=0 first="" pid0="" pid st
  why=""
  while :; do
    sleep 1
    polls=$((polls + 1))
    if ! systemctl --user is-active --quiet "$SERVICE"; then
      if [ -n "$first" ]; then
        why="$SERVICE stopped being active at poll $polls, after it had been answering on $ADDR"
      else
        why="$SERVICE stopped being active at poll $polls, before it answered on $ADDR"
      fi
      return 1
    fi
    # The process to watch is the first real one: MainPID reads 0 while systemd is between a
    # crash and its automatic restart, so the first read may not be one.
    pid=$(systemctl --user show -p MainPID --value "$SERVICE" 2>/dev/null || true)
    if [ -n "$pid" ] && [ "$pid" != 0 ]; then
      if [ -z "$pid0" ]; then
        pid0=$pid
      elif [ "$pid" != "$pid0" ]; then
        why="$SERVICE restarted on its own by poll $polls (main process $pid0, then $pid): it crashed and systemd started it again"
        return 1
      fi
    fi
    st=0
    listener_state || st=$?
    if [ "$st" -eq 0 ]; then
      [ -n "$first" ] || first=$polls
      [ $((polls - first)) -lt "$STABLE" ] || return 0
    elif [ -n "$first" ]; then
      why="$ADDR stopped answering from $SERVICE at poll $polls, after it had answered at poll $first"
      return 1
    elif [ "$polls" -ge "$LISTEN_WAIT" ]; then
      if [ "$st" -eq 2 ]; then
        why="$ADDR answers, but $SERVICE is not on that address: it holds no listening socket there within ${LISTEN_WAIT} polls, so what answers is another process (the dev instance, if AB_PROD_ADDR is left at the default). AB_PROD_ADDR must be a numeric address, the bind_addr the service runs with"
      else
        why="$SERVICE is active but $ADDR did not answer within ${LISTEN_WAIT} polls (AB_PROD_ADDR must be the bind_addr the service runs with; AB_PROD_LISTEN_WAIT sets the wait)"
      fi
      return 1
    fi
  done
}

echo "restarting $SERVICE"
systemctl --user restart "$SERVICE"
echo "waiting for $SERVICE itself to answer on $ADDR (up to ${LISTEN_WAIT} polls, one a second), then to stay up for ${STABLE} more polls"

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
