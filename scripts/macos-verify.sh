#!/usr/bin/env bash
# Verify a PUBLISHED (or draft) release's darwin tarballs on an Apple Silicon Mac and
# record the result on the release:
#
#   scripts/macos-verify.sh <tag>        e.g. v0.1.0
#
# Needs: macOS on Apple Silicon, `gh` logged in with release write access, git, Go,
# make and a C compiler (abduco is built from source), and Rosetta 2
# (`softwareupdate --install-rosetta --agree-to-license`).
#
# What it does, in order:
#   1. downloads the two darwin tarballs and SHA256SUMS and checks the sums; any
#      mismatch stops the script before a single test or binary runs
#      Before ANY release code or binary runs it also checks, for itself, that the
#      tagged commit is reachable from main (the release workflow's own check lives
#      in the tagged commit and can be edited away), and, once the repository is
#      public, that each tarball has a build attestation from release.yml for this
#      tag and commit
#   2. clones <tag> and runs the Go tests natively (arm64) and under Rosetta 2
#      (amd64, one package at a time, and a package that times out is run once more
#      alone; the record names it), built the way the tarballs are: CGO_ENABLED=0 GOOS=darwin
#      GOARCH=<arch>, ignoring the caller's GOFLAGS and go env file. Each arch's tests
#      use the abduco its tarball bundles (#317); for a tarball without one (releases
#      before #317) the pinned abduco is built from source instead
#   3. runs each extracted binary's `version` and requires exactly
#      `culibox <tag> (<commit of the clone>)`, and each bundled abduco's `-v`, which
#      must print abduco-<version>; amd64 runs under `arch -x86_64`
#   4. asks for the manual Mac-test-guide rows (never defaults one to pass)
#   5. builds macos-verification.json with tools/release-status (it holds the sha256
#      of each binary that was run, which the manual rows are told to use) and
#      uploads it to the release with --clobber (earlier runs stay in its runs[])
#   6. rewrites the darwin lines of the release notes' platform status block. An
#      earlier tick in the notes is cleared BEFORE the upload, and the final edit is
#      tried three times, so a failed edit leaves "NOT yet verified", never an old tick
#
# Exit status: 0 verified; 3 the record was written but the verdict is not
# "verified" (partial or failed: the release now says so); 1 any other failure,
# with nothing uploaded unless the message says otherwise; 2 usage.
# A failing test run does NOT stop the script: it is recorded honestly as failed.
# Only integrity failures abort before an upload: bad sums, a tag that is not on main,
# a missing or invalid attestation (public repo), or a tag/commit that does not match
# between the clone, the stamp and an earlier record.
# On any non-zero exit the work dir keeps its logs; the path is printed.
#
# Environment (every external effect can be redirected, which is how
# scripts/macos-verify_test.sh runs this offline):
#   RELEASE_STATUS               release-status binary (default: built from this checkout)
#   MACOS_VERIFY_GIT_URL         what to clone (default https://github.com/culiops/culibox.git)
#   MACOS_VERIFY_INSTALL_ABDUCO  abduco installer, called with <prefix>
#                                (default: ci/install-abduco.sh next to this script)
#   MACOS_VERIFY_GH_REPO         the release's repo for gh (default culiops/culibox)
#   MACOS_VERIFY_TEST=1          skips ONLY the "must be Darwin arm64" refusal
#   TMPDIR                       where the work dir goes (used by its physical path)
#
# bash 3.2 (the macOS default) compatible: no associative arrays, no ${x,,}, no mapfile.
set -euo pipefail

die() {
  echo "macos-verify: $*" >&2
  exit 1
}

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <tag>   (vX.Y.Z or vX.Y.Z-<prerelease>)" >&2
  exit 2
fi
tag=$1
# [[ =~ ]] anchors on the whole string: a grep on a pipe checks each LINE, so a tag
# with a second line would pass.
tag_re='^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'
if ! [[ $tag =~ $tag_re ]]; then
  echo "macos-verify: tag '$tag' is not vX.Y.Z or vX.Y.Z-<prerelease>" >&2
  exit 2
fi

# The manual rows are read from the caller's stdin, kept on fd 3. Everything else
# (go test, make, git) gets /dev/null, so none of them can swallow a typed row.
exec 3<&0
exec </dev/null

script_dir=$(cd "$(dirname "$0")" && pwd)
repo_root=$(cd "$script_dir/.." && pwd)
git_url=${MACOS_VERIFY_GIT_URL:-https://github.com/culiops/culibox.git}
installer=${MACOS_VERIFY_INSTALL_ABDUCO:-$script_dir/ci/install-abduco.sh}
export GH_REPO=${MACOS_VERIFY_GH_REPO:-culiops/culibox}

if [ "${MACOS_VERIFY_TEST:-}" != 1 ]; then
  if [ "$(uname -s)" != Darwin ] || [ "$(uname -m)" != arm64 ]; then
    die "this runs only on Apple Silicon macOS (Darwin arm64), not $(uname -s) $(uname -m); a Rosetta shell reports x86_64 and is refused too"
  fi
fi

for tool in gh git go tar; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is not on PATH"
done

# Cleanup: a good run leaves nothing; a failed run keeps logs and downloads (not the
# clone or the extracted binaries) and says where.
work=
cleanup() {
  local rc=$?
  trap - EXIT
  if [ -n "$work" ] && [ -d "$work" ]; then
    if [ "$rc" -eq 0 ]; then
      rm -rf "$work"
    else
      rm -rf "$work/src" "$work/abduco" "$work/arm64" "$work/amd64"
      echo "macos-verify: exit $rc; logs and downloads kept in $work" >&2
    fi
  fi
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT TERM
work=$(mktemp -d "${TMPDIR:-/tmp}/macos-verify.XXXXXX")
# By its physical path: macOS /tmp and /var are symlinks into /private, and a tree
# reached through one gives a relative path with `..` a different meaning for the
# kernel (physical) than for Go's filepath (logical, from $PWD), which fails tests of
# a good build (#313).
work_phys=$(cd -P -- "$work" >/dev/null && pwd -P) || die "cannot resolve the work dir $work"
work=$work_phys

# gh must be logged in before anything slow runs; the login is the verifier.
verifier=$(gh api user -q .login) || die "gh is not logged in (gh api user failed)"
[ -n "$verifier" ] || die "gh api user returned no login"

if [ -n "${RELEASE_STATUS:-}" ]; then
  rs=$RELEASE_STATUS
  [ -x "$rs" ] || die "RELEASE_STATUS=$rs is not an executable file"
else
  rs=$work/release-status
  echo "building release-status from $repo_root"
  (cd "$repo_root" && env -u GOOS -u GOARCH go build -o "$rs" ./tools/release-status) ||
    die "cannot build tools/release-status from $repo_root"
fi

if command -v sha256sum >/dev/null 2>&1; then
  sum() { sha256sum "$1" | cut -d ' ' -f 1; }
else
  sum() { shasum -a 256 "$1" | cut -d ' ' -f 1; }
fi

# json_str VALUE: VALUE as a JSON string literal (backslash, quote, control chars).
json_str() {
  local s
  s=$(printf '%s\n' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' -e 's/'"$(printf '\t')"'/\\t/g' | LC_ALL=C tr -d '\000-\037\177')
  printf '"%s"' "$s"
}

# ---- 1. download and check the sums -------------------------------------------------
dl=$work/dl
mkdir -p "$dl"
echo "downloading the darwin tarballs and SHA256SUMS for $tag"
gh release download "$tag" -p 'culibox_*_darwin_*.tar.gz' -p SHA256SUMS -p macos-verification.json --dir "$dl" ||
  die "gh release download $tag failed"

arm_name=culibox_${tag#v}_darwin_arm64.tar.gz
amd_name=culibox_${tag#v}_darwin_amd64.tar.gz
[ -f "$dl/$arm_name" ] || die "release $tag has no $arm_name"
[ -f "$dl/$amd_name" ] || die "release $tag has no $amd_name"
[ -f "$dl/SHA256SUMS" ] || die "release $tag has no SHA256SUMS"
extra=$(find "$dl" -maxdepth 1 -name 'culibox_*' ! -name "$arm_name" ! -name "$amd_name" -print)
[ -z "$extra" ] || die "unexpected darwin files in the release: $extra"

"$rs" check-sums --sums "$dl/SHA256SUMS" "$dl/$arm_name" "$dl/$amd_name" ||
  die "the downloaded tarballs do not match SHA256SUMS; nothing was run"
# From here on the tarballs are verified. The record's sha256 values come from these
# files, never from their names.
arm_sha=$(sum "$dl/$arm_name")
amd_sha=$(sum "$dl/$amd_name")

# ---- 2. the tag's source, abduco, the tests ----------------------------------------
echo "cloning $tag"
# Not shallow: the next check needs main's history. Cloning runs no code from the repo.
git -c advice.detachedHead=false clone --quiet --branch "$tag" "$git_url" "$work/src" ||
  die "cannot clone $tag from $git_url"
commit=$(git -C "$work/src" rev-parse HEAD)
tag_commit=$(git -C "$work/src" rev-parse "refs/tags/$tag^{commit}" 2>/dev/null) ||
  die "$tag is not a tag in $git_url"
[ "$commit" = "$tag_commit" ] || die "the clone's HEAD $commit is not tag $tag ($tag_commit)"

# Trust nothing the tag's own commit says. A v* tag runs the release.yml OF THE TAGGED
# COMMIT, so its "tag is on main" check can be edited away by whoever can push a tag.
# This script is about to run that tag's code and binaries on a Mac that has this
# login, so it checks for itself, before anything from the release runs: the tagged
# commit must be reachable from main.
git -C "$work/src" rev-parse --verify --quiet refs/remotes/origin/main >/dev/null ||
  die "origin/main is not in the clone of $git_url, so $tag cannot be checked; nothing was run"
git -C "$work/src" merge-base --is-ancestor "$commit" refs/remotes/origin/main ||
  die "tag $tag ($commit) is not on main: only commits on main are verified; nothing was run"

# Once the repository is public every release carries build provenance for each
# tarball. Require that it was made by release.yml of this repository, for this tag
# and this commit, on a GitHub-hosted runner. The signer is pinned by its exact
# identity (the workflow URL with this tag's ref), not with --signer-workflow, which
# older gh versions match as a prefix. While the repository is private no attestation
# exists (the workflow skips them), so only an exact PRIVATE skips the check, loudly;
# any other answer (INTERNAL, empty, anything new) stops the script.
visibility=$(gh repo view "$GH_REPO" --json visibility -q .visibility) ||
  die "cannot read the visibility of $GH_REPO; nothing was run"
case "$visibility" in
  PUBLIC)
    for f in "$arm_name" "$amd_name"; do
      gh attestation verify "$dl/$f" --repo "$GH_REPO" \
        --cert-identity "https://github.com/$GH_REPO/.github/workflows/release.yml@refs/tags/$tag" \
        --deny-self-hosted-runners \
        --source-ref "refs/tags/$tag" --source-digest "$commit" >"$work/attest-$f.log" 2>&1 || {
        cat "$work/attest-$f.log" >&2
        die "no valid build attestation for $f (from release.yml, $tag, $commit); nothing was run"
      }
    done
    ;;
  PRIVATE)
    echo "macos-verify: attestation check skipped: $GH_REPO is PRIVATE, so the release carries no attestations" >&2
    ;;
  *)
    die "the visibility of $GH_REPO is '$visibility', not PUBLIC or PRIVATE, so the attestation check cannot be decided; nothing was run"
    ;;
esac

# An earlier record must be for these exact bytes. `release-status record` enforces
# it too; checking now saves the test runs when the tag has moved.
prior=$dl/macos-verification.json
prior_arg=
if [ -f "$prior" ]; then
  prior_arg=$prior
  prior_commit=$(sed -n 's/^  "commit": "\([^"]*\)".*/\1/p' "$prior" | head -1)
  if [ -n "$prior_commit" ] && [ "$prior_commit" != "$commit" ]; then
    die "the release's macos-verification.json is for commit $prior_commit but $tag is at $commit now (a moved tag?); not merging runs for different bytes"
  fi
fi

# Rosetta must be installed, or amd64 binaries cannot run at all.
arch -x86_64 true >/dev/null 2>&1 ||
  die "Rosetta 2 is not usable (arch -x86_64 true failed); run: softwareupdate --install-rosetta --agree-to-license"

# <arch>_abduco: the abduco a tarball bundles (#317), empty when it has none.
arm64_abduco=
amd64_abduco=
for pair in "arm64:$arm_name" "amd64:$amd_name"; do
  a=${pair%%:*}
  t=${pair#*:}
  mkdir -p "$work/$a"
  # Only the binaries are extracted: a tarball is untrusted data until it is run.
  tar -xzf "$dl/$t" -C "$work/$a" culibox ||
    die "cannot extract culibox from $t"
  names=$(tar -tzf "$dl/$t") || die "cannot list $t"
  if printf '%s\n' "$names" | grep -qx abduco; then
    kind=$(tar -tvzf "$dl/$t" abduco | cut -c1) || die "cannot list abduco in $t"
    [ "$kind" = - ] || die "abduco in $t is not a regular file; nothing was run"
    tar -xzf "$dl/$t" -C "$work/$a" abduco || die "cannot extract abduco from $t"
    case $a in
      arm64) arm64_abduco=$work/arm64/abduco ;;
      amd64) amd64_abduco=$work/amd64/abduco ;;
    esac
  fi
done

# ---- 3. abduco and the tests -----------------------------------------------------
# Only a tarball without a bundled abduco needs one built from the pinned source. A
# failed build is a finding about this Mac or the pinned source, not a reason to leave
# no record: the tests cannot run without abduco (they would skip the abduco-backed
# ones), so that arch is recorded as failed and the script goes on.
src_abduco_dir=
if [ -z "$arm64_abduco" ] || [ -z "$amd64_abduco" ]; then
  echo "installing the pinned abduco (a tarball bundles none)"
  if "$installer" "$work/abduco" >"$work/abduco-install.log" 2>&1; then
    src_abduco_dir=$work/abduco/bin
  else
    cat "$work/abduco-install.log" >&2
    echo "macos-verify: the abduco install failed; the test runs without a bundled abduco are recorded as failed" >&2
  fi
fi

# run_tests ARCH GOARCH ABDUCO_DIR: go test ./... in the clone, with ABDUCO_DIR (the
# bundled abduco's folder, or the source build's; empty when that build failed) first
# on PATH, logged to test-<arch>.log. The
# environment is pinned, not inherited: the shipped tarballs are built with
# CGO_ENABLED=0 (scripts/release/build.sh), so the tests are built that way too, for
# darwin and this arch. GOENV=off with an explicit GOFLAGS: an EMPTY GOFLAGS falls back to
# the go env file (`go env -w`), so only a non-empty value pins it. -v so tests can be counted.
# A failing run sets T_PASS=false and does not stop the script.
#
# Under Rosetta (amd64), test binaries launched in parallel can hang before Go starts,
# at 0% CPU, until go test kills them; the same package passes alone in seconds
# (measured on a rented M1, #313). So amd64 tests one package at a time (-p 1), and a
# package that go test reports as timed out is run once more, alone, logged to
# test-<arch>-retry.log. The arch passes only when every such retry passes and nothing
# else failed; the summary names each retried package and its result either way.
T_PASS=
T_SUMMARY=
T_ENV=
go_test() { # LOG GO-TEST-ARGS...: go test in the clone, pinned as above, appended to LOG
  local log=$1
  shift
  (cd "$work/src" && env PATH="$abdir:$PATH" GOENV=off GOFLAGS=-mod=readonly CGO_ENABLED=0 GOOS=darwin GOARCH="$goarch" AGENT_BOX_REQUIRE_ABDUCO=1 \
    go test "$@") >>"$log" 2>&1
}
# timed_out_pkgs LOG: the packages whose FAIL line follows go test's timeout panic or
# its "ran too long" kill (a binary that never started gets only the kill).
timed_out_pkgs() {
  local line t=false
  while IFS= read -r line || [ -n "$line" ]; do
    case $line in
    "panic: test timed out after "* | "*** Test killed"*"ran too long"*) t=true ;;
    ok[[:space:]]*) t=false ;;
    FAIL$'\t'*$'\t'*)
      if [ "$t" = true ]; then
        line=${line#FAIL$'\t'}
        echo "${line%%$'\t'*}"
      fi
      t=false
      ;;
    esac
  done <"$1"
}
run_tests() {
  local arch=$1 goarch=$2 abdir=$3 log="$work/test-$1.log" rlog="$work/test-$1-retry.log"
  local passed skipped failed pkgs badpkgs timedout pkg ntimed=0 retried="" retry_ok=true note=""
  local par=""
  T_ENV="CGO_ENABLED=0 GOOS=darwin GOARCH=$goarch"
  if [ -z "$abdir" ]; then
    T_PASS=false
    T_SUMMARY="go test not run: the abduco install failed (see abduco-install.log)"
    echo "go test ./... ($arch): not run, the abduco install failed" >&2
    return 0
  fi
  if [ "$arch" = amd64 ]; then
    par="-p 1"
    note="; one package at a time under Rosetta (-p 1)"
  fi
  echo "go test ./... ($arch, $T_ENV${par:+, $par})"
  : >"$log"
  # $par unquoted on purpose: empty, or the two words -p 1.
  if go_test "$log" $par -count=1 -v ./...; then
    T_PASS=true
  else
    T_PASS=false
    timedout=
    if [ "$arch" = amd64 ]; then timedout=$(timed_out_pkgs "$log"); fi
    if [ -n "$timedout" ]; then
      : >"$rlog"
      for pkg in $timedout; do
        ntimed=$((ntimed + 1))
        echo "go test $pkg ($arch): timed out in the full run, running it again alone"
        if go_test "$rlog" -p 1 -count=1 -v "$pkg"; then
          retried="$retried; $pkg timed out and passed when run again alone"
        else
          retried="$retried; $pkg timed out and failed when run again alone"
          retry_ok=false
        fi
      done
      retried="$retried (see test-$arch-retry.log)"
    fi
  fi
  failed=$(grep -c '^--- FAIL' "$log" || true)
  badpkgs=$(grep -cE '^FAIL[[:space:]]+[^[:space:]]+' "$log" || true)
  if [ "$T_PASS" = false ] && [ "$ntimed" -gt 0 ] && [ "$retry_ok" = true ] && [ "$failed" -eq 0 ] && [ "$badpkgs" -eq "$ntimed" ]; then
    T_PASS=true
  fi
  if [ "$T_PASS" = true ]; then
    passed=$(cat "$log" ${retried:+"$rlog"} | grep -c '^--- PASS' || true)
    skipped=$(cat "$log" ${retried:+"$rlog"} | grep -c '^--- SKIP' || true)
    pkgs=$(cat "$log" ${retried:+"$rlog"} | grep -c '^ok' || true)
    T_SUMMARY="go test ./... ok: $passed test(s) passed, $skipped skipped, in $pkgs package(s)$retried$note"
  else
    T_SUMMARY="go test failed: $failed test(s) failed, $badpkgs package(s) FAIL, see test-$arch.log$retried$note"
  fi
  grep -E '^(ok|FAIL|--- FAIL|panic)' "$log" ${retried:+"$rlog"} || true
}

# check_version ARCH BINARY [RUNNER...]: the binary's `version` must equal exactly
# `culibox <tag> (<commit>)`. V_LINE keeps what it printed either way.
V_LINE=
V_OK=
V_BIN_SHA=
check_version() {
  local arch=$1 bin=$2
  shift 2
  local want="culibox $tag ($commit)"
  # The record ties the arch to these exact bytes, which are also the ones the manual
  # rows are told to use.
  V_BIN_SHA=$(sum "$bin")
  if V_LINE=$("$@" "$bin" version 2>&1); then :; else V_LINE="$V_LINE (exit $?)"; fi
  if [ "$V_LINE" = "$want" ]; then
    V_OK=true
  else
    V_OK=false
    echo "macos-verify: $arch version stamp mismatch: got '$V_LINE', want '$want'" >&2
  fi
}

# check_abduco ARCH BINARY [RUNNER...]: a bundled abduco's `-v` must print
# abduco-<version>, the version the tag's own scripts/release/build-abduco.sh pins (it
# may exit non-zero, so only the text counts). An empty BINARY is a tarball without
# one. A_NOTE says which abduco the tests used.
A_OK=
A_NOTE=
# A tag from before #317 has no such file: abduco_want stays empty, which fails only
# an arch whose tarball bundles an abduco anyway.
abduco_want=
if [ -f "$work/src/scripts/release/build-abduco.sh" ]; then
  abduco_want=$(sed -n 's/^ABDUCO_VERSION=//p' "$work/src/scripts/release/build-abduco.sh" | head -1)
fi
check_abduco() {
  local arch=$1 bin=$2 line
  shift 2
  A_OK=true
  if [ -z "$bin" ]; then
    if [ -n "$src_abduco_dir" ]; then
      A_NOTE="no bundled abduco: tests ran against the pinned source build"
    else
      A_NOTE="no bundled abduco, and the source build failed"
    fi
    return 0
  fi
  line=$("$@" "$bin" -v 2>&1 | head -1 || true)
  if [ -n "$abduco_want" ] && [ "${line%% *}" = "abduco-$abduco_want" ]; then
    A_NOTE="tests ran against the bundled abduco (abduco-$abduco_want)"
  else
    A_OK=false
    if [ -n "$abduco_want" ]; then
      A_NOTE="bundled abduco -v printed '$line', want abduco-$abduco_want"
    else
      A_NOTE="bundled abduco -v printed '$line', but the tag pins no ABDUCO_VERSION to check it against"
    fi
    echo "macos-verify: $arch $A_NOTE" >&2
  fi
}

# arch_json ARCH MODE: this arch's result, from T_PASS/T_SUMMARY, V_LINE/V_OK and
# A_OK/A_NOTE. A wrong version stamp or a bundled abduco that does not run fails the
# arch even when its tests pass.
arch_json() {
  local passed=$T_PASS summary="$T_SUMMARY; $A_NOTE"
  if [ "$V_OK" != true ]; then
    passed=false
    summary="$summary; version stamp mismatch (want culibox $tag ($commit))"
  fi
  if [ "$A_OK" != true ]; then
    passed=false
  fi
  printf '{"arch": %s, "mode": %s, "tests_passed": %s, "test_summary": %s, "version_line": %s, "binary_sha256": %s, "test_env": %s}' \
    "$(json_str "$1")" "$(json_str "$2")" "$passed" "$(json_str "$summary")" "$(json_str "$V_LINE")" "$(json_str "$V_BIN_SHA")" "$(json_str "$T_ENV")"
  echo "$1 ($2): $([ "$passed" = true ] && echo PASS || echo FAIL): $summary" >&2
}

run_tests arm64 arm64 "$([ -n "$arm64_abduco" ] && echo "$work/arm64" || echo "$src_abduco_dir")"
check_version arm64 "$work/arm64/culibox" env
check_abduco arm64 "$arm64_abduco" env
arm_json=$(arch_json arm64 native)

run_tests amd64 amd64 "$([ -n "$amd64_abduco" ] && echo "$work/amd64" || echo "$src_abduco_dir")"
check_version amd64 "$work/amd64/culibox" arch -x86_64
check_abduco amd64 "$amd64_abduco" arch -x86_64
amd_json=$(arch_json amd64 rosetta2)

# ---- 4. the manual rows -------------------------------------------------------------
# Rows are `ID=pass|fail|na [note]`. A bad row is an error without a tty and is
# asked for again with one. Nothing is ever defaulted to pass.
manual_json=
seen=" "
row_re='^([A-Za-z0-9][A-Za-z0-9._-]*)=([A-Za-z]+)([[:space:]]+(.*))?$'
echo "Run the manual rows with these exact binaries (the record holds their sha256):"
echo "  arm64: $work/arm64/culibox"
echo "  amd64: $work/amd64/culibox (start it with arch -x86_64)"
echo "Mac test guide rows for $tag (ID=pass|fail|na [note]), empty line to finish:"
while IFS= read -r -u 3 line || [ -n "$line" ]; do
  line=${line%$'\r'}
  [ -n "$line" ] || break
  bad=
  if [[ $line =~ $row_re ]]; then
    id=${BASH_REMATCH[1]}
    result=$(printf '%s' "${BASH_REMATCH[2]}" | tr '[:upper:]' '[:lower:]')
    note=${BASH_REMATCH[4]:-}
    case "$result" in
      pass | fail | na) ;;
      *) bad="the result must be pass, fail or na" ;;
    esac
    case "$seen" in
      *" $id "*) bad="row $id was already given" ;;
    esac
  else
    bad="a row is ID=pass|fail|na [note]"
  fi
  if [ -n "$bad" ]; then
    if [ -t 3 ]; then
      echo "not accepted ($bad): $line; enter it again" >&2
      continue
    fi
    die "bad manual row '$line': $bad"
  fi
  seen="$seen$id "
  manual_json="$manual_json${manual_json:+, }{\"id\": $(json_str "$id"), \"result\": $(json_str "$result"), \"note\": $(json_str "$note")}"
done

# ---- 5. build and upload the record -------------------------------------------------
out=$work/out
mkdir -p "$out"
macos=$(sw_vers -productVersion)
chip=$(sysctl -n machdep.cpu.brand_string)
gover=$(go version | sed 's/^go version //')
date_utc=$(date -u +%FT%TZ)
cat >"$work/run.json" <<EOF
{
  "date": $(json_str "$date_utc"),
  "macos": $(json_str "$macos"),
  "chip": $(json_str "$chip"),
  "go_version": $(json_str "$gover"),
  "verifier": $(json_str "$verifier"),
  "archives": {$(json_str "$arm_name"): $(json_str "$arm_sha"), $(json_str "$amd_name"): $(json_str "$amd_sha")},
  "arches": [$arm_json, $amd_json],
  "manual": [$manual_json]
}
EOF

"$rs" record --tag "$tag" --commit "$commit" --in "$prior_arg" --run "$work/run.json" >"$out/macos-verification.json" ||
  die "release-status record failed; nothing was uploaded"
# No `grep | tail`: under pipefail a record without a verdict line would end the
# script here with no message.
verdict=$({ grep '"verdict":' "$out/macos-verification.json" || true; } | tail -1 | sed 's/.*: "\(.*\)".*/\1/')
[ -n "$verdict" ] || die "the record has no verdict line; nothing was uploaded"

# Both versions of the notes are computed before anything is changed, so a release
# without a status block fails here, not half-way.
body=$(gh release view "$tag" --json body -q .body) || die "gh release view $tag failed; nothing was uploaded"
printf '%s' "$body" >"$work/notes.md"
"$rs" rewrite --notes "$work/notes.md" --record "$out/macos-verification.json" >"$work/notes.new" ||
  die "cannot rewrite the platform status block in the notes of $tag; nothing was uploaded"
"$rs" rewrite --notes "$work/notes.md" --not-verified >"$work/notes.cleared" ||
  die "cannot rewrite the platform status block in the notes of $tag; nothing was uploaded"

# edit_notes FILE: gh release edit, three tries.
edit_notes() {
  local i
  for i in 1 2 3; do
    if gh release edit "$tag" --notes-file - <"$1"; then
      return 0
    fi
    if [ "$i" -lt 3 ]; then
      echo "macos-verify: gh release edit failed (attempt $i of 3), retrying" >&2
      sleep "${MACOS_VERIFY_RETRY_DELAY:-3}"
    fi
  done
  return 1
}

# If the notes show anything but "NOT yet verified" for the darwin lines (a tick for an
# earlier run, however it is spaced: the cleared text is computed by release-status, so
# there is no second reading of the notes here), clear it BEFORE the new record is
# uploaded. The notes edit after the upload can fail, and then the notes would keep
# the older tick next to a record that says partial or failed. Cleared first, the
# worst a failed final edit leaves is "NOT yet verified".
if ! cmp -s "$work/notes.md" "$work/notes.cleared"; then
  echo "clearing the earlier darwin status from the notes of $tag"
  edit_notes "$work/notes.cleared" ||
    die "cannot edit the notes of $tag; nothing was uploaded and the record is unchanged"
fi

gh release upload "$tag" "$out/macos-verification.json" --clobber ||
  die "gh release upload failed; macos-verification.json may have been replaced or removed (--clobber), so check the release's assets; any earlier tick in the notes was already cleared"
# ---- 6. the status block ------------------------------------------------------------
edit_notes "$work/notes.new" ||
  die "macos-verification.json is uploaded (verdict $verdict) but the notes were NOT updated and now say NOT yet verified (an earlier tick was cleared); run this again for $tag"

echo "$tag (commit $commit): verdict $verdict; macos-verification.json uploaded and the notes updated"
[ "$verdict" = verified ] || exit 3
