#!/usr/bin/env bash
# Print the release version of the checkout: `git describe` against the newest
# vX.Y.Z tag. A tagged commit prints "v0.1.0"; later commits print
# "v0.1.0-3-g<sha>"; a dirty tree adds "-dirty"; before any tag, the short sha.
# Builds stamp it with: go build -ldflags "-X main.version=$(scripts/version.sh)"
set -euo pipefail
REPO="$(cd "$(dirname "$0")/.." && pwd)"
git -C "$REPO" describe --tags --always --dirty --match 'v[0-9]*'
