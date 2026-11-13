#!/usr/bin/env bash
# Upgrade test (#229): build the previous release (vN-1) and this checkout (vN), then
# run internal/upgradetest, which fills a throwaway data root with vN-1, upgrades it to
# vN, rolls it back to vN-1, and fails on any user data lost or rewritten outside its
# allowlist. Run it as `bash scripts/upgrade-test.sh`; scripts/test.sh and CI do.
#
# vN-1 is the newest v* tag reachable from HEAD that is not HEAD itself. A clone with
# no such tag (a shallow CI checkout) prints SKIP and exits 0, or 1 when
# AB_UPGRADE_REQUIRE=1.
#
# Environment (all optional):
#   AB_PREV_REF         use this ref as vN-1 instead of the resolved tag
#   AB_PREV_BIN         a prebuilt vN-1 binary (skips that build; the Mac row uses it)
#   AB_CUR_BIN          a prebuilt vN binary
#   AB_UPGRADE_TESTBIN  a prebuilt test binary (go test -c -tags upgrade), so no Go is needed
#   AB_UPGRADE_BUILD    build dir (default: a mktemp dir, removed on exit); in a box use
#                       one under /home/agent/ext, not the 512 MB /tmp
#   AB_UPGRADE_KEEP=1   keep the build dir and the test's data roots
#   AB_UPGRADE_REQUIRE=1  a SKIP is a failure
# Bash 3.2 code: it also runs on macOS.
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
# Paths given relative to the caller's directory: the script cds to the repo,
# the previous build runs in its own source dir and go test in the package dir.
abs() { case $1 in /* | '') echo "$1" ;; *) echo "$PWD/$1" ;; esac; }
AB_PREV_BIN=$(abs "${AB_PREV_BIN:-}")
AB_CUR_BIN=$(abs "${AB_CUR_BIN:-}")
AB_UPGRADE_TESTBIN=$(abs "${AB_UPGRADE_TESTBIN:-}")
AB_UPGRADE_BUILD=$(abs "${AB_UPGRADE_BUILD:-}")
cd "$repo"

skip() {
  echo "SKIP upgrade-test: $*"
  if [ "${AB_UPGRADE_REQUIRE:-}" = 1 ]; then exit 1; fi
  exit 0
}

cur=$(scripts/version.sh)
head=$(git rev-parse HEAD)

if [ -n "${AB_PREV_REF:-}" ]; then
  prev=$AB_PREV_REF
  if ! git rev-parse -q --verify "$prev^{commit}" >/dev/null; then
    echo "upgrade-test: AB_PREV_REF=$prev is not a commit in this clone" >&2
    exit 1
  fi
else
  prev=$(git describe --tags --abbrev=0 --match 'v[0-9]*' HEAD 2>/dev/null || true)
  if [ -n "$prev" ] && [ "$(git rev-parse "$prev^{commit}")" = "$head" ]; then
    # HEAD is the release itself: test the upgrade from the one before it.
    prev=$(git describe --tags --abbrev=0 --match 'v[0-9]*' HEAD^ 2>/dev/null || true)
  fi
  if [ -z "$prev" ]; then
    skip "no previous v* tag reachable from HEAD (shallow or tagless clone; CI needs fetch-depth: 0)"
  fi
fi
behind=$(git rev-list --count "$prev..HEAD")
echo "upgrade-test: $prev ($behind commits behind) -> $cur"

# guard NAME BIN WANT: a supplied binary must report WANT when it can report a version.
guard() {
  local out rc=0
  out=$("$2" version 2>&1) || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "upgrade-test: $1 version unverifiable ($2 has no version command); using it as $3"
    return 0
  fi
  # `<name> <version> (<commit>)`: the second word must be exactly WANT.
  local got
  read -r _ got _ <<<"$out" || true
  if [ "$got" != "$3" ]; then
    echo "upgrade-test: $1 version mismatch: $2 reports '$out', want $3" >&2
    exit 1
  fi
}

# mainpkg REF: the main package directory in the tree at REF.
mainpkg() {
  if git cat-file -e "$1:cmd/culibox/main.go" 2>/dev/null; then echo ./cmd/culibox; else echo ./cmd/agent-box; fi
}

build_dir=${AB_UPGRADE_BUILD:-}
made_build=
if [ -z "$build_dir" ]; then
  build_dir=$(mktemp -d)
  made_build=1
fi
cleanup() {
  if [ -n "$made_build" ] && [ "${AB_UPGRADE_KEEP:-}" != 1 ]; then rm -rf "$build_dir"; fi
}
trap cleanup EXIT

prev_pkg=$(mainpkg "$prev")
cur_pkg=$(mainpkg HEAD)
# Stamped like a release build (scripts/release/build.sh): version and full commit.
prev_sha=$(git rev-parse "$prev^{commit}")
prev_flags="-X main.version=$prev -X main.commit=$prev_sha"
cur_flags="-X main.version=$cur -X main.commit=$head"
prev_build="CGO_ENABLED=0 go build -trimpath -ldflags \"$prev_flags\" -o $build_dir/prev $prev_pkg"
cur_build="CGO_ENABLED=0 go build -trimpath -ldflags \"$cur_flags\" -o $build_dir/cur $cur_pkg"

if [ -n "${AB_PREV_BIN:-}" ]; then
  guard prev "$AB_PREV_BIN" "$prev"
  echo "prev: $AB_PREV_BIN (supplied)"
else
  echo "prev: (in $build_dir/src-prev) $prev_build"
fi
if [ -n "${AB_CUR_BIN:-}" ]; then
  guard cur "$AB_CUR_BIN" "$cur"
  echo "cur: $AB_CUR_BIN (supplied)"
else
  echo "cur: $cur_build"
fi
if [ -n "${AB_UPGRADE_TESTBIN:-}" ]; then
  test_cmd=("$AB_UPGRADE_TESTBIN" -test.v -test.count=1 -test.timeout 15m)
else
  test_cmd=(go test -tags upgrade -count=1 -v -timeout 15m ./internal/upgradetest/)
fi
echo "run: ${test_cmd[*]}"
if [ "${AB_UPGRADE_DRYRUN:-}" = 1 ]; then exit 0; fi

prev_bin=${AB_PREV_BIN:-$build_dir/prev}
cur_bin=${AB_CUR_BIN:-$build_dir/cur}
if [ -z "${AB_PREV_BIN:-}" ]; then
  rm -rf "$build_dir/src-prev"
  mkdir -p "$build_dir/src-prev"
  git archive "$prev" | tar -x -C "$build_dir/src-prev"
  (cd "$build_dir/src-prev" && CGO_ENABLED=0 go build -trimpath -ldflags "$prev_flags" -o "$build_dir/prev" "$prev_pkg")
fi
if [ -z "${AB_CUR_BIN:-}" ]; then
  CGO_ENABLED=0 go build -trimpath -ldflags "$cur_flags" -o "$build_dir/cur" "$cur_pkg"
fi
# The commit the current binary stamps, for the version-stamp check; unknown
# for a supplied binary.
cur_commit=
if [ -z "${AB_CUR_BIN:-}" ]; then cur_commit=$head; fi

AB_UPGRADE_PREV_BIN=$prev_bin AB_UPGRADE_CUR_BIN=$cur_bin \
  AB_UPGRADE_PREV_VERSION=$prev AB_UPGRADE_CUR_VERSION=$cur AB_UPGRADE_CUR_COMMIT=$cur_commit \
  AB_UPGRADE_KEEP=${AB_UPGRADE_KEEP:-} \
  "${test_cmd[@]}"
