#!/usr/bin/env sh
# Run all three checks: Go (server), node --test (browser-side pure functions),
# and lint.sh (the page's cross-script references, which no test can reach).
# The glob is required: `node --test <dir>` is broken on Node >= 22.6, where a bare
# directory resolves to itself and is executed as a script — reporting a failure
# whether or not the tests pass.
set -e
cd "$(dirname "$0")/.."
go test ./...
node --test "internal/web/jstest/*.test.js"
# Last, because it is the slowest and the one that can skip itself.
./scripts/lint.sh
