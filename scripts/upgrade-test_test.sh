#!/usr/bin/env bash
# Offline test for scripts/upgrade-test.sh: run it with `bash scripts/upgrade-test_test.sh`.
#
# The script resolves the previous release from the checkout it lives in, so it runs
# from a copy inside throwaway git repos. AB_UPGRADE_DRYRUN=1 makes it print what it
# would build and run, then exit, so nothing is built and no server starts. Binaries
# passed in AB_PREV_BIN are stubs that only answer `version`.
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
expect_rc() { # ACTUAL WANT MESSAGE
  if [ "$1" -eq "$2" ]; then pass "$3"; else
    fail "$3 (exit $1, want $2)"
    sed 's/^/        | /' "$tmp/out" | tail -8 >&2 || true
  fi
}
has() { # PATTERN MESSAGE: the last run's output contains PATTERN (fixed string)
  if grep -qF -- "$1" "$tmp/out"; then pass "$2"; else
    fail "$2 (no '$1' in output)"
    sed 's/^/        | /' "$tmp/out" | tail -8 >&2 || true
  fi
}

export HOME=$tmp/home
mkdir -p "$HOME"
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.invalid GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.invalid
unset AB_PREV_BIN AB_CUR_BIN AB_PREV_REF AB_UPGRADE_TESTBIN AB_UPGRADE_BUILD AB_UPGRADE_KEEP AB_UPGRADE_REQUIRE || true

# repo NAME MAINPKG: a fresh repo holding a copy of the script, with MAINPKG
# (cmd/culibox or cmd/agent-box) as the main package. Prints its path.
repo() {
  local r=$tmp/$1
  mkdir -p "$r/scripts" "$r/$2"
  cp "$here/upgrade-test.sh" "$here/version.sh" "$r/scripts/"
  echo 'package main' >"$r/$2/main.go"
  git -C "$r" init -q
  git -C "$r" add -A
  git -C "$r" commit -qm one
  echo "$r"
}
commit() { git -C "$1" commit -q --allow-empty -m "$2"; }
run() { # REPO [ENV=VAL...]: dry-run the script in REPO; rc in $rc
  local r=$1
  shift
  rc=0
  env AB_UPGRADE_DRYRUN=1 "$@" bash "$r/scripts/upgrade-test.sh" >"$tmp/out" 2>&1 || rc=$?
}
stub() { # NAME OUTPUT RC: a stub binary whose `version` prints OUTPUT and exits RC
  local s=$tmp/stubs/$1
  mkdir -p "$tmp/stubs"
  # shellcheck disable=SC2016 # $1 belongs to the stub, not to this script
  printf '#!/bin/sh\n[ "$1" = version ] || exit 9\necho "%s"\nexit %s\n' "$2" "$3" >"$s"
  chmod 755 "$s"
  echo "$s"
}

# 1. Two commits past v0.2.0, with v0.1.0 older: vN-1 is v0.2.0.
r=$(repo r1 cmd/agent-box)
git -C "$r" tag -a v0.1.0 -m v0.1.0
commit "$r" two
git -C "$r" tag -a v0.2.0 -m v0.2.0
commit "$r" three
commit "$r" four
run "$r"
expect_rc "$rc" 0 "1: dry run exits 0"
has "upgrade-test: v0.2.0 (2 commits behind) -> v0.2.0-2-g" "1: resolves the newest tag below HEAD"
# 10. The builds are stamped: prev with its tag, cur with version.sh.
has '-X main.version=v0.2.0"' "10: the previous build is stamped with its tag"
has "-X main.version=$(git -C "$r" describe --tags --always --dirty --match 'v[0-9]*')\"" "10: the current build is stamped with version.sh"
# 9. The main package comes from the tree at each ref.
has "./cmd/agent-box" "9: a tree with only cmd/agent-box builds ./cmd/agent-box"

# 2. HEAD exactly at a tag: vN-1 is the tag before it.
r=$(repo r2 cmd/culibox)
git -C "$r" tag -a v0.1.0 -m v0.1.0
commit "$r" two
git -C "$r" tag -a v0.2.0 -m v0.2.0
run "$r"
expect_rc "$rc" 0 "2: dry run exits 0"
has "upgrade-test: v0.1.0 (1 commits behind) -> v0.2.0" "2: HEAD's own tag is skipped"
has "./cmd/culibox" "9: a tree with cmd/culibox builds ./cmd/culibox"

# 3, 4. No tag at all: a loud SKIP, a failure only under AB_UPGRADE_REQUIRE=1.
r=$(repo r3 cmd/culibox)
run "$r"
expect_rc "$rc" 0 "3: no tag exits 0"
has "SKIP upgrade-test: no previous v* tag reachable" "3: no tag prints SKIP"
run "$r" AB_UPGRADE_REQUIRE=1
expect_rc "$rc" 1 "4: no tag under AB_UPGRADE_REQUIRE=1 fails"
has "SKIP upgrade-test: no previous v* tag reachable" "4: and still says why"

# 5. AB_PREV_REF overrides the resolved tag; an unknown ref fails.
r=$tmp/r1
run "$r" AB_PREV_REF=v0.1.0
expect_rc "$rc" 0 "5: AB_PREV_REF exits 0"
has "upgrade-test: v0.1.0 (" "5: AB_PREV_REF wins"
run "$r" AB_PREV_REF=v9.9.9
expect_rc "$rc" 1 "5: an unknown AB_PREV_REF fails"

# 6, 7, 8. The version guard on a supplied AB_PREV_BIN.
run "$r" AB_PREV_REF=v0.1.0 AB_PREV_BIN="$(stub wrong 'agent-box v0.9.9 (x)' 0)"
expect_rc "$rc" 1 "6: a supplied binary of another version fails"
has "version mismatch" "6: and names the mismatch"
run "$r" AB_PREV_REF=v0.1.0 AB_PREV_BIN="$(stub old 'agent-box: unknown command "version"' 2)"
expect_rc "$rc" 0 "7: a binary without a version command goes on"
has "version unverifiable" "7: and says it could not check"
run "$r" AB_PREV_REF=v0.1.0 AB_PREV_BIN="$(stub good 'culibox v0.1.0 (abc)' 0)"
expect_rc "$rc" 0 "8: a matching binary passes the guard"
has "prev: $tmp/stubs/good (supplied)" "8: and is used instead of a build"
run "$r" AB_PREV_REF=v0.1.0 AB_PREV_BIN="$(stub prefix 'agent-box v0.1.0-3-gabc (abc)' 0)"
expect_rc "$rc" 1 "6: a tag that only prefixes the reported version is a mismatch"

# The script is bash 3.2 code that also runs on macOS: lint it when shellcheck is here.
if command -v shellcheck >/dev/null 2>&1; then
  if shellcheck "$here/upgrade-test.sh" >"$tmp/out" 2>&1; then pass "shellcheck is clean"; else
    fail "shellcheck"
    sed 's/^/        | /' "$tmp/out" | tail -12 >&2
  fi
fi

if [ "$fails" -ne 0 ]; then
  echo "upgrade-test_test: $fails check(s) failed" >&2
  exit 1
fi
echo "upgrade-test_test: all checks passed"
