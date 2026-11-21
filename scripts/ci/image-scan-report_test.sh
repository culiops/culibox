#!/bin/sh
# Offline test for scripts/ci/image-scan-report.sh against hand-written Grype
# JSON (the fields it reads: matches[].vulnerability.{id,severity,fix.state,
# fix.versions} and matches[].artifact.{name,version,type}, as in Grype 0.121.2).
set -eu

here=$(cd "$(dirname "$0")" && pwd)
script=$here/image-scan-report.sh

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fails=0
fail() {
  echo "FAIL: $*" >&2
  fails=$((fails + 1))
}
pass() { echo "ok:   $*"; }
run() { # ARGS...: prints the exit code; stdout in $tmp/out, stderr in $tmp/err
  rc=0
  sh "$script" "$@" >"$tmp/out" 2>"$tmp/err" || rc=$?
  echo "$rc"
}
has() { # TEXT MESSAGE
  if grep -qF -- "$1" "$tmp/out"; then pass "$2"; else
    fail "$2 (no '$1' in output)"
    sed 's/^/        | /' "$tmp/out" >&2
  fi
}
fixable() { # N MESSAGE: the last stdout line
  got=$(tail -n 1 "$tmp/out")
  if [ "$got" = "<!-- fixable: $1 -->" ]; then pass "$2"; else fail "$2 (last line '$got')"; fi
}
match() { # ID SEVERITY STATE FIXVERSION PACKAGE VERSION
  printf '{"vulnerability":{"id":"%s","severity":"%s","fix":{"state":"%s","versions":[%s]}},"artifact":{"name":"%s","version":"%s","type":"deb"}}' \
    "$1" "$2" "$3" "$4" "$5" "$6"
}

printf '{"matches":[]}' >"$tmp/empty.json"
printf '{"matches":null}' >"$tmp/null.json"
{
  printf '{"matches":['
  match CVE-2026-0001 Critical fixed '"3.0.2"' openssl 3.0.1
  printf ','
  # The same finding at a second location: counted once.
  match CVE-2026-0001 Critical fixed '"3.0.2"' openssl 3.0.1
  printf ','
  match CVE-2026-0002 High not-fixed '' chromium 156.0
  printf ','
  match CVE-2026-0003 Medium fixed '"1.2"' zlib 1.1
  printf ']}'
} >"$tmp/mixed.json"
{
  printf '{"matches":['
  match CVE-2026-0009 High fixed '"2"' 'a|b`$(x)' '1'
  printf ']}'
} >"$tmp/hostile.json"
printf 'not json' >"$tmp/bad.json"
: >"$tmp/none.txt"

rc=$(run -d 2026-11-21 "base amd64=$tmp/empty.json")
[ "$rc" -eq 0 ] && pass "empty: exit 0" || fail "empty: exit $rc"
has "No findings." "empty: says so"
fixable 0 "empty: fixable 0"

rc=$(run -d 2026-11-21 "x=$tmp/null.json")
[ "$rc" -eq 0 ] && pass "null matches: exit 0" || fail "null matches: exit $rc: $(cat "$tmp/err")"
fixable 0 "null matches: fixable 0"

rc=$(run -d 2026-11-21 "base amd64=$tmp/mixed.json")
[ "$rc" -eq 0 ] && pass "mixed: exit 0 (findings never fail the report)" || fail "mixed: exit $rc"
has "### base amd64" "mixed: section per label"
has "| Critical | 1 | 1 |" "mixed: Critical counted once (deduped)"
has "| High | 1 | 0 |" "mixed: High not-fixed has no fix"
has "| Medium | 1 | 1 |" "mixed: Medium counted"
has "| CVE-2026-0001 | openssl | 3.0.1 | 3.0.2 | deb |" "mixed: fixable row"
if grep -q 'CVE-2026-0002\|CVE-2026-0003' "$tmp/out"; then fail "mixed: only HIGH/CRITICAL with a fix are listed"; else pass "mixed: table holds fixable HIGH/CRITICAL only"; fi
fixable 1 "mixed: fixable 1"

printf '# accepted findings\n\nCVE-2026-0001 2026-11-20 openssl is not reachable from the agent\n' >"$tmp/acc.txt"
rc=$(run -a "$tmp/acc.txt" -d 2026-11-21 "base=$tmp/mixed.json")
[ "$rc" -eq 0 ] && pass "accepted: exit 0" || fail "accepted: exit $rc: $(cat "$tmp/err")"
has "Accepted" "accepted: listed apart"
fixable 0 "accepted: not counted"

printf 'CVE-2026-0001 2026-05-24 too old\n' >"$tmp/old.txt"
rc=$(run -a "$tmp/old.txt" -d 2026-11-21 "base=$tmp/mixed.json")
[ "$rc" -eq 0 ] && pass "expired: exit 0" || fail "expired: exit $rc"
fixable 1 "expired: counted again"
grep -q 'CVE-2026-0001.*expired' "$tmp/err" && pass "expired: named on stderr" || fail "expired: stderr '$(cat "$tmp/err")'"

printf 'CVE-2026-0001 2026-05-25 exactly 180 days\n' >"$tmp/edge.txt"
run -a "$tmp/edge.txt" -d 2026-11-21 "base=$tmp/mixed.json" >/dev/null
fixable 0 "180 days old: still accepted"

for bad in 'CVE-2026-0001 2026-11-20' 'CVE-26-1 2026-11-20 short id' 'CVE-2026-0001 2026-13-40 bad date' 'CVE-2026-0001 2026-12-01 future date'; do
  printf '%s\n' "$bad" >"$tmp/bad.txt"
  rc=$(run -a "$tmp/bad.txt" -d 2026-11-21 "base=$tmp/mixed.json")
  [ "$rc" -eq 1 ] && pass "malformed refused: $bad" || fail "malformed accepted (exit $rc): $bad"
done
printf 'GHSA-c2f9-gh3m-pqrv 2026-11-20 a GitHub advisory\n' >"$tmp/ghsa.txt"
rc=$(run -a "$tmp/ghsa.txt" -d 2026-11-21 "base=$tmp/mixed.json")
[ "$rc" -eq 0 ] && pass "GHSA id accepted" || fail "GHSA id refused: $(cat "$tmp/err")"

rc=$(run -d 2026-11-21 "x=$tmp/hostile.json")
has '| CVE-2026-0009 | a?b???x? |' "hostile: sanitised"
row=$(grep 'CVE-2026-0009' "$tmp/out" || true)
[ "$(printf '%s' "$row" | tr -cd '|' | wc -c)" -eq 6 ] && pass "hostile: one row, 6 pipes" || fail "hostile: row '$row'"

rc=$(run -d 2026-11-21 "base=$tmp/mixed.json" "release=$tmp/hostile.json")
has "### base" "two labels: first section"
has "### release" "two labels: second section"
fixable 2 "two labels: fixable sums"

rc=$(run -d 2026-11-21 "x=$tmp/bad.json")
[ "$rc" -eq 1 ] && pass "invalid JSON: exit 1" || fail "invalid JSON: exit $rc"
rc=$(run -d 2026-11-21 "x=$tmp/missing.json")
[ "$rc" -eq 1 ] && pass "missing file: exit 1" || fail "missing file: exit $rc"
rc=$(run -d 2026-11-21 "$tmp/mixed.json")
[ "$rc" -eq 2 ] && pass "no label: usage, exit 2" || fail "no label: exit $rc"

rc=$(run -a "$here/image-scan-accepted.txt" "base=$tmp/empty.json")
[ "$rc" -eq 0 ] && pass "the committed accepted file parses" || fail "the committed accepted file: $(cat "$tmp/err")"

if command -v shellcheck >/dev/null 2>&1; then
  if shellcheck -s sh "$script" >"$tmp/sc.out" 2>&1; then pass "shellcheck -s sh is clean"; else
    fail "shellcheck -s sh found problems"
    sed 's/^/        | /' "$tmp/sc.out" >&2
  fi
else
  echo "SKIP shellcheck (not installed)"
fi

if [ "$fails" -ne 0 ]; then
  echo "image-scan-report_test: $fails failure(s)" >&2
  exit 1
fi
echo "image-scan-report_test: all passed"
