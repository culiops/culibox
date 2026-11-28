#!/usr/bin/env bash
# Offline test for scripts/ci/gitleaks-install.sh: run it with
#   bash scripts/ci/gitleaks-install_test.sh
# `go` is a stub that records its arguments and writes a fake binary into GOBIN.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
script=$here/gitleaks-install.sh
ci=$here/../../.github/workflows/ci.yml

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin"

fails=0
fail() {
  echo "FAIL: $*" >&2
  fails=$((fails + 1))
}
pass() { echo "ok:   $*"; }

cat >"$tmp/bin/go" <<'STUB'
#!/bin/sh
echo "GOBIN=$GOBIN $*" >>"$STUB_LOG"
[ "${STUB_RC:-0}" = 0 ] || exit "$STUB_RC"
printf '#!/bin/sh\necho fake gitleaks\n' >"$GOBIN/gitleaks"
chmod 755 "$GOBIN/gitleaks"
STUB
chmod 755 "$tmp/bin/go"

run() { env PATH="$tmp/bin:/usr/bin:/bin" XDG_CACHE_HOME="$tmp/cache" STUB_LOG="$tmp/log" "$@" bash "$script"; }

# The pinned version is CI's: the box builds the scanner CI and the host's pre-push gate run.
want=$(sed -n 's/^ *GITLEAKS_VERSION: *"\([0-9.]*\)".*/\1/p' "$ci" | head -1)
got=$(sed -n 's/^version=\([0-9.]*\)$/\1/p' "$script")
if [ -n "$want" ] && [ "$want" = "$got" ]; then pass "version $got = ci.yml GITLEAKS_VERSION"; else fail "version '$got' != ci.yml '$want'"; fi

# First call builds the pinned module version into a versioned cache dir and prints it.
: >"$tmp/log"
dir=$(run) || fail "first install exited non-zero"
if [ "$dir" = "$tmp/cache/culibox/gitleaks-$got" ] && [ -x "$dir/gitleaks" ]; then pass "installed into $dir"; else fail "dir = '$dir'"; fi
if grep -q " install github.com/zricethezav/gitleaks/v8@v$got\$" "$tmp/log"; then pass "go install of the pinned module"; else fail "go args: $(cat "$tmp/log")"; fi
if ! grep -q "GOBIN=$dir " "$tmp/log"; then pass "built into a temp dir, then moved in"; else fail "built straight into the final dir"; fi
if [ -z "$(find "$tmp/cache/culibox" -name '.tmp*' 2>/dev/null)" ]; then pass "no temp dir left"; else fail "temp dir left behind"; fi

# Second call reuses it: go is not run again.
: >"$tmp/log"
dir2=$(run) || fail "second call exited non-zero"
if [ "$dir2" = "$dir" ] && [ ! -s "$tmp/log" ]; then pass "cached: go not run again"; else fail "second call ran go: $(cat "$tmp/log")"; fi

# A failed build fails loudly and leaves nothing that looks installed.
rm -rf "$tmp/cache"
: >"$tmp/log"
if out=$(run STUB_RC=1 2>&1); then fail "a failed go install exited 0"; else pass "a failed build exits non-zero"; fi
case $out in *"could not build gitleaks"*) pass "and says so" ;; *) fail "message: $out" ;; esac
if [ ! -e "$tmp/cache/culibox/gitleaks-$got/gitleaks" ]; then pass "no binary after a failed build"; else fail "binary left after a failed build"; fi

# No go at all: a clear failure, not a silent skip.
if out=$(env PATH="/nonexistent" XDG_CACHE_HOME="$tmp/cache" /bin/bash "$script" 2>&1); then fail "no go exited 0"; else pass "no go exits non-zero"; fi

[ "$fails" -eq 0 ] || { echo "gitleaks-install_test: $fails failure(s)" >&2; exit 1; }
echo "gitleaks-install_test: all passed"
