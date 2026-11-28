#!/usr/bin/env bash
# Offline test for scripts/ci/branch-scan.sh: run it with `bash scripts/ci/branch-scan_test.sh`.
# gitleaks is a stub that records its arguments; the repository is a throwaway one.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
script=$here/branch-scan.sh
# The real scanner, if any, before the stub below shadows it (test.sh puts the pinned one on PATH).
real_gitleaks=$(command -v gitleaks || true)

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export HOME=$tmp/home GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
mkdir -p "$HOME" "$tmp/bin"

fails=0
fail() {
  echo "FAIL: $*" >&2
  fails=$((fails + 1))
}
pass() { echo "ok:   $*"; }

cat >"$tmp/bin/gitleaks" <<'STUB'
#!/bin/sh
echo "$*" >>"$STUB_LOG"
exit "${STUB_RC:-0}"
STUB
chmod 755 "$tmp/bin/gitleaks"

# A repo laid out like the culibox tree (the script cds to its ../..), with origin/main
# one commit behind HEAD.
repo=$tmp/repo
mkdir -p "$repo/scripts/ci"
cp "$script" "$repo/scripts/ci/"
git init -q -b main "$repo"
gc() { git -C "$repo" -c user.name=t -c user.email=t@example.com -c commit.gpgsign=false "$@"; }
echo a >"$repo/f"
gc add -A
gc commit -q -m c1
gc update-ref refs/remotes/origin/main HEAD
base=$(git -C "$repo" rev-parse HEAD)
echo b >"$repo/f"
gc commit -q -am c2

run() { env PATH="$tmp/bin:/usr/bin:/bin" STUB_LOG="$tmp/log" "$@" bash "$repo/scripts/ci/branch-scan.sh"; }

: >"$tmp/log"
if out=$(run 2>&1); then pass "clean branch exits 0"; else fail "clean branch exited non-zero: $out"; fi
if grep -qx -- "git --config .gitleaks.toml --redact --no-banner --log-level warn --log-opts=origin/main..HEAD -m ." "$tmp/log"; then
  pass "scans origin/main..HEAD with merges, the repo config, redacted"
else fail "gitleaks args: $(cat "$tmp/log")"; fi
case $out in *"1 commit(s)"*) pass "names the commit count" ;; *) fail "output: $out" ;; esac

: >"$tmp/log"
if out=$(run STUB_RC=1 2>&1); then fail "a leak exited 0"; else pass "a leak fails the scan"; fi
case $out in *"Rewrite that commit"*) pass "and says a fix commit is not enough" ;; *) fail "message: $out" ;; esac

# HEAD == base: nothing to scan, gitleaks not run.
gc update-ref refs/remotes/origin/main HEAD
: >"$tmp/log"
if out=$(run 2>&1) && [ ! -s "$tmp/log" ]; then pass "nothing ahead: no scan"; else fail "ran gitleaks with nothing ahead: $(cat "$tmp/log")"; fi

# No origin/main in this clone: a loud skip, exit 0.
gc update-ref -d refs/remotes/origin/main
: >"$tmp/log"
if out=$(run 2>&1) && [ ! -s "$tmp/log" ]; then pass "no base: skip"; else fail "no base: $out"; fi
case $out in *SKIPPED*) pass "and says SKIPPED" ;; *) fail "skip message: $out" ;; esac

# AB_SCAN_BASE picks another base; one that looks like an option is refused.
: >"$tmp/log"
if run AB_SCAN_BASE="$base" >/dev/null 2>&1 && grep -q -- "--log-opts=$base..HEAD -m" "$tmp/log"; then pass "AB_SCAN_BASE is the base"; else fail "AB_SCAN_BASE: $(cat "$tmp/log")"; fi
: >"$tmp/log"
if run AB_SCAN_BASE=--all >/dev/null 2>&1; then fail "an option-like base exited 0"; else pass "an option-like base is refused"; fi
[ ! -s "$tmp/log" ] && pass "and gitleaks is not run" || fail "gitleaks ran for --all"

# No gitleaks on PATH: fails loudly.
if out=$(env PATH="/usr/bin:/bin" AB_SCAN_BASE="$base" bash "$repo/scripts/ci/branch-scan.sh" 2>&1); then
  command -v gitleaks >/dev/null 2>&1 && pass "(gitleaks on the system PATH: missing-scanner case not testable here)" || fail "no gitleaks exited 0"
else pass "no gitleaks fails"; fi

# End to end with the real scanner and the repo's .gitleaks.toml: a commit whose fixture
# holds a home path of a user that is not a documented placeholder fails; the same
# commit with a placeholder user passes. The values are assembled from fragments so
# this file holds nothing a scan would flag.
if [ -n "$real_gitleaks" ]; then
  mkdir -p "$tmp/real"
  ln -s "$real_gitleaks" "$tmp/real/gitleaks"
  e2e() { # USER -> exit status of the scan of one commit that adds a fixture naming the home folder of that user
    local r=$tmp/e2e-$1
    rm -rf "$r"
    mkdir -p "$r/scripts/ci"
    cp "$script" "$r/scripts/ci/"
    cp "$here/../../.gitleaks.toml" "$r/"
    git init -q -b main "$r"
    git -C "$r" -c user.name=t -c user.email=t@example.com add -A
    git -C "$r" -c user.name=t -c user.email=t@example.com commit -q -m base
    git -C "$r" update-ref refs/remotes/origin/main HEAD
    printf 'const p = "/%s/%s/work/acme";\n' "home" "$1" >"$r/fixture.js"
    git -C "$r" -c user.name=t -c user.email=t@example.com add fixture.js
    git -C "$r" -c user.name=t -c user.email=t@example.com commit -q -m fixture
    env PATH="$tmp/real:/usr/bin:/bin" bash "$r/scripts/ci/branch-scan.sh" >/dev/null 2>&1
  }
  if e2e "$(printf 'zq%sv' 'rt')"; then fail "real scan: a non-placeholder home path passed"; else pass "real scan: a non-placeholder home path fails"; fi
  if e2e u; then pass "real scan: a placeholder home path passes"; else fail "real scan: a placeholder home path failed"; fi
elif [ "${BRANCH_SCAN_TEST_REQUIRE:-}" = 1 ]; then
  fail "end-to-end part needs gitleaks on PATH (BRANCH_SCAN_TEST_REQUIRE=1)"
else
  echo "branch-scan_test: end-to-end part SKIPPED (no gitleaks on PATH; scripts/test.sh provides it)" >&2
fi

[ "$fails" -eq 0 ] || { echo "branch-scan_test: $fails failure(s)" >&2; exit 1; }
echo "branch-scan_test: all passed"
