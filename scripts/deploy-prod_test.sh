#!/usr/bin/env bash
# Offline test for scripts/deploy-prod.sh: run it with `bash scripts/deploy-prod_test.sh`.
#
# The script runs from a copy inside a throwaway fixture repo (it deploys the checkout
# it lives in), with every external effect stubbed:
#   go          $GO points at a stub that "builds" a file holding the commit it was given
#   systemctl   a PATH stub; STUB_RESTART_FAIL=1 makes the service not come up
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
case "$*" in
*is-active*) [ -z "${STUB_RESTART_FAIL:-}" ] ;;
*) exit 0 ;;
esac
EOF
chmod +x "$tmp/bin/go" "$tmp/bin/systemctl"
export PATH=$tmp/bin:$PATH STUB_OUT=$tmp
export GO=$tmp/bin/go AB_PROD_CONFIRM=yes AB_PROD_SERVICE=stub-prod
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

if [ "$fails" -ne 0 ]; then
  echo "deploy-prod_test: $fails failure(s)" >&2
  exit 1
fi
echo "deploy-prod_test: all passed"
