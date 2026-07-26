#!/usr/bin/env sh
# Run both suites: Go (server) and node --test (browser-side pure functions).
# The glob is required: `node --test <dir>` is broken on Node >= 22.6, where a bare
# directory resolves to itself and is executed as a script — reporting a failure
# whether or not the tests pass.
set -e
cd "$(dirname "$0")/.."
go test ./...
node --test "internal/web/jstest/*.test.js"
