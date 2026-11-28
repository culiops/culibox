#!/usr/bin/env bash
# Prints a directory holding `gitleaks`, the version CI's secrets job and the host's
# pre-push gate run (gitleaks-install_test.sh checks it against ci.yml). Used by
# scripts/test.sh when gitleaks is not on PATH, which is the case in a box: the box
# image has no scanner, and its default egress reaches the Go module proxy and
# checksum database, so the pinned module is built from source that go verifies
# against sum.golang.org. The first build takes a few minutes; later calls reuse it.
#
# The binary is built into a temp dir and moved into a versioned cache dir, so an
# interrupted or failed build never leaves something that looks installed.
set -euo pipefail

version=8.30.1
module=github.com/zricethezav/gitleaks/v8

cache=${XDG_CACHE_HOME:-$HOME/.cache}/culibox
dir=$cache/gitleaks-$version
if [ -x "$dir/gitleaks" ]; then
  echo "$dir"
  exit 0
fi
if ! command -v go >/dev/null 2>&1; then
  echo "gitleaks-install: could not build gitleaks $version: go is not on PATH" >&2
  exit 1
fi
mkdir -p "$dir"
tmpd=$(mktemp -d "$cache/.tmp.XXXXXX")
trap 'rm -rf "$tmpd"' EXIT
echo "gitleaks-install: building gitleaks $version from $module (first time only, a few minutes)" >&2
if ! GOBIN=$tmpd go install "$module@v$version" >&2; then
  echo "gitleaks-install: could not build gitleaks $version (go install $module@v$version failed)" >&2
  exit 1
fi
mv -f "$tmpd/gitleaks" "$dir/gitleaks"
echo "$dir"
