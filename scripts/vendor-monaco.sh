#!/usr/bin/env sh
# Regenerate internal/web/static/vendor/monaco/ from the npm tarball for monaco-editor@VERSION.
#
# The vendored tree is checked in so the repo stays buildable with no network and no bundler.
# Run this only to move to another Monaco version: change VERSION below, run it, read the diff,
# and commit the tree with the integrity string this prints.
#
# The tarball is checked against the registry's own dist.integrity (sha512) before a byte of it
# is unpacked, and the run stops on a mismatch. It is never installed: no `npm install`, so no
# package script runs; npm is only asked where the tarball is and what it must hash to. Only
# package/min/vs/** and package/LICENSE are copied out, into the repo's vendor/monaco, from a
# fresh temp dir that is removed on exit.
#
# MANIFEST.sha256 lists "<sha256>  <relpath>" for every vendored file, sorted bytewise, so
# jstest/monaco-vendor.test.js can tell an edited or stray file from the checked tarball.
set -e
cd "$(dirname "$0")/.."

VERSION=0.57.0
DEST=internal/web/static/vendor/monaco

die() { echo "vendor-monaco: $*" >&2; exit 1; }

for tool in npm curl tar openssl find sort xargs tr grep; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done
if command -v sha256sum >/dev/null 2>&1; then
  sha256() { tr '\n' '\0' | xargs -0 sha256sum; }
elif command -v shasum >/dev/null 2>&1; then
  sha256() { tr '\n' '\0' | xargs -0 shasum -a 256; }
else
  die "neither sha256sum nor shasum is available"
fi

tarball=$(npm view "monaco-editor@$VERSION" dist.tarball) || die "npm view dist.tarball failed"
integrity=$(npm view "monaco-editor@$VERSION" dist.integrity) || die "npm view dist.integrity failed"
case "$tarball" in https://*.tgz) ;; *) die "unexpected tarball URL: $tarball" ;; esac
case "$integrity" in sha512-*) ;; *) die "unexpected integrity (want sha512-...): $integrity" ;; esac

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM
mkdir "$work/dl" "$work/unpacked" "$work/stage"

curl -fsSL --proto '=https' "$tarball" -o "$work/dl/monaco.tgz" || die "download failed: $tarball"
got="sha512-$(openssl dgst -sha512 -binary "$work/dl/monaco.tgz" | openssl base64 -A)"
if [ "$got" != "$integrity" ]; then
  die "integrity mismatch for $tarball: npm says $integrity, the download is $got"
fi

# Members are named, so nothing else in the archive is written, and the archive's own
# leading-slash and .. handling applies to those two names only.
tar -xzf "$work/dl/monaco.tgz" -C "$work/unpacked" package/min/vs package/LICENSE \
  || die "extract failed"
[ -f "$work/unpacked/package/min/vs/loader.js" ] || die "package/min/vs/loader.js is missing from the tarball"
[ -f "$work/unpacked/package/LICENSE" ] || die "package/LICENSE is missing from the tarball"
# A link could point outside the tree this script copies, and the manifest hashes what it reaches.
if find "$work/unpacked/package" ! -type f ! -type d | grep -q .; then
  die "the tarball holds something other than files and directories under min/vs"
fi

cp -R "$work/unpacked/package/min/vs" "$work/stage/vs"
cp "$work/unpacked/package/LICENSE" "$work/stage/LICENSE"
# Modes come from the archive; git tracks only the exec bit, so make that uniform.
find "$work/stage" -type d -exec chmod 755 {} +
find "$work/stage" -type f -exec chmod 644 {} +

# The manifest format is "<sha256>  <relpath>" with the path as one token, so refuse a name that
# would break the line rather than write a manifest the test cannot read.
list=$(cd "$work/stage" && find vs LICENSE -type f | LC_ALL=C sort)
if printf '%s\n' "$list" | grep -q '[[:space:]]'; then
  die "a vendored file name holds whitespace"
fi
(cd "$work/stage" && printf '%s\n' "$list" | sha256 > MANIFEST.sha256)

rm -rf "$DEST"
mkdir -p "$DEST"
cp -R "$work/stage/." "$DEST/"

n=$(printf '%s\n' "$list" | grep -c .)
echo "vendor-monaco: monaco-editor@$VERSION"
echo "vendor-monaco: integrity $integrity"
echo "vendor-monaco: $n files in $DEST (plus MANIFEST.sha256)"
