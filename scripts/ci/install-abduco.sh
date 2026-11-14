#!/usr/bin/env sh
# Build and install the pinned abduco: scripts/ci/install-abduco.sh <prefix>
# leaves <prefix>/bin/abduco. CI puts that bin on PATH so the abduco-backed Go
# tests run against the same abduco the box image ships (they must not skip).
#
# The version and sum are the Dockerfile's ARG ABDUCO_VERSION / ABDUCO_SHA256
# (docker/agent-box/workflow_test.go fails if they drift apart). The tarball is
# checked against the sum BEFORE it is unpacked or built. Runs on Linux and on
# macOS: sha256sum where present, else `shasum -a 256`.
set -eu

ABDUCO_VERSION=0.6
ABDUCO_SHA256=c90909e13fa95770b5afc3b59f311b3d3d2fdfae23f9569fa4f96a3e192a35f4

if [ "$#" -ne 1 ] || [ -z "$1" ]; then
  echo "usage: $0 <prefix>" >&2
  exit 2
fi
prefix=$1

if command -v sha256sum >/dev/null 2>&1; then
  sum() { sha256sum "$1" | cut -d ' ' -f 1; }
elif command -v shasum >/dev/null 2>&1; then
  sum() { shasum -a 256 "$1" | cut -d ' ' -f 1; }
else
  echo "install-abduco: neither sha256sum nor shasum is available" >&2
  exit 1
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
tarball="$work/abduco.tar.gz"

curl -fsSL "https://github.com/martanne/abduco/releases/download/v${ABDUCO_VERSION}/abduco-${ABDUCO_VERSION}.tar.gz" -o "$tarball"
got=$(sum "$tarball")
if [ "$got" != "$ABDUCO_SHA256" ]; then
  echo "install-abduco: sha256 mismatch: got $got, want $ABDUCO_SHA256" >&2
  exit 1
fi

tar -C "$work" -xzf "$tarball"
# abduco's config.mk defines _POSIX_C_SOURCE and _XOPEN_SOURCE, which on Darwin hide
# SIGWINCH and VLNEXT (measured: macos-15 build errors in client.c, server.c and
# abduco.c). _DARWIN_C_SOURCE restores them. config.mk appends to CFLAGS (`+=`), so an
# env CFLAGS keeps its own flags. Darwin only: the Linux build is unchanged.
if [ "$(uname -s)" = Darwin ]; then
  CFLAGS="-D_DARWIN_C_SOURCE" make -C "$work/abduco-${ABDUCO_VERSION}"
else
  make -C "$work/abduco-${ABDUCO_VERSION}"
fi
mkdir -p "$prefix/bin"
install -m 0755 "$work/abduco-${ABDUCO_VERSION}/abduco" "$prefix/bin/abduco"

# `abduco -v` prints "abduco-0.6 ..." and may exit non-zero, so only the text counts.
"$prefix/bin/abduco" -v 2>&1 | grep -q "abduco-${ABDUCO_VERSION}"
echo "install-abduco: installed $prefix/bin/abduco (abduco-${ABDUCO_VERSION})"
