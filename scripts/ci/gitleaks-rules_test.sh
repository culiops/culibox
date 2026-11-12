#!/usr/bin/env bash
# Test for the custom rules in .gitleaks.toml: run it with
#   bash scripts/ci/gitleaks-rules_test.sh
# Needs `gitleaks` on PATH (CI installs the pinned release before this runs; without
# it the test SKIPS, loudly, unless GITLEAKS_RULES_TEST_REQUIRE=1, which CI sets).
#
# Each rule is proven both ways on made-up values: a fixture that must fire and one
# that must not. The values are assembled at run time from fragments, so this file
# itself holds nothing a scan would flag, and none of them is a real identifier.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
cfg=$here/../../.gitleaks.toml

if ! command -v gitleaks >/dev/null 2>&1; then
  if [ "${GITLEAKS_RULES_TEST_REQUIRE:-}" = 1 ]; then
    echo "gitleaks-rules_test: gitleaks is not on PATH" >&2
    exit 1
  fi
  echo "gitleaks-rules_test: SKIPPED (gitleaks is not on PATH)" >&2
  exit 0
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fails=0
fail() {
  echo "FAIL: $*" >&2
  fails=$((fails + 1))
}
pass() { echo "ok:   $*"; }

# scan LINE: writes LINE to a fresh file, scans it with the repo's config and prints
# the rule ids that fired, one per line.
n=0
scan() {
  n=$((n + 1))
  local d=$tmp/case$n
  mkdir -p "$d"
  printf '%s\n' "$1" >"$d/fixture.txt"
  gitleaks dir "$d" --config "$cfg" --no-banner --redact --exit-code 0 \
    --report-format json --report-path "$d/report.json" >/dev/null 2>&1
  grep -o '"RuleID": *"[^"]*"' "$d/report.json" | sed 's/.*: *"\(.*\)"/\1/' | sort -u
}
# fires RULE LINE MESSAGE / quiet RULE LINE MESSAGE
fires() {
  if scan "$2" | grep -qx "$1"; then pass "$3"; else fail "$3 ($1 did not fire)"; fi
}
quiet() {
  if scan "$2" | grep -qx "$1"; then fail "$3 ($1 fired)"; else pass "$3"; fi
}

dot=.
hm=/ho""me
um=/Us""ers

# ---- rule: a CGNAT / tailnet address outside the documented fixture range 100.64.0.0/24
r=culibox-cgnat-address
fires "$r" "host 100${dot}99${dot}1${dot}1 is up" "a CGNAT address in the middle of the range fires"
fires "$r" "ip=100${dot}127${dot}1${dot}1" "a CGNAT address near the top fires"
fires "$r" "url http://100${dot}64${dot}1${dot}7:8899/" "100.64.1.x, just outside the fixture /24, fires"
quiet "$r" "fixture 100${dot}64${dot}0${dot}7" "the documented fixture range 100.64.0.x is allowed"
quiet "$r" "cidr 100${dot}64${dot}0${dot}0/10" "the CGNAT network address itself is in the fixture range"
quiet "$r" "edge case 100${dot}127${dot}255${dot}254" "the one documented top-of-range edge fixture is allowed"
quiet "$r" "100${dot}63${dot}1${dot}1 is public" "100.63.x is not CGNAT"
quiet "$r" "100${dot}128${dot}1${dot}1 is public" "100.128.x is not CGNAT"
quiet "$r" "version 2${dot}100${dot}99${dot}1${dot}5 of a thing" "a longer dotted version is not an address"
quiet "$r" "id 1100${dot}99${dot}1${dot}1" "digits glued in front are not an address"

# ---- rule: an absolute home path outside the documented placeholder users
r=culibox-home-path
fires "$r" "cd ${hm}/jdoe42/work" "a /home/<name>/ path fires"
fires "$r" "path=${um}/jdoe42/Library/x" "a /Users/<name>/ path fires"
fires "$r" "HOME=\"${hm}/jdoe42\"" "a home path with nothing after the name fires"
quiet "$r" "cd ${hm}/user/work" "the placeholder user 'user' is allowed"
quiet "$r" "path=${um}/alice/Library/x" "the placeholder user 'alice' is allowed"
quiet "$r" "in the image: ${hm}/agent/.local/share" "the box image's own user 'agent' is allowed"
quiet "$r" "go to /var/home-lab/x and /srv/Users/" "other directories are not home paths"
quiet "$r" "not under ${um}/a. but ${hm}/ab, or <tmp>${hm}/.claude" "trailing punctuation and hidden directories do not make a path a user's"
fires "$r" "see ${hm}/jdoe42." "a real-looking name followed by a full stop still fires"

# ---- rule: a 12-digit AWS account id in an ARN or an account context
r=culibox-aws-account-id
real_looking=$(printf '%s%s' 98765432 1098)
fires "$r" "role arn:aws:iam::${real_looking}:role/x" "an ARN with an undocumented account id fires"
fires "$r" "account_id = \"${real_looking}\"" "an account_id assignment with an undocumented id fires"
fires "$r" "\"Account\": \"${real_looking}\"" "a JSON Account field with an undocumented id fires"
quiet "$r" "role arn:aws:iam::$(printf '%s%s' 12345678 9012):role/x" "the documented fake 123456789012 is allowed"
quiet "$r" "account: $(printf '%s%s%s' 1111 1111 1111)" "a repeated-digit fake id is allowed"
quiet "$r" "stamp 2026$(printf '%s' 11121530)00 in a log" "a 12-digit number outside an account context is not flagged"
quiet "$r" "account id 98765432109 is short" "an 11-digit number is not an account id"

# ---- force-push range scan: the private-identifier rules apply to the final tree, not to
# the old commits a rewritten history leaves in the range. Real-secret rules still apply
# to the range. A real throwaway repository, the real scanner, made-up values.
root=$(cd "$here/../.." && pwd)
mkrepo() { # NAME: a repo with the configs and the script; a tagged first commit; sets repo
  repo=$tmp/$1
  mkdir -p "$repo/scripts/ci"
  cp "$root/.gitleaks.toml" "$repo/"
  cp "$root/scripts/ci/gitleaks-range.toml" "$root/scripts/ci/secret-scan.sh" "$repo/scripts/ci/"
  git init -q -b main "$repo"
  echo base >"$repo/README"
  rgit add -A
  rgit commit -q -m base
  rgit tag v0.1.0
}
rgit() { git -C "$repo" -c user.name=t -c user.email=t@example.com -c commit.gpgsign=false -c tag.gpgsign=false "$@"; }
# scan_range BEFORE: runs secret-scan.sh in $repo as a push to main; sets rc
scan_range() {
  if (cd "$repo" && env GIT_CONFIG_GLOBAL=/dev/null GITHUB_EVENT_NAME=push GITHUB_REF=refs/heads/main \
    GITHUB_SHA="$(git rev-parse HEAD)" BEFORE="$1" bash scripts/ci/secret-scan.sh) >"$tmp/range.out" 2>&1; then rc=0; else rc=$?; fi
}
gone=0123456789abcdef0123456789abcdef01234567
private_line="host 100${dot}99${dot}1${dot}1 at ${hm}/jdoe42/work"
token_line="tok $(printf '%s%s%s' ghp_ aZ3kQ9xLm2Pv7R tYw5Nc8BdFh4JsUe6GoXiV)"

echo "== force-push: a private-identifier value only in an old commit"
mkrepo fp1
printf '%s\n' "$private_line" >"$repo/old.txt"
rgit add -A && rgit commit -q -m old
rgit rm -q old.txt && rgit commit -q -m remove
scan_range "$gone"
if [ "$rc" -eq 0 ]; then pass "a force-push passes: the range scan has no private-identifier rules and the final tree is clean"; else fail "the old private value failed the force-push scan: $(tail -5 "$tmp/range.out")"; fi
scan_range "$(rgit rev-parse v0.1.0)"
if [ "$rc" -ne 0 ]; then pass "a normal push of the same commits fails: all rules apply to the pushed range"; else fail "a normal push let a private value in a pushed commit through"; fi

echo "== force-push: the same value in the final tree"
mkrepo fp2
printf '%s\n' "$private_line" >"$repo/now.txt"
rgit add -A && rgit commit -q -m now
scan_range "$gone"
if [ "$rc" -ne 0 ]; then pass "a force-push fails when the final tree holds a private value (full config)"; else fail "the final tree scan missed a private value"; fi

echo "== force-push: a real-secret shape in an old commit"
mkrepo fp3
printf '%s\n' "$token_line" >"$repo/tok.txt"
rgit add -A && rgit commit -q -m tok
rgit rm -q tok.txt && rgit commit -q -m remove
scan_range "$gone"
if [ "$rc" -ne 0 ]; then pass "a force-push still fails on a real-secret rule hit in the range"; else fail "the range scan dropped the real-secret rules"; fi

echo
if [ "$fails" -ne 0 ]; then
  echo "gitleaks-rules_test: $fails check(s) failed" >&2
  exit 1
fi
echo "gitleaks-rules_test: all checks passed"
