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
# The macOS verify script runs offline against stubs, so a change to it is caught here
# and not on the Mac. bash, because the script and its test are bash 3.2 code.
bash scripts/macos-verify_test.sh
# The prod deploy script (kept previous binary, rollback line), against a stub go and
# systemctl in a throwaway checkout.
bash scripts/deploy-prod_test.sh
# The installer (checksum, provenance, .prev + rollback, from-source) against stub curl, gh,
# git, go and uname and a fake release dir; shellcheck runs when it is installed.
bash scripts/setup_test.sh
# The upgrade test (#229): its resolver and build steps offline, then the real thing,
# which builds the previous release and upgrades a throwaway data root to this
# checkout (prints SKIP in a clone with no previous tag). The test package carries the
# upgrade build tag, so neither go vet ./... nor the darwin vet below reaches it:
# vet it here (a tagless clone SKIPs the run above and would never compile it).
bash scripts/upgrade-test_test.sh
bash scripts/upgrade-test.sh
go vet -tags upgrade ./internal/upgradetest/
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go vet -tags upgrade ./internal/upgradetest/
# The release abduco build's refusals (download, pin, outdir), against a stub curl,
# and the release packing (members, setup.sh, SHA256SUMS), against a stub go.
sh scripts/release/build-abduco_test.sh
sh scripts/release/build_test.sh
# The CI secret-scan script, against a stub gitleaks and a throwaway repo.
bash scripts/ci/secret-scan_test.sh
# The pinned Grype install's refusals (platform, sum before unpacking), against a stub curl.
sh scripts/ci/install-grype_test.sh
# The scan report (counts, accepted findings and their expiry, sanitising), against fixtures.
sh scripts/ci/image-scan-report_test.sh
# gitleaks for the rules test and the branch scan below, at CI's pinned version: the one
# on PATH when it reports that version, else the same version built from its
# checksum-verified module source (a box has no scanner in its image; the first build
# takes a few minutes, then it is cached). A failed build stops test.sh here.
gitleaks_dir=$(bash scripts/ci/gitleaks-install.sh)
PATH="$gitleaks_dir:$PATH"
bash scripts/ci/gitleaks-install_test.sh
bash scripts/ci/branch-scan_test.sh
# The private-identifier gitleaks rules, proven on made-up values.
bash scripts/ci/gitleaks-rules_test.sh
# The commits this checkout has and origin/main lacks, and its uncommitted files, with
# .gitleaks.toml, as the host's pre-push gate will scan them: a box finds a leak before its
# branch comes home (#381).
bash scripts/ci/branch-scan.sh
# macOS runs only on a laptop this box cannot reach, so the darwin build must
# fail here, not there.
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go vet ./...
go vet -tags uidemo ./cmd/culibox
go test -tags uidemo -run UIDemo ./cmd/culibox
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go vet -tags uidemo ./cmd/culibox
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o /dev/null ./cmd/culibox
node --test "internal/web/jstest/*.test.js"
# Last, because it is the slowest and the one that can skip itself.
./scripts/lint.sh
