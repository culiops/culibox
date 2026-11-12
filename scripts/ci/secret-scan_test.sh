#!/usr/bin/env bash
# Offline test for scripts/ci/secret-scan.sh: run it with `bash scripts/ci/secret-scan_test.sh`.
# gitleaks is a stub that records its arguments; the repository is a throwaway one
# in a temp dir. Needs only bash, git and coreutils.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
script=$here/secret-scan.sh
[ -x "$script" ] || {
  echo "secret-scan_test: $script is missing or not executable" >&2
  exit 1
}

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

cat >"$tmp/bin/gitleaks" <<'EOF'
#!/bin/sh
echo "$*" >>"$STUB_LOG"
exit "${STUB_RC:-0}"
EOF
chmod 755 "$tmp/bin/gitleaks"

# A repo with three commits; the middle one is tagged.
repo=$tmp/repo
git init -q -b main "$repo"
gc() { git -C "$repo" -c user.name=t -c user.email=t@example.com -c commit.gpgsign=false -c tag.gpgsign=false "$@"; }
for n in 1 2 3; do
  echo "$n" >"$repo/f"
  gc add f
  gc commit -q -m "c$n"
done
c1=$(git -C "$repo" rev-parse HEAD~2)
c2=$(git -C "$repo" rev-parse HEAD~1)
c3=$(git -C "$repo" rev-parse HEAD)
bare=$tmp/untagged
git clone -q "$repo" "$bare" # a clone without the tag, made before it exists
gc tag v0.1.0 "$c2"
gone=0123456789abcdef0123456789abcdef01234567
zeros=0000000000000000000000000000000000000000

# scan NAME REPO EVENT REF BEFORE [STUB_RC]: runs the script; sets rc, log, err.
scan() {
  local name=$1 dir=$2 event=$3 ref=$4 before=$5
  log=$tmp/$name.log
  err=$tmp/$name.err
  : >"$log"
  if (cd "$dir" && env PATH="$tmp/bin:$PATH" STUB_LOG="$log" STUB_RC="${6:-0}" \
    GITHUB_EVENT_NAME="$event" GITHUB_REF="$ref" GITHUB_SHA="$c3" BEFORE="$before" \
    "$script") >"$tmp/$name.out" 2>"$err"; then rc=0; else rc=$?; fi
}
lines() { grep -c . "$1" || true; }
has() { grep -qF -- "$2" "$1"; }

# 1. a normal push to main scans exactly the commits it adds, and not the tree
scan a "$repo" push refs/heads/main "$c1"
if [ "$rc" -eq 0 ] && [ "$(lines "$log")" = 1 ] && has "$log" "git --config .gitleaks.toml --redact --log-opts=$c1..$c3 ."; then
  pass "a push scans BEFORE..sha only"
else fail "a push must scan BEFORE..sha once: rc=$rc log=$(cat "$log")"; fi

# 2. a new branch (BEFORE all zeros) and an empty BEFORE scan the checkout
for b in "$zeros" ""; do
  scan b "$repo" push refs/heads/main "$b"
  if [ "$rc" -eq 0 ] && [ "$(lines "$log")" = 1 ] && has "$log" "dir --config .gitleaks.toml --redact ."; then
    pass "BEFORE '${b:-empty}' scans the checkout"
  else fail "BEFORE '$b' must scan the checkout once: $(cat "$log")"; fi
done

# 3. a force-push: BEFORE is not in the clone. The range since the newest tag is
#    scanned (a superset of what was pushed), and then the final tree as well.
scan c "$repo" push refs/heads/main "$gone"
if [ "$rc" -eq 0 ] && [ "$(lines "$log")" = 2 ] &&
  has "$log" "git --config .gitleaks.toml --redact --log-opts=v0.1.0..$c3 ." && has "$log" "dir --config .gitleaks.toml --redact ."; then
  pass "a force-push scans the range since the newest tag and the tree"
else fail "a force-push must scan tag..sha and the tree: rc=$rc log=$(cat "$log")"; fi
if has "$tmp/c.out" "not in this clone"; then pass "the force-push is announced"; else fail "no force-push notice: $(cat "$err")"; fi

# 4. a force-push with no tag to anchor the range: tree only, with a warning
scan d "$bare" push refs/heads/main "$gone"
if [ "$rc" -eq 0 ] && [ "$(lines "$log")" = 1 ] && has "$log" "dir --config .gitleaks.toml --redact ." && has "$tmp/d.out" "::warning::"; then
  pass "a force-push with no tag scans the tree and warns"
else fail "no-tag force-push: rc=$rc log=$(cat "$log") err=$(cat "$err")"; fi

# 5. other events and refs scan the checkout
scan e "$repo" workflow_dispatch refs/heads/main "$c1"
scan f "$repo" push refs/tags/v1.0.0 "$zeros"
scan g "$repo" push refs/heads/other "$c1"
for n in e f g; do
  if [ "$(lines "$tmp/$n.log")" = 1 ] && has "$tmp/$n.log" "dir --config .gitleaks.toml --redact ."; then
    pass "case $n scans the checkout"
  else fail "case $n: $(cat "$tmp/$n.log")"; fi
done

# 6. a finding fails the step, on every path
for args in "push refs/heads/main $c1" "push refs/heads/main $gone" "workflow_dispatch refs/heads/main $zeros"; do
  # shellcheck disable=SC2086
  scan h "$repo" $args 1
  if [ "$rc" -ne 0 ]; then pass "a gitleaks failure fails the step ($args)"; else fail "gitleaks exit 1 was swallowed ($args)"; fi
done

# 7. BEFORE reaches git only as 40 hex digits: anything else is treated as unusable
for evil in '--output=/tmp/x' "$c1;touch $tmp/pwned" "$c1..$c3" 'main' "${c1:0:12}"; do
  scan i "$repo" push refs/heads/main "$evil"
  if [ "$rc" -eq 0 ] && ! has "$tmp/i.log" "$evil" && [ ! -e "$tmp/pwned" ]; then
    pass "BEFORE '$evil' never reaches gitleaks or the shell"
  else fail "BEFORE '$evil' was used: $(cat "$tmp/i.log")"; fi
done
# A value of several lines whose first (or last) line is 40 hex digits is not one
# commit id: grep checks line by line, so the gate must match the whole value.
for multi in "$c1"$'\nextra' $'extra\n'"$c1" "$c1"$'\n'; do
  scan j "$repo" push refs/heads/main "$multi"
  if [ "$rc" -eq 0 ] && ! has "$tmp/j.log" "--log-opts" && has "$tmp/j.log" "dir --config .gitleaks.toml --redact ."; then
    pass "a multi-line BEFORE is unusable and the checkout is scanned ($(printf '%s' "$multi" | tr '\n' '|' | cut -c1-30))"
  else fail "a multi-line BEFORE was used: $(cat "$tmp/j.log")"; fi
done

echo
if [ "$fails" -ne 0 ]; then
  echo "secret-scan_test: $fails check(s) failed" >&2
  exit 1
fi
echo "secret-scan_test: all checks passed"
