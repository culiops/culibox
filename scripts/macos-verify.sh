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
#   2. clones <tag>, installs the pinned abduco, and runs the Go tests natively
#      (arm64) and under Rosetta 2 (GOARCH=amd64)
#   3. runs each extracted binary's `version` and requires exactly
#      `agent-box <tag> (<commit of the clone>)`; amd64 runs under `arch -x86_64`
#   4. asks for the manual Mac-test-guide rows (never defaults one to pass)
#   5. builds macos-verification.json with tools/release-status and uploads it to
#      the release with --clobber (earlier runs stay in its runs[])
#   6. rewrites the darwin lines of the release notes' platform status block
#
# Exit status: 0 verified; 3 the record was written but the verdict is not
# "verified" (partial or failed: the release now says so); 1 any other failure,
# with nothing uploaded unless the message says otherwise; 2 usage.
# A failing test run does NOT stop the script: it is recorded honestly as failed.
# Only integrity failures abort before an upload: bad sums, or a tag/commit that
# does not match between the clone, the stamp and an earlier record.
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
#   TMPDIR                       where the work dir goes
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
if ! printf '%s\n' "$tag" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
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

# Rosetta must be installed, or amd64 binaries cannot run at all.
arch -x86_64 true >/dev/null 2>&1 ||
  die "Rosetta 2 is not usable (arch -x86_64 true failed); run: softwareupdate --install-rosetta --agree-to-license"

for pair in "arm64:$arm_name" "amd64:$amd_name"; do
  mkdir -p "$work/${pair%%:*}"
  # Only the binary is extracted: a tarball is untrusted data until it is run.
  tar -xzf "$dl/${pair#*:}" -C "$work/${pair%%:*}" agent-box ||
    die "cannot extract agent-box from ${pair#*:}"
done

# ---- 2. the tag's source, abduco, the tests ----------------------------------------
echo "cloning $tag"
git -c advice.detachedHead=false clone --quiet --depth 1 --branch "$tag" "$git_url" "$work/src" ||
  die "cannot clone $tag from $git_url"
commit=$(git -C "$work/src" rev-parse HEAD)
tag_commit=$(git -C "$work/src" rev-parse "refs/tags/$tag^{commit}" 2>/dev/null) ||
  die "$tag is not a tag in $git_url"
[ "$commit" = "$tag_commit" ] || die "the clone's HEAD $commit is not tag $tag ($tag_commit)"

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

echo "installing the pinned abduco"
"$installer" "$work/abduco" >"$work/abduco-install.log" 2>&1 || {
  cat "$work/abduco-install.log" >&2
  die "the abduco installer failed"
}
export PATH="$work/abduco/bin:$PATH"

# run_tests ARCH GOARCH: go test ./... in the clone, logged to test-<arch>.log. A
# failing run sets T_PASS=false and does not stop the script.
T_PASS=
T_SUMMARY=
run_tests() {
  local arch=$1 goarch=$2 log="$work/test-$1.log" n
  echo "go test ./... ($arch, GOARCH=$goarch)"
  if (cd "$work/src" && GOARCH=$goarch AGENT_BOX_REQUIRE_ABDUCO=1 go test -count=1 ./...) 2>&1 | tee "$log"; then
    T_PASS=true
    n=$(grep -c '^ok' "$log" || true)
    T_SUMMARY="go test ./... ok: $n package(s)"
  else
    T_PASS=false
    n=$(grep -cE '^(--- )?FAIL' "$log" || true)
    T_SUMMARY="go test failed: $n FAIL line(s), see test-$arch.log"
  fi
}

# check_version ARCH BINARY [RUNNER...]: the binary's `version` must equal exactly
# `agent-box <tag> (<commit>)`. V_LINE keeps what it printed either way.
V_LINE=
V_OK=
check_version() {
  local arch=$1 bin=$2
  shift 2
  local want="agent-box $tag ($commit)"
  if V_LINE=$("$@" "$bin" version 2>&1); then :; else V_LINE="$V_LINE (exit $?)"; fi
  if [ "$V_LINE" = "$want" ]; then
    V_OK=true
  else
    V_OK=false
    echo "macos-verify: $arch version stamp mismatch: got '$V_LINE', want '$want'" >&2
  fi
}

# arch_json ARCH MODE: this arch's result, from T_PASS/T_SUMMARY and V_LINE/V_OK. A
# wrong version stamp fails the arch even when its tests pass.
arch_json() {
  local passed=$T_PASS summary=$T_SUMMARY
  if [ "$V_OK" != true ]; then
    passed=false
    summary="$summary; version stamp mismatch (want agent-box $tag ($commit))"
  fi
  printf '{"arch": %s, "mode": %s, "tests_passed": %s, "test_summary": %s, "version_line": %s}' \
    "$(json_str "$1")" "$(json_str "$2")" "$passed" "$(json_str "$summary")" "$(json_str "$V_LINE")"
  echo "$1 ($2): $([ "$passed" = true ] && echo PASS || echo FAIL): $summary" >&2
}

run_tests arm64 arm64
check_version arm64 "$work/arm64/agent-box" env
arm_json=$(arch_json arm64 native)

run_tests amd64 amd64
check_version amd64 "$work/amd64/agent-box" arch -x86_64
amd_json=$(arch_json amd64 rosetta2)

# ---- 4. the manual rows -------------------------------------------------------------
# Rows are `ID=pass|fail|na [note]`. A bad row is an error without a tty and is
# asked for again with one. Nothing is ever defaulted to pass.
manual_json=
seen=" "
row_re='^([A-Za-z0-9][A-Za-z0-9._-]*)=([A-Za-z]+)([[:space:]]+(.*))?$'
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
verdict=$(grep '"verdict":' "$out/macos-verification.json" | tail -1 | sed 's/.*: "\(.*\)".*/\1/')
[ -n "$verdict" ] || die "the record has no verdict; nothing was uploaded"

# The new notes are computed before anything is uploaded, so a release without a
# status block fails here, not half-way.
body=$(gh release view "$tag" --json body -q .body) || die "gh release view $tag failed; nothing was uploaded"
printf '%s' "$body" >"$work/notes.md"
"$rs" rewrite --notes "$work/notes.md" --record "$out/macos-verification.json" >"$work/notes.new" ||
  die "cannot rewrite the platform status block in the notes of $tag; nothing was uploaded"

gh release upload "$tag" "$out/macos-verification.json" --clobber ||
  die "gh release upload failed; nothing was changed"
# ---- 6. the status block ------------------------------------------------------------
gh release edit "$tag" --notes-file - <"$work/notes.new" ||
  die "macos-verification.json is uploaded but the notes were NOT updated; run this again for $tag"

echo "$tag (commit $commit): verdict $verdict; macos-verification.json uploaded and the notes updated"
[ "$verdict" = verified ] || exit 3
