#!/bin/sh
# Fail when a binary was built with the uidemo build tag.
#
#   scripts/release/assert-no-uidemo.sh <binary>...
#
# The uidemo tag swaps in a demo of the VM folders page over a fake home. It is
# for UI checks only and must never ship. A Go binary records the tags it was
# built with, so this reads them back (`go version -m`) rather than trusting the
# build command. Any unreadable binary fails too.
set -eu

if [ "$#" -lt 1 ]; then
  echo "usage: $0 <binary>..." >&2
  exit 2
fi

for bin in "$@"; do
  if [ ! -f "$bin" ]; then
    echo "assert-no-uidemo.sh: $bin is not a file" >&2
    exit 1
  fi
  # Assigned on its own so a failing go command stops the script.
  info=$(go version -m "$bin") || {
    echo "assert-no-uidemo.sh: could not read the build info of $bin" >&2
    exit 1
  }
  if printf '%s\n' "$info" | grep -Eq '^[[:space:]]+build[[:space:]]+-tags=.*uidemo'; then
    echo "assert-no-uidemo.sh: $bin was built with the uidemo tag" >&2
    exit 1
  fi
done
