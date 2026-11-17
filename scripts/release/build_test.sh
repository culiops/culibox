#!/bin/sh
# Offline test for scripts/release/build.sh: what goes into each tarball and into
# SHA256SUMS. A stub go writes a small file per target instead of compiling, and a
# fake abduco dir stands in for build-abduco.sh's output, so this runs in seconds.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
script=$here/build.sh
repo=$(cd "$here/../.." && pwd)

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fails=0
fail() {
  echo "FAIL: $*" >&2
  fails=$((fails + 1))
}
pass() { echo "ok:   $*"; }
contains() { # FILE TEXT MESSAGE
  if grep -qF -- "$2" "$1"; then pass "$3"; else
    fail "$3 (no '$2' in output)"
    sed 's/^/        | /' "$1" >&2
  fi
}
expect_fail() { # RC MESSAGE
  if [ "$1" -ne 0 ]; then pass "$2"; else fail "$2 (exit 0)"; fi
}
no_assets() { # OUTDIR MESSAGE
  left=$(find "$1" -maxdepth 1 \( -name 'culibox_*' -o -name SHA256SUMS -o -name setup.sh \) 2>/dev/null || true)
  if [ -z "$left" ]; then pass "$2"; else fail "$2 (left: $left)"; fi
}

# The stub go: `go build ... -o <file> <pkg>` writes "culibox $GOOS/$GOARCH" to
# <file>; `go version -m` prints no build tags (assert-no-uidemo.sh passes).
mkdir -p "$tmp/bin"
cat >"$tmp/bin/go" <<'EOF'
#!/bin/sh
case $1 in
  build)
    printf '%s\n' "$*" >>"$GO_ARGV"
    while [ "$#" -gt 0 ]; do
      [ "$1" = -o ] && out=$2
      shift
    done
    printf 'culibox %s/%s\n' "$GOOS" "$GOARCH" >"$out"
    ;;
  version) echo "stub: go1.0" ;;
  *) echo "stub go: unexpected $*" >&2; exit 1 ;;
esac
EOF
chmod 755 "$tmp/bin/go"
PATH=$tmp/bin:$PATH
GO_ARGV=$tmp/go-argv
export PATH GO_ARGV

# fake_abduco DIR: the layout build-abduco.sh writes, each abduco naming its target.
fake_abduco() {
  for t in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64; do
    mkdir -p "$1/$t"
    printf 'abduco %s\n' "$t" >"$1/$t/abduco"
    chmod 700 "$1/$t/abduco"
  done
  echo "ISC licence" >"$1/LICENSE.abduco"
}

run() { # ARGS...: sets rc, output in $tmp/out
  rc=0
  sh "$script" "$@" >"$tmp/out" 2>&1 || rc=$?
}

# 1. The abduco dir is required; at most four arguments.
run v1.2.3 "$tmp/o1"
if [ "$rc" -eq 2 ]; then pass "two arguments exit 2"; else fail "two arguments exit $rc, want 2"; fi
run v1.2.3 "$tmp/o1" "$tmp/a1" ref extra
if [ "$rc" -eq 2 ]; then pass "five arguments exit 2"; else fail "five arguments exit $rc, want 2"; fi
contains "$tmp/out" "<abduco-dir>" "the usage names the abduco dir"

# 2. A missing target or licence is refused before any asset is written.
fake_abduco "$tmp/a2"
rm "$tmp/a2/darwin_arm64/abduco"
run v1.2.3 "$tmp/o2" "$tmp/a2"
expect_fail "$rc" "a missing darwin_arm64 abduco is refused"
contains "$tmp/out" "darwin_arm64/abduco" "the refusal names the missing file"
no_assets "$tmp/o2" "nothing is written when an abduco is missing"

fake_abduco "$tmp/a3"
rm "$tmp/a3/LICENSE.abduco"
run v1.2.3 "$tmp/o3" "$tmp/a3"
expect_fail "$rc" "a missing LICENSE.abduco is refused"
no_assets "$tmp/o3" "nothing is written when the licence is missing"

# 3. A symlink is not a regular file: refused.
fake_abduco "$tmp/a4"
rm "$tmp/a4/linux_amd64/abduco"
ln -s "$tmp/a4/linux_arm64/abduco" "$tmp/a4/linux_amd64/abduco"
run v1.2.3 "$tmp/o4" "$tmp/a4"
expect_fail "$rc" "a symlinked abduco is refused"
no_assets "$tmp/o4" "nothing is written for a symlinked abduco"

# 4. A good run: five members per tarball, each abduco from its own target, setup.sh
# next to the tarballs, and SHA256SUMS covering all five assets.
fake_abduco "$tmp/a5"
run v1.2.3 "$tmp/o5" "$tmp/a5"
if [ "$rc" -eq 0 ]; then pass "a good run exits 0"; else
  fail "a good run exits $rc"
  sed 's/^/        | /' "$tmp/out" >&2
fi
for t in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64; do
  tgz=$tmp/o5/culibox_1.2.3_$t.tar.gz
  members=$(tar -tzf "$tgz" 2>/dev/null | LC_ALL=C sort | tr '\n' ' ')
  if [ "$members" = "LICENSE LICENSE.abduco README.md abduco culibox " ]; then pass "$t: five members"; else
    fail "$t: members are '$members'"
  fi
  got=$(tar -xOzf "$tgz" abduco 2>/dev/null || true)
  if [ "$got" = "abduco $t" ]; then pass "$t: holds its own abduco"; else fail "$t: abduco is '$got'"; fi
  mode=$(tar -tvzf "$tgz" abduco 2>/dev/null | cut -c1-10)
  if [ "$mode" = "-rwxr-xr-x" ]; then pass "$t: abduco is 755"; else fail "$t: abduco mode $mode"; fi
done
if cmp -s "$repo/scripts/setup.sh" "$tmp/o5/setup.sh"; then pass "setup.sh is attached as is"; else fail "setup.sh differs from scripts/setup.sh"; fi
if [ -x "$tmp/o5/setup.sh" ]; then pass "setup.sh is executable"; else fail "setup.sh is not executable"; fi
lines=$(wc -l <"$tmp/o5/SHA256SUMS" | tr -d ' ')
if [ "$lines" = 5 ]; then pass "SHA256SUMS has five lines"; else fail "SHA256SUMS has $lines lines"; fi
contains "$tmp/o5/SHA256SUMS" "  setup.sh" "SHA256SUMS lists setup.sh"
if (cd "$tmp/o5" && sha256sum -c SHA256SUMS >/dev/null 2>&1); then pass "SHA256SUMS checks"; else fail "SHA256SUMS does not check"; fi

# 5. Reproducible: a second run gives the same SHA256SUMS.
run v1.2.3 "$tmp/o6" "$tmp/a5"
if cmp -s "$tmp/o5/SHA256SUMS" "$tmp/o6/SHA256SUMS"; then pass "two runs give the same SHA256SUMS"; else fail "two runs differ"; fi

# 6. The box image ref (#335): baked when given, validated before anything is written.
ref=ghcr.io/culiops/culibox@sha256:$(printf 'a%.0s' $(seq 64))
flag="-X github.com/culiops/culibox/docker/agent-box.published=$ref"
: >"$GO_ARGV"
run v1.2.3 "$tmp/o7" "$tmp/a5" "$ref"
if [ "$rc" -eq 0 ]; then pass "a run with a box image ref exits 0"; else fail "a run with a box image ref exits $rc"; fi
n=$(grep -cF -- "$flag" "$GO_ARGV" || true)
if [ "$n" = 4 ]; then pass "all four builds bake the box image ref"; else fail "$n of 4 builds bake the ref"; fi
: >"$GO_ARGV"
run v1.2.3 "$tmp/o8" "$tmp/a5"
if grep -qF "published=" "$GO_ARGV"; then fail "a run without a ref bakes one"; else pass "a run without a ref bakes none"; fi
nl="$ref
-X main.version=evil"
for bad in ghcr.io/culiops/culibox:base "ghcr.io/culiops/culibox@sha256:abc" "$ref -X main.version=evil" "-X" "$nl" \
  "ghcr.io/culiops/culibox@sha256:$(printf 'A%.0s' $(seq 64))" ""; do
  run v1.2.3 "$tmp/o9" "$tmp/a5" "$bad"
  if [ "$rc" -eq 2 ]; then pass "box image ref '$bad' exits 2"; else fail "box image ref '$bad' exits $rc, want 2"; fi
  no_assets "$tmp/o9" "nothing is written for box image ref '$bad'"
done

if [ "$fails" -ne 0 ]; then
  echo "build_test: $fails check(s) failed" >&2
  exit 1
fi
echo "build_test: all checks passed"
