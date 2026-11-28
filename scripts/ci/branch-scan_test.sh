#!/usr/bin/env bash
# Test for scripts/ci/branch-scan.sh: run it with `bash scripts/ci/branch-scan_test.sh`.
# The first part is offline: gitleaks is a stub that records its arguments, the
# repository a throwaway one. The end-to-end part runs the real scanner with the repo's
# .gitleaks.toml when gitleaks is on PATH (scripts/test.sh puts the pinned one there);
# without it that part SKIPS, unless BRANCH_SCAN_TEST_REQUIRE=1, which CI sets.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
script=$here/branch-scan.sh
# The real scanner, if any, before the stub below shadows it.
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

# The stub records "$PWD: args" so the uncommitted-files scan's working dir shows.
cat >"$tmp/bin/gitleaks" <<'STUB'
#!/bin/sh
echo "$*" >>"$STUB_LOG"
[ "$1" = dir ] && ls -R . >>"$STUB_LOG"
exit "${STUB_RC:-0}"
STUB
chmod 755 "$tmp/bin/gitleaks"

# newrepo DIR: a repo laid out like the culibox tree (the script cds to its ../..)
# with one commit, which origin/main points at.
gc() { git -C "$1" -c user.name=t -c user.email=t@example.com -c commit.gpgsign=false "${@:2}"; }
newrepo() {
  mkdir -p "$1/scripts/ci"
  cp "$script" "$1/scripts/ci/"
  [ -n "${2:-}" ] && cp "$here/../../.gitleaks.toml" "$1/"
  printf 'ignored/\n' >"$1/.gitignore"
  git init -q -b main "$1"
  gc "$1" add -A
  gc "$1" commit -q -m base
  gc "$1" update-ref refs/remotes/origin/main HEAD
}

repo=$tmp/repo
newrepo "$repo"
base=$(git -C "$repo" rev-parse HEAD)
echo b >"$repo/f"
gc "$repo" add f
gc "$repo" commit -q -m c2

run() { env PATH="$tmp/bin:/usr/bin:/bin" STUB_LOG="$tmp/log" "$@" bash "$repo/scripts/ci/branch-scan.sh"; }

# Committed: the range is passed as a resolved commit id, never as the ref text.
: >"$tmp/log"
if out=$(run 2>&1); then pass "clean branch exits 0"; else fail "clean branch exited non-zero: $out"; fi
if grep -qx -- "git --config $repo/.gitleaks.toml --redact --no-banner --log-level warn --log-opts=$base..HEAD -m ." "$tmp/log"; then
  pass "scans <base sha>..HEAD with merges, the repo config, redacted"
else fail "gitleaks args: $(cat "$tmp/log")"; fi
case $out in *"1 commit(s)"*) pass "names the commit count" ;; *) fail "output: $out" ;; esac
if ! grep -q '^dir ' "$tmp/log"; then pass "clean tree: no uncommitted-files scan"; else fail "scanned files on a clean tree"; fi

: >"$tmp/log"
if out=$(run STUB_RC=1 2>&1); then fail "a leak exited 0"; else pass "a leak fails the scan"; fi
case $out in *"Rewrite that commit"*) pass "and says a fix commit is not enough" ;; *) fail "message: $out" ;; esac

# Uncommitted: a modified file, a staged new file and an untracked file are copied (by
# their paths) into a temp dir and scanned with the repo config; ignored and deleted
# files are not.
echo c >"$repo/f"
mkdir -p "$repo/sub" "$repo/ignored"
echo s >"$repo/sub/staged.txt"
gc "$repo" add sub/staged.txt
echo u >"$repo/untracked.txt"
echo i >"$repo/ignored/x.txt"
: >"$tmp/log"
if out=$(run 2>&1); then pass "dirty tree, clean files: exits 0"; else fail "dirty tree exited non-zero: $out"; fi
if grep -qx -- "dir --config $repo/.gitleaks.toml --redact --no-banner --log-level warn ." "$tmp/log"; then pass "scans uncommitted files with the repo config"; else fail "dir args: $(cat "$tmp/log")"; fi
listing=$(sed -n '/^dir /,$p' "$tmp/log")
for want in f staged.txt untracked.txt; do
  case $listing in *"$want"*) pass "uncommitted scan holds $want" ;; *) fail "uncommitted scan lacks $want: $listing" ;; esac
done
case $listing in *x.txt*) fail "an ignored file was scanned" ;; *) pass "ignored files are not scanned" ;; esac
case $out in *"3 uncommitted file(s)"*) pass "names the uncommitted file count" ;; *) fail "output: $out" ;; esac
: >"$tmp/log"
if out=$(run STUB_RC=1 2>&1); then fail "an uncommitted leak exited 0"; else pass "an uncommitted leak fails"; fi
case $out in *"uncommitted"*"fix it before you commit"*) pass "and says so" ;; *) fail "message: $out" ;; esac
gc "$repo" reset -q --hard
rm -rf "$repo/untracked.txt" "$repo/ignored"

# HEAD == base and a clean tree: nothing to scan, gitleaks not run.
gc "$repo" update-ref refs/remotes/origin/main HEAD
: >"$tmp/log"
if out=$(run 2>&1) && [ ! -s "$tmp/log" ]; then pass "nothing ahead, clean tree: no scan"; else fail "ran gitleaks with nothing to scan: $(cat "$tmp/log")"; fi

# No origin/main in this clone: the commits part says SKIPPED; the tree is still scanned.
gc "$repo" update-ref -d refs/remotes/origin/main
echo u >"$repo/untracked.txt"
: >"$tmp/log"
if out=$(run 2>&1); then pass "no base: exits 0"; else fail "no base: $out"; fi
case $out in *SKIPPED*) pass "and says SKIPPED" ;; *) fail "skip message: $out" ;; esac
if grep -q '^dir ' "$tmp/log"; then pass "no base: uncommitted files still scanned"; else fail "no base: tree not scanned"; fi
rm -f "$repo/untracked.txt"

# A checkout git cannot read (no repo here, or one it refuses) fails, never "nothing to scan".
mkdir -p "$tmp/norepo/scripts/ci"
cp "$script" "$tmp/norepo/scripts/ci/"
echo x >"$tmp/norepo/leak.txt"
: >"$tmp/log"
if out=$(env PATH="$tmp/bin:/usr/bin:/bin" STUB_LOG="$tmp/log" GIT_CEILING_DIRECTORIES="$tmp" bash "$tmp/norepo/scripts/ci/branch-scan.sh" 2>&1); then
  fail "no git repo exited 0: $out"
else pass "no git repo fails"; fi

# A file whose name starts with a dash is copied as a file, not read as an option.
echo d >"$repo/-rf"
: >"$tmp/log"
if out=$(run 2>&1) && grep -q -- '-rf' "$tmp/log"; then pass "a dash-named file is scanned"; else fail "dash-named file: $out"; fi
rm -f "$repo/-rf"

# AB_SCAN_BASE picks another base, passed on as its commit id; an option-like one is refused.
: >"$tmp/log"
if run AB_SCAN_BASE="$base" >/dev/null 2>&1 && grep -q -- "--log-opts=$base..HEAD -m" "$tmp/log"; then pass "AB_SCAN_BASE is the base"; else fail "AB_SCAN_BASE: $(cat "$tmp/log")"; fi
: >"$tmp/log"
if run AB_SCAN_BASE=--all >/dev/null 2>&1; then fail "an option-like base exited 0"; else pass "an option-like base is refused"; fi
[ ! -s "$tmp/log" ] && pass "and gitleaks is not run" || fail "gitleaks ran for --all"

# No gitleaks on PATH with something to scan: fails loudly.
if out=$(env PATH="/usr/bin:/bin" AB_SCAN_BASE="$base" bash "$repo/scripts/ci/branch-scan.sh" 2>&1); then
  command -v gitleaks >/dev/null 2>&1 && pass "(gitleaks on the system PATH: missing-scanner case not testable here)" || fail "no gitleaks exited 0"
else pass "no gitleaks fails"; fi

# End to end with the real scanner and the repo's .gitleaks.toml: a fixture holding the
# home folder of a user who is not a documented placeholder fails, committed or not; the
# same fixture with a placeholder user passes. The values are assembled from fragments
# so this file holds nothing a scan would flag.
if [ -n "$real_gitleaks" ]; then
  mkdir -p "$tmp/real"
  ln -s "$real_gitleaks" "$tmp/real/gitleaks"
  # e2e USER MODE: MODE commit | staged | untracked. Exit status of the scan.
  e2e() {
    local r=$tmp/e2e-$1-$2
    rm -rf "$r"
    newrepo "$r" with-config
    printf 'const p = "/%s/%s/work/acme";\n' "home" "$1" >"$r/fixture.js"
    case $2 in
    commit) gc "$r" add fixture.js && gc "$r" commit -q -m fixture ;;
    staged) gc "$r" add fixture.js ;;
    esac
    env PATH="$tmp/real:/usr/bin:/bin" bash "$r/scripts/ci/branch-scan.sh" >/dev/null 2>&1
  }
  user=$(printf 'zq%sv' 'rt')
  for mode in commit staged untracked; do
    if e2e "$user" "$mode"; then fail "real scan ($mode): a non-placeholder home path passed"; else pass "real scan ($mode): a non-placeholder home path fails"; fi
    if e2e u "$mode"; then pass "real scan ($mode): a placeholder home path passes"; else fail "real scan ($mode): a placeholder home path failed"; fi
  done
elif [ "${BRANCH_SCAN_TEST_REQUIRE:-}" = 1 ]; then
  fail "end-to-end part needs gitleaks on PATH (BRANCH_SCAN_TEST_REQUIRE=1)"
else
  echo "branch-scan_test: end-to-end part SKIPPED (no gitleaks on PATH; scripts/test.sh provides it)" >&2
fi

[ "$fails" -eq 0 ] || { echo "branch-scan_test: $fails failure(s)" >&2; exit 1; }
echo "branch-scan_test: all passed"
