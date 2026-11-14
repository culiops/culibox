#!/usr/bin/env bash
# Offline test for scripts/setup.sh: run it with `bash scripts/setup_test.sh`.
#
# setup.sh runs under dash (`sh`) with PATH reduced to a directory of stubs plus a
# directory of symlinks to the few real tools it may use, so the host's gh, go, git,
# curl and uname can never leak in. The stubs serve a fake release built here (real
# tar + sha256sum):
#   uname   STUB_OS / STUB_ARCH
#   curl    maps the GitHub release URLs onto $C/rel/<tag>/<file>; a miss fails like a 404
#   gh      optional (absent unless a case adds it); STUB_VIS, STUB_ATTEST_RC
#   git/go  optional; `git clone` writes a go.mod, `go build` writes a fake binary
# The fake culibox prints `culibox <tag> (abc)` for `version` and appends `init <args>`
# to $LOG otherwise. Everything lives under the mktemp dir, removed on exit. Nothing
# here touches the network or installs anything outside it.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
script=$here/setup.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
: >"$tmp/fails"
: >"$tmp/passes"

fail() {
  echo "FAIL: $*" >&2
  echo x >>"$tmp/fails"
}
pass() {
  echo "ok:   $*"
  echo x >>"$tmp/passes"
}
# Cases run in subshells (their exports stay theirs), so counts go through files.

SH=$(command -v sh)

# ---- the tools setup.sh may see ----------------------------------------------------
mkdir -p "$tmp/tools"
for t in awk cat chmod cp cut dirname grep gzip head ls mkdir mktemp mv rm sed sha256sum tar tr wc; do
  p=$(command -v "$t") || {
    echo "setup_test: need $t on this machine" >&2
    exit 1
  }
  ln -s "$p" "$tmp/tools/$t"
done
sha256sum_real=$tmp/tools/sha256sum

# ---- stubs ---------------------------------------------------------------------------
mkdir -p "$tmp/stubdefs"
# mkfake VER OUT: a fake culibox binary (shell script).
cat >"$tmp/mkfake" <<'EOF'
#!/bin/sh
cat >"$2" <<FAKE
#!/bin/sh
if [ "\${1:-}" = version ]; then echo "culibox $1 (abc)"; exit 0; fi
echo "init \$*" >>"\$LOG"
FAKE
chmod 755 "$2"
EOF
cat >"$tmp/stubdefs/uname" <<'EOF'
#!/bin/sh
case "${1:-}" in
-s) echo "${STUB_OS:-Linux}" ;;
-m) echo "${STUB_ARCH:-x86_64}" ;;
*) echo "stub uname: unexpected call: $*" >&2; exit 1 ;;
esac
EOF
cat >"$tmp/stubdefs/curl" <<'EOF'
#!/bin/sh
out= url=
prev=
for a in "$@"; do
  [ "$prev" = -o ] && out=$a
  prev=$a
  url=$a
done
echo "curl $url" >>"$STUB_CALLS"
src=
case "$url" in
https://api.github.com/repos/culiops/culibox/releases/latest)
  if [ -f "$STUB_REL/LATEST" ]; then
    printf '{\n  "url": "x",\n  "tag_name": "%s",\n  "name": "n"\n}\n' "$(cat "$STUB_REL/LATEST")"
    exit 0
  fi
  ;;
https://github.com/culiops/culibox/releases/download/*)
  rest=${url#https://github.com/culiops/culibox/releases/download/}
  src=$STUB_REL/$rest
  ;;
*) echo "stub curl: unexpected url: $url" >&2; exit 1 ;;
esac
if [ -z "$src" ] || [ ! -f "$src" ]; then
  echo "curl: (22) The requested URL returned error: 404" >&2
  exit 22
fi
if [ -n "$out" ]; then cp "$src" "$out"; else cat "$src"; fi
EOF
cat >"$tmp/stubdefs/gh" <<'EOF'
#!/bin/sh
echo "gh $*" >>"$STUB_CALLS"
case "$1 $2" in
"auth status") [ -z "${STUB_GH_UNAUTHED:-}" ] ;;
"release view")
  [ -f "$STUB_REL/LATEST" ] || { echo "release not found" >&2; exit 1; }
  cat "$STUB_REL/LATEST"
  ;;
"release download")
  tag=$3 pat= dir=
  prev=
  for a in "$@"; do
    [ "$prev" = --pattern ] && pat=$a
    [ "$prev" = --dir ] && dir=$a
    prev=$a
  done
  [ -f "$STUB_REL/$tag/$pat" ] || { echo "no assets match" >&2; exit 1; }
  cp "$STUB_REL/$tag/$pat" "$dir/$pat"
  ;;
"repo view") echo "${STUB_VIS:-PUBLIC}" ;;
"attestation --help") [ -z "${STUB_GH_NO_ATTEST:-}" ] ;;
"attestation verify") exit "${STUB_ATTEST_RC:-0}" ;;
*) echo "stub gh: unexpected call: $*" >&2; exit 1 ;;
esac
EOF
cat >"$tmp/stubdefs/git" <<'EOF'
#!/bin/sh
echo "git $*" >>"$STUB_CALLS"
if [ "$1" = clone ]; then
  for a in "$@"; do dest=$a; done
  mkdir -p "$dest/cmd/culibox"
  printf 'module example.com/culibox\n\ngo %s\n' "${STUB_GOMOD:-1.26.5}" >"$dest/go.mod"
  exit 0
fi
if [ "$1" = -C ] && [ "$3" = rev-parse ]; then echo abc1234; exit 0; fi
echo "stub git: unexpected call: $*" >&2
exit 1
EOF
cat >"$tmp/stubdefs/go" <<'EOF'
#!/bin/sh
case "$1" in
env) echo "${STUB_GOVERSION:-go1.26.5}" ;;
version) echo "go version ${STUB_GOVERSION:-go1.26.5} linux/amd64" ;;
build)
  echo "go cgo=${CGO_ENABLED:-} cwd=$PWD $*" >>"$STUB_CALLS"
  out= flags= prev=
  for a in "$@"; do
    [ "$prev" = -o ] && out=$a
    [ "$prev" = -ldflags ] && flags=$a
    prev=$a
  done
  ver=$(echo "$flags" | sed 's/.*main\.version=\([^ ]*\).*/\1/')
  "$STUB_MKFAKE" "$ver" "$out"
  ;;
*) echo "stub go: unexpected call: $*" >&2; exit 1 ;;
esac
EOF
# Without a working sha256sum on PATH, setup.sh must fall back to shasum -a 256.
cat >"$tmp/stubdefs/shasum" <<EOF
#!/bin/sh
[ "\$1" = -a ] && [ "\$2" = 256 ] || { echo "stub shasum: unexpected call: \$*" >&2; exit 1; }
shift 2
echo shasum >>"\$STUB_CALLS"
exec "$sha256sum_real" "\$@"
EOF
chmod +x "$tmp/mkfake" "$tmp"/stubdefs/*

# ---- case plumbing ---------------------------------------------------------------------
# newcase NAME: a fresh sandbox; sets C, PFX (the default install dir, not yet created).
newcase() {
  C=$tmp/$1
  rm -rf "$C"
  mkdir -p "$C/stubs" "$C/other" "$C/home" "$C/tmp" "$C/rel" "$C/tools"
  cp -P "$tmp"/tools/* "$C/tools/"
  PFX=$C/home/.local/bin
  : >"$C/calls"
  use uname
  use curl
  export STUB_REL=$C/rel STUB_CALLS=$C/calls LOG=$C/log STUB_MKFAKE=$tmp/mkfake
  echo "-- $1"
}
use() { # STUB...: put stubs on the case's PATH
  for s in "$@"; do ln -s "$tmp/stubdefs/$s" "$C/stubs/$s"; done
}
# mkrelease VER [latest]: a fake release for every platform, with a real SHA256SUMS.
mkrelease() {
  local ver=$1 n=${1#v} os arch
  mkdir -p "$C/rel/$ver" "$C/stage"
  "$tmp/mkfake" "$ver" "$C/stage/culibox"
  for os in linux darwin; do
    for arch in amd64 arm64; do
      tar -C "$C/stage" -czf "$C/rel/$ver/culibox_${n}_${os}_${arch}.tar.gz" culibox
    done
  done
  (cd "$C/rel/$ver" && sha256sum culibox_*.tar.gz >SHA256SUMS)
  if [ "${2:-}" = latest ]; then echo "$ver" >"$C/rel/LATEST"; fi
}
# installed VER [DIR]: a fake culibox already in the install dir.
installed() {
  mkdir -p "${2:-$PFX}"
  "$tmp/mkfake" "$1" "${2:-$PFX}/culibox"
}
# run_setup ARGS...: setup.sh under dash with the reduced PATH. Sets rc; output in $C/out.
run_setup() {
  rc=0
  env -i PATH="$C/stubs:$C/other:$C/tools${EXTRA_PATH:+:$EXTRA_PATH}" HOME="$C/home" TMPDIR="$C/tmp" \
    LOG="$LOG" STUB_REL="$STUB_REL" STUB_CALLS="$STUB_CALLS" STUB_MKFAKE="$STUB_MKFAKE" \
    STUB_OS="${STUB_OS:-Linux}" STUB_ARCH="${STUB_ARCH:-x86_64}" STUB_VIS="${STUB_VIS:-}" \
    STUB_ATTEST_RC="${STUB_ATTEST_RC:-0}" STUB_GOVERSION="${STUB_GOVERSION:-go1.26.5}" \
    STUB_GOMOD="${STUB_GOMOD:-1.26.5}" STUB_GH_UNAUTHED="${STUB_GH_UNAUTHED:-}" \
    STUB_GH_NO_ATTEST="${STUB_GH_NO_ATTEST:-}" \
    "$SH" "$script" "$@" >"$C/out" 2>&1 </dev/null || rc=$?
}
show() { sed 's/^/        | /' "$C/out" | tail -12 >&2 || true; }

ok_rc() { # MESSAGE
  if [ "$rc" -eq 0 ]; then pass "$1"; else
    fail "$1 (exit $rc, want 0)"
    show
  fi
}
bad_rc() { # MESSAGE
  if [ "$rc" -ne 0 ]; then pass "$1"; else
    fail "$1 (exit 0, want nonzero)"
    show
  fi
}
says() { # PATTERN MESSAGE: the output matches the fixed string
  if grep -qF -- "$1" "$C/out"; then pass "$2"; else
    fail "$2 (output lacks '$1')"
    show
  fi
}
silent() { # PATTERN MESSAGE: the output does not match the fixed string
  if grep -qF -- "$1" "$C/out"; then fail "$2 (output has '$1')"; else pass "$2"; fi
}
nothing_installed() { # MESSAGE
  if [ -z "$(ls -A "$PFX" 2>/dev/null)" ]; then pass "$1"; else fail "$1 ($PFX holds: $(ls -A "$PFX"))"; fi
}
version_of() { # BINARY -> its version tag
  "$1" version | sed -n 's/^culibox \([^ ]*\) .*/\1/p'
}
installed_is() { # VER MESSAGE
  local got=none
  [ -x "$PFX/culibox" ] && got=$(version_of "$PFX/culibox")
  if [ "$got" = "$1" ]; then pass "$2"; else fail "$2 (installed is $got, want $1)"; fi
}
init_ran() { # MESSAGE
  if grep -q '^init' "$LOG" 2>/dev/null; then pass "$1"; else fail "$1 (no init in $LOG)"; show; fi
}
init_not_run() { # MESSAGE
  if [ -s "$LOG" ]; then fail "$1 ($(cat "$LOG"))"; else pass "$1"; fi
}
called() { # PATTERN MESSAGE: a stub was called with this
  if grep -q -- "$1" "$C/calls"; then pass "$2"; else fail "$2 (no call matching '$1')"; fi
}
not_called() { # PATTERN MESSAGE
  if grep -q -- "$1" "$C/calls"; then fail "$2 ($(grep -- "$1" "$C/calls" | head -1))"; else pass "$2"; fi
}

# MIN_VERSION is read from the script, so bumping it on tagging does not break the test.
min=$(sed -n 's/^MIN_VERSION="\(v[0-9.]*\)".*/\1/p' "$script" 2>/dev/null || true)
min=${min:-v0.35.0}
mmaj=${min#v}
mmaj=${mmaj%%.*}
mmin=${min#v*.}
mmin=${mmin%%.*}
newer=v$mmaj.$((mmin + 1)).0

# ---- cases -----------------------------------------------------------------------------
case_happy() {
  newcase happy
  mkrelease "$min" latest
  run_setup
  ok_rc "default run succeeds"
  installed_is "$min" "the newest release is installed in the default prefix"
  init_ran "init is exec'd"
  says "==> " "each step is announced with ==>"
}
case_default_prefix() {
  newcase default_prefix
  mkrelease "$min" latest
  run_setup
  if [ -x "$C/home/.local/bin/culibox" ]; then pass "default prefix is \$HOME/.local/bin"; else
    fail "default prefix is \$HOME/.local/bin (not installed there)"
    show
  fi
}
case_tampered() {
  newcase tampered
  mkrelease "$min" latest
  printf 'X' | dd of="$C/rel/$min/culibox_${min#v}_linux_amd64.tar.gz" bs=1 seek=24 conv=notrunc 2>/dev/null
  run_setup
  bad_rc "a tampered tarball is refused"
  nothing_installed "nothing is installed after a checksum failure"
  says "checksum" "the failure names the checksum"
  init_not_run "init does not run"
}
case_missing_sum_entry() {
  newcase missing_sum
  mkrelease "$min" latest
  grep -v linux_amd64 "$C/rel/$min/SHA256SUMS" >"$C/sums" || true
  cp "$C/sums" "$C/rel/$min/SHA256SUMS"
  run_setup
  bad_rc "a SHA256SUMS without the tarball is refused"
  nothing_installed "nothing is installed without a checksum entry"
  says "checksum" "the failure names the checksum"
}
case_no_release() {
  newcase no_release
  run_setup
  bad_rc "no published release fails"
  says "Use --from-source" "the message points at --from-source"
  silent "404" "no raw 404 reaches the user"
}
case_below_min() {
  newcase below_min
  mkrelease v0.9.0 latest
  run_setup
  bad_rc "a release below MIN_VERSION is refused"
  says "predates" "the message says the release predates init"
  nothing_installed "nothing is installed"
  not_called "releases/download" "nothing is downloaded"
}
case_numeric_version_compare() {
  newcase numeric_compare
  mkrelease v0.100.0 latest
  run_setup
  ok_rc "v0.100.0 is not below $min (numeric, not string, comparison)"
  installed_is v0.100.0 "v0.100.0 is installed"
}
case_explicit_version() {
  newcase explicit_version
  mkrelease "$min" latest
  mkrelease "$newer"
  run_setup --version "$min"
  ok_rc "--version installs that release"
  installed_is "$min" "the pinned version is installed, not the newest"
  not_called "releases/latest" "a pinned version asks no one for the newest"
}
case_bad_version_format() {
  newcase bad_version
  mkrelease "$min" latest
  run_setup --version 0.34
  bad_rc "a malformed --version is refused"
  says "vX.Y.Z" "the message shows the format"
  nothing_installed "nothing is installed"
}
case_same_version() {
  newcase same_version
  mkrelease "$min" latest
  installed "$min"
  run_setup
  ok_rc "an up-to-date install is fine"
  not_called "releases/download" "the tarball is not downloaded again"
  init_ran "init is still exec'd"
  if [ -e "$PFX/culibox.prev" ]; then fail "no .prev for the same version"; else pass "no .prev for the same version"; fi
}
case_upgrade_keeps_prev() {
  newcase upgrade
  mkrelease "$min" latest
  installed v0.0.1
  run_setup
  ok_rc "an upgrade succeeds"
  installed_is "$min" "the new version is installed"
  if [ -x "$PFX/culibox.prev" ] && [ "$(version_of "$PFX/culibox.prev")" = v0.0.1 ]; then
    pass "the old binary is kept as culibox.prev"
  else fail "the old binary is kept as culibox.prev"; fi
  says "rollback: mv -f" "the rollback line is printed"
  says "$PFX/culibox.prev" "the rollback line names the .prev path"
  says "docs/UPGRADING.md" "the rollback points at the data restore"
  if [ -e "$PFX/culibox.new" ]; then fail "no culibox.new is left behind"; else pass "no culibox.new is left behind"; fi
}
case_unsupported_arch() {
  newcase arch
  mkrelease "$min" latest
  STUB_ARCH=riscv64
  run_setup
  bad_rc "an unsupported architecture is refused"
  says "unsupported" "the message says unsupported"
  nothing_installed "nothing is installed"
}
case_unsupported_os() {
  newcase os
  mkrelease "$min" latest
  STUB_OS=FreeBSD
  run_setup
  bad_rc "an unsupported OS is refused"
  says "unsupported" "the message says unsupported"
}
case_arch_names() {
  newcase arch_names
  mkrelease "$min" latest
  STUB_OS=Darwin STUB_ARCH=arm64
  run_setup
  ok_rc "darwin/arm64 resolves"
  called "culibox_${min#v}_darwin_arm64.tar.gz" "the darwin_arm64 tarball is the one fetched"
  newcase arch_names2
  mkrelease "$min" latest
  STUB_OS=Linux STUB_ARCH=aarch64
  run_setup
  ok_rc "linux/aarch64 resolves"
  called "culibox_${min#v}_linux_arm64.tar.gz" "aarch64 maps to arm64"
}
case_prefix() {
  newcase prefix
  mkrelease "$min" latest
  run_setup --prefix "$C/bin2"
  ok_rc "--prefix is accepted"
  if [ -x "$C/bin2/culibox" ]; then pass "installed under --prefix"; else fail "installed under --prefix"; fi
  if [ -e "$PFX" ]; then fail "nothing is written to the default prefix"; else pass "nothing is written to the default prefix"; fi
}
case_shadowed_on_path() {
  newcase shadowed
  mkrelease "$min" latest
  installed v9.9.9 "$C/other"
  EXTRA_PATH=$PFX
  run_setup
  ok_rc "the install succeeds"
  says "$C/other/culibox" "the shadowing culibox on PATH is named"
}
case_prefix_not_on_path() {
  newcase not_on_path
  mkrelease "$min" latest
  run_setup
  says "export PATH=\"$PFX:\$PATH\"" "a PATH hint is printed when the prefix is not on PATH"
  newcase on_path
  mkrelease "$min" latest
  EXTRA_PATH=$PFX
  run_setup
  silent "export PATH=" "no PATH hint when the prefix is on PATH"
}
case_shasum_fallback() {
  newcase shasum
  mkrelease "$min" latest
  use shasum
  rm "$C/tools/sha256sum"
  run_setup
  ok_rc "shasum -a 256 is used when sha256sum is missing"
  called shasum "shasum was the hash tool"
  installed_is "$min" "the tarball verified and installed"
}
case_no_hash_tool() {
  newcase nohash
  mkrelease "$min" latest
  rm "$C/tools/sha256sum"
  run_setup
  bad_rc "no hash tool at all fails closed"
  nothing_installed "nothing is installed"
}
case_from_source() {
  newcase from_source
  use git go
  run_setup --from-source --version "$min"
  ok_rc "--from-source builds and installs"
  installed_is "$min" "the built binary is installed"
  init_ran "init is exec'd"
  if grep -q "^go cgo=0 cwd=$C/tmp/[^ ]*/src .*-trimpath .*main.version=$min .*main.commit=abc1234 .*-o $C/tmp/[^/ ]*/culibox ./cmd/culibox" "$C/calls"; then
    pass "go build: CGO off, trimpath, version and clone commit stamped, output in the work dir"
  else
    fail "go build line is wrong: $(grep '^go ' "$C/calls" || echo none)"
  fi
  called "clone .*--depth 1 .*--branch $min" "a shallow clone of the tag"
  if ls -d "$C/tmp"/* >/dev/null 2>&1; then fail "the work dir is removed on exit ($(ls "$C/tmp"))"; else pass "the work dir is removed on exit"; fi
}
case_from_source_resolves_version() {
  newcase from_source_latest
  use git go
  mkrelease "$min" latest
  run_setup --from-source
  ok_rc "--from-source without --version uses the newest release"
  installed_is "$min" "that version is built"
  newcase from_source_below_min
  use git go
  mkrelease v0.9.0 latest
  run_setup --from-source
  bad_rc "--from-source honours MIN_VERSION"
  says "predates" "and says why"
  not_called "^git clone" "nothing is cloned"
}
case_from_source_no_go_linux() {
  newcase no_go
  use git
  run_setup --from-source --version "$min"
  bad_rc "no go on linux fails"
  says "https://go.dev/dl/" "the official Go link is printed"
  nothing_installed "nothing is installed"
}
case_from_source_old_go() {
  newcase old_go
  use git go
  STUB_GOVERSION=go1.20.1
  run_setup --from-source --version "$min"
  bad_rc "a go older than go.mod asks for is refused"
  says "https://go.dev/dl/" "the official Go link is printed"
  says "1.26.5" "the needed version is named"
  nothing_installed "nothing is installed"
}
case_from_source_no_git() {
  newcase no_git
  use go
  run_setup --from-source --version "$min"
  bad_rc "no git fails"
  says "git" "git is named"
}
case_from_source_darwin_no_go_no_tty() {
  newcase darwin_no_go
  use git
  cat >"$C/stubs/brew" <<'EOF'
#!/bin/sh
echo "brew $*" >>"$STUB_CALLS"
EOF
  chmod +x "$C/stubs/brew"
  STUB_OS=Darwin STUB_ARCH=arm64
  if ! command -v setsid >/dev/null 2>&1; then
    echo "SKIP darwin brew prompt without a terminal (no setsid)"
    return 0
  fi
  # A new session has no controlling terminal, so /dev/tty cannot be opened: an answer
  # that cannot be read must count as no.
  rc=0
  setsid -w env -i PATH="$C/stubs:$C/other:$C/tools" HOME="$C/home" TMPDIR="$C/tmp" LOG="$LOG" \
    STUB_REL="$STUB_REL" STUB_CALLS="$STUB_CALLS" STUB_MKFAKE="$STUB_MKFAKE" STUB_OS=Darwin STUB_ARCH=arm64 \
    "$SH" "$script" --from-source --version "$min" >"$C/out" 2>&1 </dev/null || rc=$?
  bad_rc "darwin without go and without a terminal stops"
  not_called "^brew" "brew is never run without a typed y"
  says "brew install go" "the exact command is printed"
}
case_from_source_darwin_no_go_no_brew() {
  newcase darwin_no_brew
  use git
  STUB_OS=Darwin STUB_ARCH=arm64
  run_setup --from-source --version "$min"
  bad_rc "darwin without go and without brew stops"
  says '/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"' "the official Homebrew line is printed"
  says "https://brew.sh" "with the brew.sh link"
  not_called "^brew" "brew is never run"
  not_called "^curl" "curl is never run"
  nothing_installed "nothing is installed"
}
case_attest_public_fail() {
  newcase attest_fail
  mkrelease "$min" latest
  use gh
  STUB_VIS=PUBLIC STUB_ATTEST_RC=1
  run_setup
  bad_rc "a failed attestation on a public repo is fatal"
  says "rejected culibox_${min#v}_linux_amd64.tar.gz;" "the message names the rejected tarball"
  nothing_installed "nothing is installed"
  called "attestation verify .*--repo culiops/culibox" "gh attestation verify ran against the repo"
  init_not_run "init does not run"
}
case_attest_public_pass() {
  newcase attest_pass
  mkrelease "$min" latest
  use gh
  STUB_VIS=PUBLIC STUB_ATTEST_RC=0
  run_setup
  ok_rc "a verified attestation installs"
  installed_is "$min" "installed"
  silent "provenance not checked" "no 'not checked' line when it was checked"
}
case_attest_private_skips() {
  newcase attest_private
  mkrelease "$min" latest
  use gh
  STUB_VIS=PRIVATE
  run_setup
  ok_rc "a private repo installs"
  says "skipped: attestations" "the skip is printed"
  installed_is "$min" "installed"
  called "release download $min" "gh release download fetched the files"
  not_called "^curl .*releases/download" "curl was not used for the files"
  not_called "attestation verify" "no attestation check on a private repo"
}
case_attest_public_no_gh() {
  newcase attest_no_gh
  mkrelease "$min" latest
  run_setup
  ok_rc "no gh: the checksum alone installs"
  says "provenance not checked" "the missing provenance check is visible"
  installed_is "$min" "installed"
}
case_gh_unauthed_is_absent() {
  newcase gh_unauthed
  mkrelease "$min" latest
  use gh
  STUB_GH_UNAUTHED=1
  run_setup
  ok_rc "an unauthenticated gh is treated as absent"
  says "provenance not checked" "and the missing provenance check is visible"
  not_called "release download" "files come from curl"
  not_called "attestation verify" "no attestation call"
}
case_gh_without_attestation() {
  newcase gh_old
  mkrelease "$min" latest
  use gh
  STUB_GH_NO_ATTEST=1 STUB_VIS=PUBLIC
  run_setup
  ok_rc "a gh without the attestation command installs on the checksum"
  says "provenance not checked" "and says so"
  not_called "attestation verify" "no attestation call"
}
case_gh_resolves_latest() {
  newcase gh_latest
  mkrelease "$min" latest
  use gh
  STUB_VIS=PRIVATE
  run_setup
  called "release view --repo culiops/culibox --json tagName" "an authed gh resolves the newest release"
  not_called "api.github.com" "no api.github.com call when gh did it"
}
case_no_sudo() {
  newcase hygiene
  if grep -En '(^|[^[:alnum:]_.-])sudo([^[:alnum:]_-]|$)' "$script" | grep -v '^[0-9]*:[[:space:]]*#' | grep -v 'no sudo' >"$C/hits"; then
    fail "setup.sh mentions sudo outside comments: $(head -1 "$C/hits")"
  else pass "setup.sh never runs sudo"; fi
}
case_shellcheck() {
  if command -v shellcheck >/dev/null 2>&1; then
    if shellcheck -s sh "$script" >"$tmp/sc.out" 2>&1; then pass "shellcheck -s sh is clean"; else
      fail "shellcheck -s sh found problems"
      sed 's/^/        | /' "$tmp/sc.out" | head -30 >&2
    fi
  else
    echo "SKIP shellcheck (not installed)"
  fi
}

for c in $(declare -F | awk '$3 ~ /^case_/ {print $3}'); do
  echo "== $c"
  # A subshell: a case's exports (STUB_*, EXTRA_PATH) must not leak into the next.
  ( "$c" ) || fail "$c aborted"
done

nfail=$(wc -l <"$tmp/fails" | tr -d ' ')
npass=$(wc -l <"$tmp/passes" | tr -d ' ')
echo
if [ "$nfail" -ne 0 ]; then
  echo "setup_test: $nfail failed, $npass passed" >&2
  exit 1
fi
echo "setup_test: all $npass checks passed"
