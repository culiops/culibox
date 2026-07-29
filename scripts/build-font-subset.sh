#!/usr/bin/env sh
# Regenerate internal/web/static/vendor/symbols-nerd-font-subset.ttf.
#
# The vendored .ttf is a build artifact of this script, checked in so the repo
# stays buildable with no network and no Python. Run this only to add a glyph —
# and never add a codepoint you have not rendered and looked at, because an
# absent codepoint subsets to nothing and ships a tofu box that no test catches.
#
# CODEPOINTS is the union of the file-type map in filetree.js and the chrome
# glyphs in index.html. Keep this comment and that map in sync.
set -e
cd "$(dirname "$0")/.."
SRC="${1:?usage: build-font-subset.sh /path/to/SymbolsNerdFontMono-Regular.ttf}"
OUT=internal/web/static/vendor/symbols-nerd-font-subset.ttf

CODEPOINTS="F07B,F07C,F15B,E627,E60C,E73E,E60B,E736,E749,F1C5,E795,E702,F013,F023,F021,F067,F0257,F013,F141,F00D"
# Chrome glyphs (Step 2, probed and eyeballed at /tmp/probe.html):
#   refresh=F021 plus=F067 new-folder=F0257 gear=F013 more=F141 close=F00D

# A missing codepoint subsets to nothing and ships a tofu box that no test can
# see, so refuse to build until the placeholder is gone and the count is right.
case "$CODEPOINTS" in *REPLACE-ME*) echo "refusing: chrome codepoints not filled in" >&2; exit 1;; esac
n=$(printf %s "$CODEPOINTS" | tr ',' '\n' | grep -c .)
[ "$n" -eq 20 ] || { echo "refusing: expected 20 codepoints, found $n" >&2; exit 1; }

python3 -m fontTools.subset "$SRC" \
  --unicodes="$CODEPOINTS" \
  --output-file="$OUT" \
  --no-hinting --desubroutinize
ls -l "$OUT"
