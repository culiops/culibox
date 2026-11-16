#!/bin/sh
# Offline test for scripts/release/build-abduco.sh: the refusals that must happen
# before anything downloaded is unpacked or run. A stub curl (BUILD_ABDUCO_CURL)
# serves fixture files, so this needs no network and no zig. The real build, with the
# pinned downloads and the pinned output sums, runs in release.yml.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
script=$here/build-abduco.sh

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
# nothing_unpacked MESSAGE: the stub tar and unzip were never called, and no
# output was written. (The script's work dir is removed on exit, so its contents
# prove nothing; the stubs do.)
nothing_unpacked() {
  if [ -e "$tmp/unpacked.marker" ]; then
    fail "$1 (called: $(cat "$tmp/unpacked.marker"))"
    rm -f "$tmp/unpacked.marker"
  else
    pass "$1"
  fi
}

# The stub curl: `curl -fsSL <url> -o <file>` copies $FIXTURES/<last part of url>,
# or fails like curl -f when there is no such fixture.
mkdir -p "$tmp/bin" "$tmp/fixtures"
cat >"$tmp/bin/curl" <<'EOF'
#!/bin/sh
url= out=
while [ "$#" -gt 0 ]; do
  case $1 in
    -o) out=$2; shift ;;
    -*) ;;
    *) url=$1 ;;
  esac
  shift
done
src=$FIXTURES/${url##*/}
[ -f "$src" ] || { echo "curl: (22) The requested URL returned error: 404" >&2; exit 22; }
cp "$src" "$out"
EOF
chmod 755 "$tmp/bin/curl"
# Stub tar and unzip, on PATH only while the script runs: any unpack is recorded.
mkdir -p "$tmp/unpack"
for tool in tar unzip; do
  printf '#!/bin/sh\necho "%s $*" >>"%s/unpacked.marker"\nexit 1\n' "$tool" "$tmp" >"$tmp/unpack/$tool"
  chmod 755 "$tmp/unpack/$tool"
done
export BUILD_ABDUCO_CURL="$tmp/bin/curl" FIXTURES="$tmp/fixtures"

run() { # OUTDIR: run the script, output to $tmp/out, sets rc
  rc=0
  PATH="$tmp/unpack:$PATH" sh "$script" "$1" >"$tmp/out" 2>&1 || rc=$?
}

# 1. Usage.
rc=0
sh "$script" >"$tmp/out" 2>&1 || rc=$?
if [ "$rc" -eq 2 ]; then pass "no argument exits 2"; else fail "no argument exits $rc, want 2"; fi
contains "$tmp/out" "usage:" "no argument prints the usage"

# 2. An outdir that already holds output is refused.
mkdir -p "$tmp/o2/linux_amd64"
run "$tmp/o2"
expect_fail "$rc" "an outdir that already holds linux_amd64 is refused"
contains "$tmp/out" "use an empty directory" "the refusal says why"

# 3. A download that fails names what it was fetching, and nothing is unpacked.
run "$tmp/o3"
expect_fail "$rc" "a failed download stops the build"
contains "$tmp/out" "download failed" "a failed download is named"
nothing_unpacked "a failed download leaves nothing unpacked"

# 4. Both downloads arrive with the wrong bytes: both are named, and neither is
# unpacked.
mkdir -p "$tmp/src/abduco-0.6"
echo 'int main(void){return 0;}' >"$tmp/src/abduco-0.6/abduco.c"
tar -C "$tmp/src" -czf "$tmp/fixtures/abduco-0.6.tar.gz" abduco-0.6
wheel=$(sed -n 's/^ZIG_WHEEL_URL=.*\/\([^/]*\.whl\)$/\1/p' "$script")
if [ -z "$wheel" ]; then
  fail "the script pins ZIG_WHEEL_URL to a .whl"
  wheel=zig.whl
fi
echo "not a wheel" >"$tmp/fixtures/$wheel"
run "$tmp/o4"
expect_fail "$rc" "wrong download bytes stop the build"
contains "$tmp/out" "abduco-0.6.tar.gz: sha256 mismatch" "the abduco mismatch is named"
contains "$tmp/out" "$wheel: sha256 mismatch" "the zig mismatch is named"
contains "$tmp/out" "nothing was unpacked" "the mismatch says nothing was unpacked"
nothing_unpacked "a mismatch leaves nothing unpacked"

# 5. An empty download is a mismatch too, never an empty build.
: >"$tmp/fixtures/abduco-0.6.tar.gz"
run "$tmp/o5"
expect_fail "$rc" "an empty download stops the build"
contains "$tmp/out" "abduco-0.6.tar.gz: sha256 mismatch" "the empty download is named"

nothing_unpacked "an empty download leaves nothing unpacked"

# 6. make splits CC on spaces, so an outdir with one is refused up front.
run "$tmp/with space"
expect_fail "$rc" "an outdir with a space is refused"
contains "$tmp/out" "has a space" "the refusal says why"

if [ "$fails" -ne 0 ]; then
  echo "build-abduco_test: $fails check(s) failed" >&2
  exit 1
fi
echo "build-abduco_test: all checks passed"
