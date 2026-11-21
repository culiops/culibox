#!/bin/sh
# Offline test for scripts/ci/install-grype.sh: the refusals that must happen
# before anything downloaded is unpacked or run. A stub curl
# (INSTALL_GRYPE_CURL) serves a fixture tarball whose sum is not the pinned one,
# and a stub uname (INSTALL_GRYPE_UNAME) plays other platforms. The real install,
# with the pinned download, runs in box-image.yml and box-image-scan.yml.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
script=$here/install-grype.sh

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fails=0
fail() {
  echo "FAIL: $*" >&2
  fails=$((fails + 1))
}
pass() { echo "ok:   $*"; }

# A tar on PATH that only records it was called: nothing may be unpacked before
# the sum is checked.
mkdir -p "$tmp/bin"
cat >"$tmp/bin/tar" <<EOF
#!/bin/sh
echo tar >>"$tmp/unpacked.marker"
exit 1
EOF
chmod +x "$tmp/bin/tar"
printf '#!/bin/sh\necho "Version: 0.121.2"\n' >"$tmp/grype"
(cd "$tmp" && /bin/tar -czf fixture.tar.gz grype)
cat >"$tmp/curl" <<EOF
#!/bin/sh
echo curl >>"$tmp/curl.marker"
while [ "\$#" -gt 0 ]; do
  if [ "\$1" = -o ]; then cp "$tmp/fixture.tar.gz" "\$2"; fi
  shift
done
EOF
chmod +x "$tmp/curl"
uname_as() { # OS ARCH: a stub uname for -s and -m
  # shellcheck disable=SC2016 # $1 belongs to the stub, not to this shell
  printf '#!/bin/sh\ncase "$1" in -s) echo %s ;; -m) echo %s ;; esac\n' "$1" "$2" >"$tmp/uname"
  chmod +x "$tmp/uname"
}
run() { # prints the exit code; output in $tmp/out
  rc=0
  PATH="$tmp/bin:$PATH" INSTALL_GRYPE_CURL="$tmp/curl" INSTALL_GRYPE_UNAME="$tmp/uname" \
    sh "$script" "$@" >"$tmp/out" 2>&1 || rc=$?
  echo "$rc"
}
untouched() { # MESSAGE: no download, no unpack
  if [ -e "$tmp/curl.marker" ] || [ -e "$tmp/unpacked.marker" ]; then
    fail "$1 (called: $(cat "$tmp/curl.marker" "$tmp/unpacked.marker" 2>/dev/null | tr '\n' ' '))"
    rm -f "$tmp/curl.marker" "$tmp/unpacked.marker"
  else
    pass "$1"
  fi
}

uname_as Linux x86_64
rc=$(run)
if [ "$rc" -eq 2 ]; then pass "no prefix: usage, exit 2"; else fail "no prefix: exit $rc, want 2"; fi
untouched "no prefix: nothing downloaded"

uname_as Linux aarch64
rc=$(run "$tmp/prefix")
if [ "$rc" -ne 0 ] && grep -q 'linux/amd64 only' "$tmp/out"; then pass "arm64 refused"; else fail "arm64: exit $rc: $(cat "$tmp/out")"; fi
untouched "arm64: nothing downloaded"

uname_as Darwin arm64
rc=$(run "$tmp/prefix")
if [ "$rc" -ne 0 ]; then pass "darwin refused"; else fail "darwin: exit 0"; fi
untouched "darwin: nothing downloaded"

uname_as Linux x86_64
rc=$(run "$tmp/prefix")
if [ "$rc" -ne 0 ] && grep -q 'sha256 mismatch' "$tmp/out"; then pass "wrong sum refused"; else fail "wrong sum: exit $rc: $(cat "$tmp/out")"; fi
if [ -e "$tmp/unpacked.marker" ]; then fail "wrong sum: tar ran before the check"; else pass "wrong sum: nothing unpacked"; fi
if [ -e "$tmp/prefix/bin/grype" ]; then fail "wrong sum: grype installed"; else pass "wrong sum: nothing installed"; fi

if grep -q '^GRYPE_VERSION=0\.121\.2$' "$script"; then pass "pins Grype 0.121.2"; else fail "GRYPE_VERSION is not 0.121.2"; fi
if grep -q '^GRYPE_SHA256=9c8965ced9df657e5e851c14021bae838802f711edd7a48f9bc3c923a8d28f12$' "$script"; then
  pass "pins the linux_amd64 sha256"
else
  fail "GRYPE_SHA256 is not the pinned linux_amd64 sum"
fi

if [ "$fails" -ne 0 ]; then
  echo "install-grype_test: $fails failure(s)" >&2
  exit 1
fi
echo "install-grype_test: all passed"
