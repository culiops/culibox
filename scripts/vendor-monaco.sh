#!/usr/bin/env sh
# Regenerate internal/web/static/vendor/monaco/ from the npm tarball for monaco-editor@VERSION.
#
# The vendored tree is checked in so the repo stays buildable with no network and no bundler.
# Run this only to move to another Monaco version: change VERSION and INTEGRITY below, run it,
# read the diff (the trim list and the three stubs below name hashed files that change with the
# version, and the run stops if one is gone), and commit the tree.
#
# The tarball is checked against INTEGRITY (sha512, pinned in this file) AND against the registry's
# own dist.integrity before a byte of it is unpacked, and the run stops on a mismatch. It is never
# installed: no `npm install`, so no package script runs; npm is only asked where the tarball is and
# what it says it hashes to. Only package/min/vs/** and package/LICENSE are copied out, into the
# repo's vendor/monaco, from a fresh temp dir that is removed on exit.
#
# What is kept is trimmed to what the Files dock uses (see "Trim" below), and that trim is part of
# this script, so a re-run reproduces the committed tree byte for byte.
#
# MANIFEST.sha256 lists "<sha256>  <relpath>" for every vendored file, sorted bytewise, so
# jstest/monaco-vendor.test.js can tell an edited or stray file from the checked tarball.
set -e
cd "$(dirname "$0")/.."

VERSION=0.57.0
# The sha512 npm publishes for monaco-editor@$VERSION: `npm view monaco-editor@<version> dist.integrity`.
INTEGRITY=sha512-5BkI9KGoqrNvBGUe15/QlZq3OooZ8WLg1AxTpaqHRCP3HNpzPPZKE2EDz8M7c+VRmCeUw1Brp4cx/PWm3kI/5A==
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
if [ "$integrity" != "$INTEGRITY" ]; then
  die "npm's dist.integrity for monaco-editor@$VERSION is $integrity, not the pinned $INTEGRITY"
fi

work=$(mktemp -d)
# An interrupt must stop the run, not just clean up and carry on to the next line with a half-written tree.
trap 'rm -rf "$work"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
mkdir "$work/dl" "$work/unpacked" "$work/stage"

curl -fsSL --proto '=https' "$tarball" -o "$work/dl/monaco.tgz" || die "download failed: $tarball"
got="sha512-$(openssl dgst -sha512 -binary "$work/dl/monaco.tgz" | openssl base64 -A)"
if [ "$got" != "$INTEGRITY" ]; then
  die "integrity mismatch for $tarball: pinned $INTEGRITY, the download is $got"
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

# --- Trim -----------------------------------------------------------------------------------------
# The dock needs the editor core, the Monarch tokenizers (vs/basic-languages and the per-language
# chunks it loads, which also give css, html, javascript, typescript, less and scss their highlighting)
# and JSON, whose tokens come from vs/language/json because Monaco has no Monarch JSON.
# It does not need the TypeScript, CSS and HTML language services (completion, diagnostics, formatting
# on a worker): they are about 16.7 MB, and the TypeScript one put squiggles on plain JS/TS files in
# Task 4's check. Their files go, and editor.main.js, which still names them, is kept loadable by stubs.
# Every name below is a file of 0.57.0; a missing one stops the run, so a version change is looked at.
trim() {
  for rel in "$@"; do
    [ -e "$work/stage/vs/$rel" ] || die "the trim list names vs/$rel, which is not in monaco-editor@$VERSION"
    rm -rf "$work/stage/vs/$rel"
  done
}
# The service directories (each holds the language's monaco.contribution.js shim and a legacy worker),
# the three module workers (assets/*.worker-*.js are what editor.main's getWorker would start), and the
# three lazy "mode" chunks that set a service up. JSON's equivalents stay.
trim language/typescript language/css language/html
trim assets/ts.worker-g3pjEjl4.js assets/css.worker-BpD9FCKP.js assets/html.worker-CLuInTTy.js
trim tsMode-DB1GiNun.js cssMode-eIUIN_ru.js htmlMode-aw5QYZo7.js
# vs/nls holds only the 13 locale bundles (and their .d.ts), which nls.messages-loader fetches only when a page
# sets require.config({"vs/nls": {availableLanguages}}); monacoload.js never does, so English is built in.
trim nls

# Stubs. editor.main.js and index-*.js list the three monaco.contribution-* chunks below as
# dependencies and read `.register` from each (to expose monaco.languages.css, .html and .typescript),
# so they cannot be deleted. Each chunk is what hooks a service up (an onLanguage listener that loads
# the mode chunk, which starts the worker). Each is replaced by a module with the same id that exports
# an empty `register` and registers nothing: no listener, no mode load, no worker, no 404.
# The marker is checked first, so a version change that moves the hashed names cannot stub the wrong chunk.
stub() {
  f="$work/stage/vs/$1"
  [ -f "$f" ] || die "the stub list names vs/$1, which is not in monaco-editor@$VERSION"
  grep -q -F -- "$2" "$f" || die "vs/$1 is not the $3 chunk (no $2 in it)"
  printf 'define("vs/%s",["exports"],(function(e){"use strict";e.register=Object.freeze(Object.create(null))}));\n' "${1%.js}" > "$f"
}
stub monaco.contribution-C7tY_xnl.js 'onLanguage("css"' CSS
stub monaco.contribution-BuX2NvO2.js 'htmlLanguageService' HTML
stub monaco.contribution-CY2lwFI8.js 'onLanguage("typescript"' TypeScript

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
