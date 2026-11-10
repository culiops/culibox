#!/usr/bin/env sh
# Run all three checks: Go (server), node --test (browser-side pure functions),
# and lint.sh (the page's cross-script references, which no test can reach).
# The glob is required: `node --test <dir>` is broken on Node >= 22.6, where a bare
# directory resolves to itself and is executed as a script — reporting a failure
# whether or not the tests pass.
set -e
cd "$(dirname "$0")/.."
# First, because it is instant and a formatting diff buried under a test run is
# a diff nobody reads. gofmt -l names files rather than failing, so the check has
# to be built from its output.
unformatted=$(gofmt -l . 2>/dev/null || true)
if [ -n "$unformatted" ]; then
  echo "gofmt: these files are not formatted — run 'gofmt -w' on them:" >&2
  echo "$unformatted" | sed 's/^/  /' >&2
  exit 1
fi
go test ./...
# macOS runs only on a laptop this box cannot reach, so the darwin build must
# fail here, not there.
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go vet ./...
go vet -tags uidemo ./cmd/agent-box
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go vet -tags uidemo ./cmd/agent-box
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o /dev/null ./cmd/agent-box
node --test "internal/web/jstest/*.test.js"
# Last, because it is the slowest and the one that can skip itself.
./scripts/lint.sh
