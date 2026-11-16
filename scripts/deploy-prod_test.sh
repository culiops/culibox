#!/usr/bin/env bash
# Offline test for scripts/deploy-prod.sh: run it with `bash scripts/deploy-prod_test.sh`.
#
# The script runs from a copy inside a throwaway fixture repo (it deploys the checkout
# it lives in), with every external effect stubbed:
#   go          $GO points at a stub that "builds" a file holding the commit it was given
#   systemctl   a PATH stub; STUB_RESTART_FAIL=1 makes the service not come up;
#               STUB_ACTIVE_UNTIL=N keeps it active for N polls, then it dies;
#               STUB_PID_CHANGE_AFTER=N makes MainPID change after N reads (a crash-restart)
#   curl        a PATH stub for the HTTP probe; STUB_LISTEN_AFTER=N answers only from the
#               (N+1)th probe on, STUB_LISTEN_UNTIL=N stops answering after N probes
#   sleep       a PATH stub that returns at once and logs, so the poll window costs no time
# The prod binary, its stamp and the previous copy all live under the mktemp dir, which
# is removed on exit. Nothing touches the real ~/bin or the real service.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fails=0
fail() {
  echo "FAIL: $*" >&2
  fails=$((fails + 1))
}
pass() { echo "ok:   $*"; }
# content FILE WANT MESSAGE: FILE holds exactly WANT.
content() {
  local got
  got=$(cat "$1" 2>/dev/null || echo "<missing>")
  if [ "$got" = "$2" ]; then pass "$3"; else fail "$3 ($1 is '$got', want '$2')"; fi
}
missing() { # PATH MESSAGE
  if [ -e "$1" ]; then fail "$2 ($1 exists)"; else pass "$2"; fi
}
expect_rc() { # ACTUAL WANT MESSAGE
  if [ "$1" -eq "$2" ]; then pass "$3"; else
    fail "$3 (exit $1, want $2)"
    sed 's/^/        | /' "$tmp/out" | tail -8 >&2 || true
  fi
}

export HOME=$tmp/home
mkdir -p "$HOME"
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1

# ---- stubs --------------------------------------------------------------------------
mkdir -p "$tmp/bin"
cat >"$tmp/bin/go" <<'EOF'
#!/bin/sh
# go build -ldflags "... -X main.commit=<sha>" -o <out> <pkg>: write "build <sha>" to <out>.
# Like the real go, a failed build writes nothing.
[ "$1" = build ] || { echo "stub go: unexpected call: $*" >&2; exit 1; }
[ -z "${STUB_GO_FAIL:-}" ] || { echo "stub go: build failed" >&2; exit 1; }
out= flags= prev=
for a in "$@"; do
  [ "$prev" = -o ] && out=$a
  [ "$prev" = -ldflags ] && flags=$a
  prev=$a
done
[ -n "$out" ] || { echo "stub go: build without -o" >&2; exit 1; }
echo "build $(echo "$flags" | sed 's/.*main\.commit=\([0-9a-f]*\).*/\1/')" >"$out"
EOF
cat >"$tmp/bin/systemctl" <<'EOF'
#!/bin/sh
echo "$*" >>"$STUB_OUT/systemctl.log"
# bump NAME: count the calls of one kind in this deploy; prints the new count.
bump() {
  n=$(cat "$STUB_OUT/count.$1" 2>/dev/null || echo 0)
  n=$((n + 1))
  echo "$n" >"$STUB_OUT/count.$1"
  echo "$n"
}
case "$*" in
*is-active*)
  n=$(bump active)
  [ -z "${STUB_RESTART_FAIL:-}" ] || exit 1
  [ -z "${STUB_ACTIVE_UNTIL:-}" ] || [ "$n" -le "$STUB_ACTIVE_UNTIL" ]
  ;;
*show*)
  n=$(bump show)
  if [ -n "${STUB_PID_CHANGE_AFTER:-}" ] && [ "$n" -gt "$STUB_PID_CHANGE_AFTER" ]; then echo 4243; else echo 4242; fi
  ;;
*) exit 0 ;;
esac
EOF
cat >"$tmp/bin/curl" <<'EOF'
#!/bin/sh
# The HTTP probe: prints the status code the way `curl -w %{http_code}` does, 000 and exit 7
# when nothing answers. Any other code (a 403 included) means a listener answered.
echo "$*" >>"$STUB_OUT/curl.log"
n=$(cat "$STUB_OUT/count.curl" 2>/dev/null || echo 0)
n=$((n + 1))
echo "$n" >"$STUB_OUT/count.curl"
if { [ -n "${STUB_LISTEN_AFTER:-}" ] && [ "$n" -le "$STUB_LISTEN_AFTER" ]; } ||
  { [ -n "${STUB_LISTEN_UNTIL:-}" ] && [ "$n" -gt "$STUB_LISTEN_UNTIL" ]; }; then
  printf 000
  exit 7
fi
printf 200
EOF
cat >"$tmp/bin/sleep" <<'EOF'
#!/bin/sh
echo "$*" >>"$STUB_OUT/sleep.log"
EOF
chmod +x "$tmp/bin/go" "$tmp/bin/systemctl" "$tmp/bin/curl" "$tmp/bin/sleep"
export PATH=$tmp/bin:$PATH STUB_OUT=$tmp
export GO=$tmp/bin/go AB_PROD_CONFIRM=yes AB_PROD_SERVICE=stub-prod
# The address the probe is told to use; the script has no config to read it from.
export AB_PROD_ADDR=127.0.0.1:18899
# A space in the path: the printed rollback line is pasted into a shell, so it must quote.
export AB_PROD_BIN="$HOME/prod bin/culibox-prod"
bin=$AB_PROD_BIN
stamp=$bin.deployed-sha

# ---- the fixture checkout the script deploys -----------------------------------------
repo=$tmp/repo
git init -q -b main "$repo"
mkdir -p "$repo/scripts"
cp "$here/deploy-prod.sh" "$here/version.sh" "$repo/scripts/"
commit() { # MESSAGE: commit a change on main and print its sha
  echo "$1" >>"$repo/CHANGES"
  git -C "$repo" add -A
  git -C "$repo" -c user.name=t -c user.email=t@example.com -c commit.gpgsign=false commit -q -m "$1"
  git -C "$repo" rev-parse HEAD
}
deploy() { # run the script; its exit code lands in $rc, its output in $tmp/out
  rc=0
  rm -f "$tmp"/count.* "$tmp/curl.log" "$tmp/sleep.log"
  bash "$repo/scripts/deploy-prod.sh" >"$tmp/out" 2>&1 || rc=$?
}
# The rollback the script printed, as one shell line.
rollback_line() { sed -n 's/^rollback: //p' "$tmp/out" | tail -1; }
# The line that points at the data restore (#229): printed with every rollback, never without one.
DATA_LINE="data: to also undo data changes, restore the backup taken at upgrade: see docs/UPGRADING.md"
data_lines() { grep -cFx "$DATA_LINE" "$tmp/out" || true; }

# ---- 1. first deploy: nothing to keep ------------------------------------------------
a=$(commit A)
deploy
expect_rc "$rc" 0 "first deploy succeeds"
content "$bin" "build $a" "first deploy installs A"
missing "$bin.prev" "first deploy has no previous binary to keep"
if [ -z "$(rollback_line)" ]; then pass "first deploy prints no rollback"; else fail "first deploy printed a rollback with nothing to roll back to"; fi
if ! grep -q '^data: ' "$tmp/out"; then pass "first deploy prints no data: line"; else fail "first deploy printed a data: line with nothing to roll back to"; fi

# ---- 2. a new commit keeps the binary it replaces -----------------------------------
b=$(commit B)
deploy
expect_rc "$rc" 0 "second deploy succeeds"
content "$bin" "build $b" "second deploy installs B"
content "$bin.prev" "build $a" "the replaced binary A is kept as .prev"
content "$bin.prev.deployed-sha" "$a" ".prev records the commit A it was built from"
missing "$bin.new" "no half-built file is left behind"
if [ "$(data_lines)" = 1 ]; then pass "a deploy with a .prev prints the data: restore line once"; else fail "a deploy with a .prev did not print the data: line exactly once"; fi

# ---- 3. a rebuild of the deployed commit keeps the real previous one ------------------
deploy
expect_rc "$rc" 0 "rebuild succeeds"
content "$bin.prev" "build $a" "a rebuild of B does not turn .prev into B"

# ---- 4. a failed build changes nothing ---------------------------------------------
c=$(commit C)
STUB_GO_FAIL=1 deploy
expect_rc "$rc" 1 "a failed build fails the deploy"
content "$bin" "build $b" "a failed build leaves the running binary B"
content "$bin.prev" "build $a" "a failed build leaves .prev A"
missing "$bin.new" "a failed build leaves no .new file"

# ---- 5. the service does not come up: the rollback is printed ------------------------
STUB_RESTART_FAIL=1 deploy
expect_rc "$rc" 1 "a service that does not come up fails the deploy"
content "$bin" "build $c" "C is installed"
content "$bin.prev" "build $b" "B, the binary it replaced, is kept"
if [ -n "$(rollback_line)" ]; then pass "a failed restart prints the rollback"; else fail "a failed restart printed no rollback line"; fi
if [ "$(data_lines)" = 1 ]; then pass "a failed restart prints the data: restore line once"; else fail "a failed restart did not print the data: line exactly once"; fi

# ---- 6. fixing forward after that: the build that never came up is not kept ------------
d=$(commit D)
deploy
expect_rc "$rc" 0 "the fix-forward deploy succeeds"
content "$bin" "build $d" "D is installed"
content "$bin.prev" "build $b" ".prev stays B, the last build that came up, not C"
content "$bin.prev.deployed-sha" "$b" ".prev still records B"

# ---- 7. the printed rollback works --------------------------------------------------
line=$(rollback_line)
if [ -n "$line" ]; then
  : >"$tmp/systemctl.log"
  if bash -c "$line" >"$tmp/rb.out" 2>&1; then pass "the printed rollback runs"; else fail "the printed rollback failed: $(cat "$tmp/rb.out")"; fi
  content "$bin" "build $b" "the rollback puts B back"
  content "$stamp" "$b" "the rollback points the stamp at B, so the next deploy lists C and D"
  if grep -q "restart stub-prod" "$tmp/systemctl.log"; then pass "the rollback restarts the service"; else fail "the rollback did not restart stub-prod"; fi
else
  fail "D's deploy printed no rollback line"
fi

# ---- 8. after a rollback, the next deploy keeps B, not a stale copy -------------------
deploy
expect_rc "$rc" 0 "the deploy after a rollback succeeds"
content "$bin" "build $d" "D is installed again"
content "$bin.prev" "build $b" "B, the binary running after the rollback, is kept"

# ---- 9. no stamp: the kept binary has no commit, and the rollback clears the stamp ------
rm -f "$stamp"
commit E >/dev/null
deploy
expect_rc "$rc" 0 "a deploy with no stamp succeeds"
content "$bin.prev" "build $d" "with no stamp the running binary D is still kept"
missing "$bin.prev.deployed-sha" "a kept binary of unknown commit gets no commit record"
line=$(rollback_line)
if bash -c "$line" >"$tmp/rb.out" 2>&1; then pass "the no-stamp rollback runs"; else fail "the no-stamp rollback failed: $(cat "$tmp/rb.out")"; fi
content "$bin" "build $d" "the no-stamp rollback puts D back"
missing "$stamp" "the no-stamp rollback removes the stamp rather than leave E's"

# ---- 10. a failed restart, then the same commit again: still not kept -------------------
f=$(commit F)
STUB_RESTART_FAIL=1 deploy
expect_rc "$rc" 1 "F does not come up"
deploy
expect_rc "$rc" 0 "F deployed again succeeds"
content "$bin" "build $f" "F is installed"
content "$bin.prev" "build $d" ".prev stays D, not the F that did not come up"

# ---- 11. "came up" is a window, not one look (#274) -------------------------------------
# State here: F is installed and stamped, D is .prev. The unit passing one is-active check
# is not enough: a build can die a few seconds later, or serve can still be taking its
# upgrade backup (it listens only after it) and then exit fail-closed.
lines() { if [ -f "$1" ]; then wc -l <"$1" | tr -d ' '; else echo 0; fi; }
unverified=$bin.unverified
# not_recorded: the failed deploy left the stamp and .prev alone, kept $UNVERIFIED, and
# printed the rollback and a message about what went wrong.
not_recorded() { # STAMPED-SHA PREV-SHA MESSAGE-PATTERN LABEL
  expect_rc "$rc" 1 "$4: the deploy fails"
  content "$stamp" "$1" "$4: the commit is not stamped"
  content "$bin.prev" "build $2" "$4: .prev is untouched by the rollback bookkeeping"
  if [ -e "$unverified" ]; then pass "$4: the build stays marked unverified"; else fail "$4: the build was marked verified"; fi
  if grep -q "$3" "$tmp/out"; then pass "$4: the operator is told why"; else fail "$4: no message matching '$3'"; sed 's/^/        | /' "$tmp/out" | tail -8 >&2; fi
  if [ -n "$(rollback_line)" ]; then pass "$4: the rollback line is printed"; else fail "$4: no rollback line"; fi
}

# The defaults: the probe goes to AB_PROD_ADDR, and a healthy build is watched for 15 s.
g=$(commit G)
deploy
expect_rc "$rc" 0 "a healthy build is recorded"
content "$stamp" "$g" "G is stamped"
content "$bin.prev" "build $f" "F, which came up, is kept as .prev"
if grep -q "http://127.0.0.1:18899/" "$tmp/curl.log"; then pass "the listener probe goes to AB_PROD_ADDR"; else fail "the probe did not use AB_PROD_ADDR: $(cat "$tmp/curl.log" 2>/dev/null)"; fi
if [ "$(lines "$tmp/sleep.log")" -ge 15 ]; then pass "by default a healthy build is watched for at least 15 polls"; else fail "default window too short: $(lines "$tmp/sleep.log") polls"; fi

# The late crash: the unit is active for the first polls, then dies, inside the window.
export AB_PROD_LISTEN_WAIT=20 AB_PROD_STABLE_SECS=5
commit H >/dev/null
STUB_ACTIVE_UNTIL=3 deploy
not_recorded "$g" "$g" "stopped being active" "a build that crashes after the first polls"
# Fixing forward must keep G, the last build that really ran, not the H that died.
i=$(commit I)
deploy
expect_rc "$rc" 0 "the fix-forward deploy after the late crash succeeds"
content "$stamp" "$i" "I is stamped"
content "$bin.prev" "build $g" ".prev is G, the last build that ran, not the crashed H"
content "$bin.prev.deployed-sha" "$g" ".prev records G"

# A slow listen: serve is taking its upgrade backup and listens only after it. Not a failure.
j=$(commit J)
STUB_LISTEN_AFTER=8 deploy
expect_rc "$rc" 0 "a build that listens after 8 polls (the backup) still comes up"
content "$stamp" "$j" "J is stamped after the slow listen"
if [ "$(lines "$tmp/sleep.log")" -le 40 ]; then pass "the slow listen is waited for, not forever"; else fail "too many polls: $(lines "$tmp/sleep.log")"; fi

# The slow listen then a fail-closed exit (backup failed): the unit goes inactive before it ever listens.
commit K >/dev/null
STUB_LISTEN_AFTER=1000 STUB_ACTIVE_UNTIL=4 deploy
not_recorded "$j" "$j" "stopped being active" "a serve that exits fail-closed before it listens"
if [ "$(lines "$tmp/sleep.log")" -lt 10 ]; then pass "the fail-closed exit is noticed at once, not after the listen wait"; else fail "waited $(lines "$tmp/sleep.log") polls for a dead unit"; fi

# Active but never listening: bounded by AB_PROD_LISTEN_WAIT.
STUB_LISTEN_AFTER=1000 deploy
not_recorded "$j" "$j" "did not answer" "a build that is active but never listens"
if grep -q "AB_PROD_ADDR" "$tmp/out"; then pass "the message names AB_PROD_ADDR, the address it probed"; else fail "the message does not say which address or variable"; fi
if [ "$(lines "$tmp/sleep.log")" -le 21 ]; then pass "the wait for the listener is bounded by AB_PROD_LISTEN_WAIT"; else fail "waited $(lines "$tmp/sleep.log") polls for a 20 s limit"; fi

# Listening, then the listener goes away inside the stable window.
STUB_LISTEN_UNTIL=3 deploy
not_recorded "$j" "$j" "stopped answering" "a build whose listener dies inside the window"

# Active and listening at every poll, but a different process each time: a crash-restart between polls.
STUB_PID_CHANGE_AFTER=3 deploy
not_recorded "$j" "$j" "restarted" "a build that crash-restarts between polls"

# A setting that is not a number is refused before anything is built or swapped.
before=$(cat "$bin")
AB_PROD_STABLE_SECS=soon deploy
expect_rc "$rc" 1 "a non-numeric AB_PROD_STABLE_SECS is refused"
content "$bin" "$before" "a refused setting leaves the installed binary alone"
unset AB_PROD_LISTEN_WAIT AB_PROD_STABLE_SECS

if [ "$fails" -ne 0 ]; then
  echo "deploy-prod_test: $fails failure(s)" >&2
  exit 1
fi
echo "deploy-prod_test: all passed"
