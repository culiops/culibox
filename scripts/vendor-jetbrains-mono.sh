#!/usr/bin/env sh
# Regenerate internal/web/static/vendor/jetbrains-mono/ (the Files dock's code font) from the npm tarball of
# @fontsource-variable/jetbrains-mono@VERSION: fontsource's repackage of JetBrains Mono's own release
# (github.com/JetBrains/JetBrainsMono, SIL OFL 1.1), split into unicode-range subsets.
#
# The vendored files are checked in so the repo stays buildable with no network. Run this only to move to another
# version: change VERSION and INTEGRITY below, run it, read the diff, and commit the tree. The tarball is checked against
# INTEGRITY (sha512, pinned here) AND against the registry's own dist.integrity before a byte of it is unpacked, and the
# run stops on a mismatch. It is never installed: no `npm install`, so no package script runs.
#
# Kept: the upright variable font (wght 100-800) in three subsets: latin, latin-ext and vietnamese (CuliBox's first
# users write Vietnamese comments), plus LICENSE. desktop.css's @font-face rules carry the package's unicode-ranges.
# MANIFEST.sha256 lists "<sha256>  <name>" for each, sorted bytewise; SOURCE.txt says where they came from.
# jstest/jetbrains-vendor.test.js holds the tree to both.
set -e
cd "$(dirname "$0")/.."

PKG=@fontsource-variable/jetbrains-mono
VERSION=5.3.0
# `npm view @fontsource-variable/jetbrains-mono@<version> dist.integrity`
INTEGRITY=sha512-F32xpS2NsGYoQi2ADSkKTgpJj7ozajsGgDJ8woTnqjmIB+dxDIqImjl4pXZVEExu8UFZ2ndhmX18EBS/hdz3Lw==
DEST=internal/web/static/vendor/jetbrains-mono
SUBSETS="latin latin-ext vietnamese"

die() { echo "vendor-jetbrains-mono: $*" >&2; exit 1; }

for tool in npm curl tar openssl sort; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done
if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum "$@"; }
elif command -v shasum >/dev/null 2>&1; then
  sha256() { shasum -a 256 "$@"; }
else
  die "neither sha256sum nor shasum is available"
fi

tarball=$(npm view "$PKG@$VERSION" dist.tarball) || die "npm view dist.tarball failed"
integrity=$(npm view "$PKG@$VERSION" dist.integrity) || die "npm view dist.integrity failed"
case "$tarball" in https://registry.npmjs.org/*.tgz) ;; *) die "unexpected tarball URL: $tarball" ;; esac
[ "$integrity" = "$INTEGRITY" ] || die "npm's dist.integrity for $PKG@$VERSION is $integrity, not the pinned $INTEGRITY"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
mkdir "$work/unpacked" "$work/stage"

curl -fsSL --proto '=https' "$tarball" -o "$work/font.tgz" || die "download failed: $tarball"
got="sha512-$(openssl dgst -sha512 -binary "$work/font.tgz" | openssl base64 -A)"
[ "$got" = "$INTEGRITY" ] || die "integrity mismatch for $tarball: pinned $INTEGRITY, the download is $got"

# Members are named, so nothing else in the archive is written.
members="package/LICENSE"
for s in $SUBSETS; do members="$members package/files/jetbrains-mono-$s-wght-normal.woff2"; done
# shellcheck disable=SC2086 # the member list is word-split on purpose; no name holds a space
tar -xzf "$work/font.tgz" -C "$work/unpacked" $members || die "extract failed"
for m in $members; do
  [ -f "$work/unpacked/$m" ] && [ ! -L "$work/unpacked/$m" ] || die "$m is missing from the tarball or not a plain file"
  cp "$work/unpacked/$m" "$work/stage/"
done
chmod 644 "$work/stage/"*

(cd "$work/stage" && for f in LICENSE *.woff2; do printf '%s\n' "$f"; done | LC_ALL=C sort | while read -r f; do sha256 "$f"; done) > "$work/MANIFEST.sha256"
cat > "$work/SOURCE.txt" <<SRC
$PKG@$VERSION (npm, OFL-1.1)
integrity $INTEGRITY
tarball $tarball
fontsource's repackage of JetBrains Mono's release, github.com/JetBrains/JetBrainsMono
subsets: $SUBSETS (wght 100-800, normal)
written by scripts/vendor-jetbrains-mono.sh
SRC

rm -rf "$DEST"
mkdir -p "$DEST"
cp "$work/stage/"* "$work/MANIFEST.sha256" "$work/SOURCE.txt" "$DEST/"
echo "vendor-jetbrains-mono: $PKG@$VERSION, integrity $integrity, into $DEST"
