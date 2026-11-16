#!/bin/sh
# Build the abduco every release tarball carries next to culibox (#317):
#
#   scripts/release/build-abduco.sh <outdir>
#
# leaves <outdir>/<os>_<arch>/abduco for linux_amd64, linux_arm64, darwin_amd64 and
# darwin_arm64, plus <outdir>/LICENSE.abduco (abduco's ISC licence, from the same
# source). scripts/release/build.sh packs them.
#
# One pinned zig cross-compiles all four on a linux/amd64 host: static musl binaries
# for Linux, so they run on any glibc or musl distro, and macOS 13+ binaries for
# darwin (the arm64 one carries the ad-hoc signature macOS requires). The Linux
# binaries contain musl, so LICENSE.abduco also carries musl's MIT notice (from the
# pinned zig). zig ships
# forkpty in its libSystem stub but no <util.h>, so the darwin build gets the one
# declaration from abduco-darwin/util.h. _DARWIN_C_SOURCE: see install-abduco.sh.
#
# Pinned three times over, and every download is checked BEFORE it is unpacked or
# run:
#   - the abduco source: ABDUCO_VERSION / ABDUCO_SHA256 are the Dockerfile's ARGs and
#     install-abduco.sh's (docker/agent-box/workflow_test.go fails if they drift);
#   - zig: the PyPI wheel the zig project publishes, by sha256;
#   - the four binaries: the build is reproducible (no build path or date ends up in
#     a stripped binary), so each one must match the sum below. A different sum means a
#     different zig, source or flag, and fails the release instead of shipping it.
# To bump either pin: change it, run this script, check the four new sums on a second
# machine, and copy them below.
set -eu

ABDUCO_VERSION=0.6
ABDUCO_SHA256=c90909e13fa95770b5afc3b59f311b3d3d2fdfae23f9569fa4f96a3e192a35f4
ZIG_VERSION=0.17.1
ZIG_WHEEL_URL=https://files.pythonhosted.org/packages/59/e3/2a34676370a11c36b7cd04ffb2439c1089c0530cf4b01408c77cf7f8d9c3/ziglang-0.17.1-py3-none-manylinux_2_12_x86_64.manylinux2010_x86_64.musllinux_1_1_x86_64.whl
ZIG_WHEEL_SHA256=ea6cc5d87b033cf0aedfbc8f9549a908cae6c50ac31f1698188fa48a74524b54
OUT_SHA256_linux_amd64=fc58beb240d5aaa05cedc42bb4121dcd3ee1bb7887827a49c18e32db8d433a89
OUT_SHA256_linux_arm64=e61eff8be545992808eb24ab1d012e83485e8a70b3fc4f2b398ab0ed1a4f628c
OUT_SHA256_darwin_amd64=1a163fffd96e00231918297e0fdc713920b0ce7f3bad041f5659ef31c97bbf8c
OUT_SHA256_darwin_arm64=9df958e476d497815d4d8052cead79db2f1f2b6eba8cac7672641d5d8c639b14

die() {
  echo "build-abduco: $*" >&2
  exit 1
}

if [ "$#" -ne 1 ] || [ -z "$1" ]; then
  echo "usage: $0 <outdir>" >&2
  exit 2
fi
here=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$1"
outdir=$(cd "$1" && pwd)
# make splits CC and CFLAGS on whitespace, and both carry these paths.
for p in "$outdir" "$here"; do
  case $p in
    *[[:space:]]*) die "$p has a space; make splits the compiler command on it, so use a path without one" ;;
  esac
done
for name in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64 LICENSE.abduco; do
  if [ -e "$outdir/$name" ]; then
    die "$outdir already holds $name; use an empty directory"
  fi
done

if command -v sha256sum >/dev/null 2>&1; then
  sum() { sha256sum "$1" | cut -d ' ' -f 1; }
elif command -v shasum >/dev/null 2>&1; then
  sum() { shasum -a 256 "$1" | cut -d ' ' -f 1; }
else
  die "neither sha256sum nor shasum is available"
fi

# Inside outdir, never /tmp (a small tmpfs in a box), and removed on exit.
work=$(mktemp -d "$outdir/.abduco-work.XXXXXX")
trap 'rm -rf "$work"' EXIT INT TERM

# BUILD_ABDUCO_CURL is for the offline test only (a stub that serves fixtures).
fetch() { # URL FILE
  ${BUILD_ABDUCO_CURL:-curl} -fsSL "$1" -o "$2" || die "download failed: $1"
}
abduco_tgz=$work/abduco-$ABDUCO_VERSION.tar.gz
zig_whl=$work/${ZIG_WHEEL_URL##*/}
fetch "https://github.com/martanne/abduco/releases/download/v${ABDUCO_VERSION}/abduco-${ABDUCO_VERSION}.tar.gz" "$abduco_tgz"
fetch "$ZIG_WHEEL_URL" "$zig_whl"

# Both are checked and both mismatches reported before anything is unpacked.
bad=0
check() { # FILE WANT
  got=$(sum "$1")
  if [ "$got" != "$2" ]; then
    echo "build-abduco: ${1##*/}: sha256 mismatch: got $got, want $2" >&2
    bad=1
  fi
}
check "$abduco_tgz" "$ABDUCO_SHA256"
check "$zig_whl" "$ZIG_WHEEL_SHA256"
[ "$bad" -eq 0 ] || die "a download does not match its pin; nothing was unpacked"

# The wheel holds a linux/amd64 zig.
[ "$(uname -s)/$(uname -m)" = Linux/x86_64 ] || die "needs a linux/amd64 host (the pinned zig is the x86_64 Linux build)"
command -v unzip >/dev/null 2>&1 || die "unzip is not installed"
command -v make >/dev/null 2>&1 || die "make is not installed"

unzip -q "$zig_whl" 'ziglang/*' -d "$work/zig"
zig=$work/zig/ziglang/zig
chmod 755 "$zig"
[ "$("$zig" version)" = "$ZIG_VERSION" ] || die "the zig in the wheel is not $ZIG_VERSION"
# zig's caches stay in the work dir: nothing from an earlier build is reused.
ZIG_GLOBAL_CACHE_DIR=$work/zig-cache
ZIG_LOCAL_CACHE_DIR=$work/zig-cache
export ZIG_GLOBAL_CACHE_DIR ZIG_LOCAL_CACHE_DIR

mkdir "$work/src"
tar -C "$work/src" -xzf "$abduco_tgz"
src=$work/src/abduco-$ABDUCO_VERSION
[ -f "$src/LICENSE" ] || die "the abduco source has no LICENSE"

# Every target is built and checked before anything is written to outdir.
mismatch=0
for pair in linux_amd64:x86_64-linux-musl linux_arm64:aarch64-linux-musl \
  darwin_amd64:x86_64-macos.13.0 darwin_arm64:aarch64-macos.13.0; do
  name=${pair%%:*}
  target=${pair#*:}
  dir=$work/build-$name
  cp -R "$src" "$dir"
  # config.mk appends to CFLAGS (`+=`), so these join its own flags; LDFLAGS from the
  # command line replaces config.mk's, so its -lc -lutil are repeated here.
  case $name in
    linux_*)
      cflags="-O2"
      ldflags="-static -s -lc -lutil"
      ;;
    darwin_*)
      cflags="-O2 -D_DARWIN_C_SOURCE -I$here/abduco-darwin"
      ldflags="-s -lc"
      ;;
  esac
  echo "build abduco $name ($target)"
  CFLAGS=$cflags make -s -C "$dir" CC="$zig cc -target $target" LDFLAGS="$ldflags" abduco >"$work/$name.log" 2>&1 || {
    cat "$work/$name.log" >&2
    die "the $name build failed"
  }
  got=$(sum "$dir/abduco")
  case $name in
    linux_amd64) want=$OUT_SHA256_linux_amd64 ;;
    linux_arm64) want=$OUT_SHA256_linux_arm64 ;;
    darwin_amd64) want=$OUT_SHA256_darwin_amd64 ;;
    darwin_arm64) want=$OUT_SHA256_darwin_arm64 ;;
  esac
  if [ "$got" != "$want" ]; then
    echo "build-abduco: abduco for $name: sha256 $got, want $want (pinned in this script)" >&2
    mismatch=1
  fi
done
[ "$mismatch" -eq 0 ] || die "a built abduco does not match its pinned sum; nothing was written"

for name in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64; do
  mkdir "$outdir/$name"
  cp "$work/build-$name/abduco" "$outdir/$name/abduco"
  chmod 755 "$outdir/$name/abduco"
done
musl_notice=$work/zig/ziglang/lib/libc/musl/COPYRIGHT
[ -s "$musl_notice" ] || die "the zig in the wheel has no musl COPYRIGHT"
{
  cat "$src/LICENSE"
  printf '\n----------------------------------------------------------------------\n\n'
  printf 'The Linux builds of abduco are statically linked with musl libc %s\n' "(as shipped with zig $ZIG_VERSION)."
  printf 'Its copyright notice follows.\n\n'
  cat "$musl_notice"
} >"$outdir/LICENSE.abduco"
chmod 644 "$outdir/LICENSE.abduco"
echo "wrote abduco $ABDUCO_VERSION for 4 targets and LICENSE.abduco to $outdir"
