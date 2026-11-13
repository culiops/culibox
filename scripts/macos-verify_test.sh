#!/usr/bin/env bash
# Offline test for scripts/macos-verify.sh: run it with `scripts/macos-verify_test.sh`.
#
# The script's every external effect is stubbed or pointed at a fixture, so this
# needs no network, no Mac, no ~/.config/gh and no real abduco:
#   gh, go, sw_vers, sysctl, arch, uname   shell stubs in a PATH dir (go serves only
#                                          `test` and `version`)
#   release-status                         the REAL tool, built here with the real go
#   git clone                              a local fixture repo that has the tag
#   abduco installer                       a stub that drops an executable abduco
# The "binaries" in the fixture tarballs are tiny shell scripts that print the
# expected `culibox <tag> (<sha>)` line. Everything lives in one mktemp dir that is
# removed on exit.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
script=$here/macos-verify.sh

# Found before PATH is shadowed. Used for exactly one thing: building release-status.
real_go=$(command -v go) || {
  echo "macos-verify_test: go is not on PATH" >&2
  exit 1
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

tag=v0.0.1
tarver=0.0.1
arm_tgz=culibox_${tarver}_darwin_arm64.tar.gz
amd_tgz=culibox_${tarver}_darwin_amd64.tar.gz

fails=0
fail() {
  echo "FAIL: $*" >&2
  fails=$((fails + 1))
}
pass() { echo "ok:   $*"; }
# contains FILE TEXT MESSAGE: TEXT (a fixed string) must be in FILE.
contains() {
  if grep -qF -- "$2" "$1" 2>/dev/null; then pass "$3"; else
    fail "$3 (no '$2' in $1)"
    sed 's/^/        | /' "$1" 2>/dev/null | head -20 >&2 || true
  fi
}
absent() { # FILE TEXT MESSAGE
  if grep -qF -- "$2" "$1" 2>/dev/null; then fail "$3 ('$2' is in $1)"; else pass "$3"; fi
}
missing() { # PATH MESSAGE
  if [ -e "$1" ]; then fail "$2 ($1 exists)"; else pass "$2"; fi
}
exists() { # PATH MESSAGE
  if [ -e "$1" ]; then pass "$2"; else fail "$2 ($1 is missing)"; fi
}
expect_rc() { # ACTUAL WANT MESSAGE
  if [ "$1" -eq "$2" ]; then pass "$3"; else
    fail "$3 (exit $1, want $2)"
    sed 's/^/        | /' "${case_dir:-/nonexistent}/stderr" 2>/dev/null | tail -8 >&2 || true
  fi
}
last_verdict() { grep '"verdict":' "$1" | tail -1 | sed 's/.*: "\(.*\)".*/\1/'; }
expect_verdict() { # FILE WANT MESSAGE
  local got
  got=$(last_verdict "$1" 2>/dev/null || true)
  if [ "$got" = "$2" ]; then pass "$3"; else fail "$3 (verdict '$got', want '$2')"; fi
}

# release-status first, with the real environment, so the Go build cache is warm.
(cd "$repo" && "$real_go" build -o "$tmp/release-status" ./tools/release-status)

# Hermetic PATH: the stubs plus symlinks to exactly the tools the script and this
# test use, resolved NOW (before PATH is narrowed). The real gh and go are not on it,
# so a stub that is missing or wrong cannot be papered over by the real tool.
mkdir -p "$tmp/tools"
for t in bash sh env git tar gzip sed grep cut tr head tail find mktemp cat cp mkdir rm sort date dirname basename sha256sum tee cmp wc ls touch chmod ln sleep true; do
  p=$(type -P "$t") || {
    echo "macos-verify_test: $t is not on PATH" >&2
    exit 1
  }
  ln -s "$p" "$tmp/tools/$t"
done
hermetic_path=$tmp/bin:$tmp/tools

# From here on nothing may read the real home: no ~/.config/gh, no global git config.
export HOME=$tmp/home
mkdir -p "$HOME"
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1

# ---- the fixture repo the script clones at the tag ---------------------------------
# The release tag is on main, as a real one is.
git init -q -b main "$tmp/repo"
(
  cd "$tmp/repo"
  echo "fixture" >README.md
  git add README.md
  git -c user.name=t -c user.email=t@example.com -c commit.gpgsign=false commit -q -m fixture
  git -c user.name=t -c user.email=t@example.com -c tag.gpgsign=false tag -a "$tag" -m "$tag"
  # main moves on after the release: the tag stays an ancestor of main.
  echo more >>README.md
  git -c user.name=t -c user.email=t@example.com -c commit.gpgsign=false commit -q -am later
)
sha=$(git -C "$tmp/repo" rev-parse "$tag^{commit}")

# A second fixture whose tag points at a commit that is NOT on main (a tag pushed
# from a side branch): the same tag name, so the release would look ordinary.
git init -q -b main "$tmp/repo-offmain"
(
  cd "$tmp/repo-offmain"
  echo "fixture" >README.md
  git add README.md
  git -c user.name=t -c user.email=t@example.com -c commit.gpgsign=false commit -q -m fixture
  git checkout -q -b side
  echo side >>README.md
  git -c user.name=t -c user.email=t@example.com -c commit.gpgsign=false commit -q -am side
  git -c user.name=t -c user.email=t@example.com -c tag.gpgsign=false tag -a "$tag" -m "$tag"
  git checkout -q main
)
offmain_sha=$(git -C "$tmp/repo-offmain" rev-parse "$tag^{commit}")

# ---- stubs --------------------------------------------------------------------------
mkdir -p "$tmp/bin"
cat >"$tmp/bin/gh" <<'EOF'
#!/bin/sh
echo "GH_REPO=${GH_REPO:-} $*" >>"$STUB_OUT/gh.log"
case "$1 $2" in
"release download")
  dir=
  prev=
  for a in "$@"; do
    [ "$prev" = --dir ] && dir=$a
    prev=$a
  done
  [ -n "$dir" ] || { echo "stub gh: download without --dir" >&2; exit 1; }
  cp "$STUB_FIX"/culibox_*_darwin_*.tar.gz "$STUB_FIX/SHA256SUMS" "$dir/"
  if [ -f "$STUB_FIX/macos-verification.json" ]; then cp "$STUB_FIX/macos-verification.json" "$dir/"; fi
  ;;
"release upload")
  [ -z "${STUB_UPLOAD_FAIL:-}" ] || { echo "stub gh: upload failed" >&2; exit 1; }
  clobber=no
  for a in "$@"; do [ "$a" = --clobber ] && clobber=yes; done
  [ "$clobber" = yes ] || { echo "stub gh: upload without --clobber" >&2; exit 1; }
  cp "$4" "$STUB_OUT/uploaded.json"
  basename "$4" >"$STUB_OUT/uploaded.name"
  ;;
"release view")
  # The notes as the last successful edit left them, else the fixture's.
  if [ -f "$STUB_OUT/current-notes.md" ]; then cat "$STUB_OUT/current-notes.md"; else cat "$STUB_FIX/notes.md"; fi
  echo
  ;;
"release edit")
  [ "$4 $5" = "--notes-file -" ] || { echo "stub gh: edit wants --notes-file -, got: $*" >&2; exit 1; }
  n=$(cat "$STUB_OUT/edit.count" 2>/dev/null || echo 0)
  n=$((n + 1))
  echo "$n" >"$STUB_OUT/edit.count"
  cat >"$STUB_OUT/edit-$n.md"
  # STUB_EDIT_FAIL_FROM=N fails edit N and every later one; STUB_EDIT_FAIL_ONLY=N only edit N.
  if { [ -n "${STUB_EDIT_FAIL_FROM:-}" ] && [ "$n" -ge "$STUB_EDIT_FAIL_FROM" ]; } || [ "${STUB_EDIT_FAIL_ONLY:-}" = "$n" ]; then
    echo "stub gh: release edit #$n failed" >&2
    exit 1
  fi
  cp "$STUB_OUT/edit-$n.md" "$STUB_OUT/current-notes.md"
  cp "$STUB_OUT/edit-$n.md" "$STUB_OUT/notes.out"
  ;;
"repo view")
  [ "$3 $4 $5 $6" = "$GH_REPO --json visibility -q" ] || { echo "stub gh: repo view wants <repo> --json visibility -q .visibility, got: $*" >&2; exit 1; }
  # STUB_VISIBILITY=EMPTY makes gh print nothing, as it would on a broken response.
  if [ "${STUB_VISIBILITY:-PRIVATE}" = EMPTY ]; then echo; else echo "${STUB_VISIBILITY:-PRIVATE}"; fi
  ;;
"attestation verify")
  [ -z "${STUB_ATTEST_FAIL:-}" ] || { echo "stub gh: attestation verification failed" >&2; exit 1; }
  ;;
"api user")
  [ "$3 $4" = "-q .login" ] || { echo "stub gh: api user wants -q .login" >&2; exit 1; }
  echo testverifier
  ;;
*)
  echo "stub gh: unexpected call: $*" >&2
  exit 1
  ;;
esac
EOF
cat >"$tmp/bin/go" <<'EOF'
#!/bin/sh
case "$1" in
version) echo "go version go1.99.0 darwin/arm64" ;;
test)
  # EFFECTIVE_GOFLAGS is what the real go would use in this very environment: a GOFLAGS
  # that is merely empty falls back to the GOENV file, so the variable alone proves nothing.
  echo "GOENV=${GOENV:-} GOOS=${GOOS:-} GOARCH=${GOARCH:-} CGO=${CGO_ENABLED:-} GOFLAGS=${GOFLAGS:-} EFFECTIVE_GOFLAGS=$("$STUB_REAL_GO" env GOFLAGS 2>/dev/null) REQUIRE=${AGENT_BOX_REQUIRE_ABDUCO:-} PWD=$PWD ABDUCO=$(command -v abduco || echo none) ARGS=$*" >>"$STUB_OUT/go.log"
  : >"$STUB_OUT/go-called.marker"
  if [ "${GOARCH:-}" = amd64 ] && [ -n "${STUB_GO_FAIL_AMD64:-}" ]; then
    printf '=== RUN   TestX\n--- FAIL: TestX (0.00s)\nFAIL\nFAIL\tfixture/a\t0.1s\nFAIL\n'
    exit 1
  fi
  # go test -v: two packages, three top-level passes, one skip, one passing subtest.
  printf '=== RUN   TestA1\n--- PASS: TestA1 (0.00s)\n=== RUN   TestA2\n=== RUN   TestA2/sub\n    --- PASS: TestA2/sub (0.00s)\n--- PASS: TestA2 (0.00s)\nPASS\nok  \tfixture/a\t0.01s\n'
  printf '=== RUN   TestB1\n--- PASS: TestB1 (0.00s)\n=== RUN   TestB2\n--- SKIP: TestB2 (0.00s)\nPASS\nok  \tfixture/b\t0.01s\n'
  ;;
*)
  echo "stub go: unexpected call: $*" >&2
  exit 1
  ;;
esac
EOF
cat >"$tmp/bin/sw_vers" <<'EOF'
#!/bin/sh
[ "$1" = -productVersion ] || { echo "stub sw_vers: unexpected: $*" >&2; exit 1; }
echo 15.1
EOF
cat >"$tmp/bin/sysctl" <<'EOF'
#!/bin/sh
[ "$*" = "-n machdep.cpu.brand_string" ] || { echo "stub sysctl: unexpected: $*" >&2; exit 1; }
echo "Apple M2"
EOF
cat >"$tmp/bin/arch" <<'EOF'
#!/bin/sh
echo "$*" >>"$STUB_OUT/arch.log"
[ "$1" = -x86_64 ] || { echo "stub arch: want -x86_64, got: $*" >&2; exit 1; }
shift
exec "$@"
EOF
cat >"$tmp/bin/uname" <<'EOF'
#!/bin/sh
case "$1" in
-s) echo Linux ;;
-m) echo x86_64 ;;
*) echo "stub uname: unexpected: $*" >&2; exit 1 ;;
esac
EOF
cat >"$tmp/bin/install-abduco" <<'EOF'
#!/bin/sh
echo "$*" >>"$STUB_OUT/abduco-install.log"
[ -z "${STUB_ABDUCO_FAIL:-}" ] || { echo "stub abduco: cc not found" >&2; exit 1; }
mkdir -p "$1/bin"
printf '#!/bin/sh\nexit 0\n' >"$1/bin/abduco"
chmod 755 "$1/bin/abduco"
EOF
# A release-status that drops the verdict line from `record`, to show that a missing
# verdict is reported and not a silent exit.
cat >"$tmp/bin/rs-noverdict" <<EOF
#!/bin/sh
if [ "\$1" = record ]; then "$tmp/release-status" "\$@" | grep -v '"verdict":'; else exec "$tmp/release-status" "\$@"; fi
EOF
chmod 755 "$tmp"/bin/*

# ---- one case = fixtures + out + tmpdir --------------------------------------------
# new_case NAME [AMD64_SHA]: tarballs whose binaries print `culibox v0.0.1 (<sha>)`;
# AMD64_SHA lets one case ship an amd64 binary stamped with another commit.
new_case() {
  case_dir=$tmp/$1
  fix=$case_dir/fix
  out=$case_dir/out
  mkdir -p "$fix" "$out" "$case_dir/tmpdir"
  make_tarball arm64 "$sha"
  make_tarball amd64 "${2:-$sha}"
  (cd "$fix" && sha256sum culibox_*.tar.gz >SHA256SUMS)
  echo '[]' >"$case_dir/releases.json"
  {
    printf '## %s\n\n' "$tag"
    "$tmp/release-status" notes --tag "$tag" --ci-run https://ci.example/run/1 --releases "$case_dir/releases.json"
  } >"$fix/notes.md"
}
make_tarball() { # ARCH SHA
  local stage=$case_dir/stage-$1
  mkdir -p "$stage"
  printf '%s\n' '#!/bin/sh' "[ \"\$1\" = version ] && echo \"culibox $tag ($2)\"" >"$stage/culibox"
  chmod 755 "$stage/culibox"
  echo license >"$stage/LICENSE"
  echo readme >"$stage/README.md"
  tar -C "$stage" -czf "$fix/culibox_${tarver}_darwin_$1.tar.gz" culibox LICENSE README.md
}

# run_script INPUT [ENV=VALUE...]: runs the script in the current case. Sets rc; the
# output lands in $case_dir/stdout and $case_dir/stderr.
run_script() {
  local input=$1
  shift
  if printf '%s' "$input" | env \
    PATH="$hermetic_path" TMPDIR="$case_dir/tmpdir" \
    MACOS_VERIFY_RETRY_DELAY=0 \
    STUB_FIX="$fix" STUB_OUT="$out" STUB_REAL_GO="$real_go" \
    MACOS_VERIFY_TEST=1 \
    RELEASE_STATUS="$tmp/release-status" \
    MACOS_VERIFY_GIT_URL="file://$tmp/repo" \
    MACOS_VERIFY_INSTALL_ABDUCO="$tmp/bin/install-abduco" \
    "$@" \
    "$script" "$tag" >"$case_dir/stdout" 2>"$case_dir/stderr"; then
    rc=0
  else
    rc=$?
  fi
}

echo "== RED check: the script exists and is executable"
if [ ! -x "$script" ]; then
  echo "macos-verify_test: $script is missing or not executable" >&2
  exit 1
fi

# ---- (a) a good run with M1=pass is verified ---------------------------------------
echo "== (a) good run, M1=pass"
new_case a
run_script $'M1=pass looks fine\nM2=na said "hi" \\ done\n\n'
expect_rc "$rc" 0 "(a) a verified run exits 0"
exists "$out/uploaded.json" "(a) the record was uploaded"
contains "$out/uploaded.name" macos-verification.json "(a) the asset is named macos-verification.json"
expect_verdict "$out/uploaded.json" verified "(a) the last run's verdict is verified"
contains "$out/notes.out" "✅ verified on macOS 15.1 (Apple M2), arm64 native, amd64 under Rosetta 2" "(a) the notes say verified on macOS"
contains "$out/notes.out" "- linux/amd64: source tests passed in CI on a native runner (cgo, -race); this tarball ran and reported its version, [run](https://ci.example/run/1)" "(a) the Linux lines survive the rewrite"
contains "$out/notes.out" "- linux/arm64: source tests passed in CI on a native runner (cgo, -race); this tarball was not run, [run](https://ci.example/run/1)" "(a) the linux/arm64 line says its tarball was not run"
contains "$out/notes.out" "go test with CGO_ENABLED=0" "(a) the tick says what was checked"
contains "$out/notes.out" "## $tag" "(a) the rest of the notes survives"
for f in "$arm_tgz" "$amd_tgz"; do
  contains "$out/uploaded.json" "\"$f\": \"$(grep " $f\$" "$fix/SHA256SUMS" | cut -d ' ' -f 1)\"" "(a) the record holds the verified sha256 of $f"
done
contains "$out/uploaded.json" '"mode": "native"' "(a) arm64 is recorded native"
contains "$out/uploaded.json" '"mode": "rosetta2"' "(a) amd64 is recorded rosetta2"
contains "$out/uploaded.json" "\"version_line\": \"culibox $tag ($sha)\"" "(a) the version stamp is recorded"
contains "$out/uploaded.json" '"verifier": "testverifier"' "(a) the verifier comes from gh api user"
contains "$out/uploaded.json" '"macos": "15.1"' "(a) the macOS version is recorded"
contains "$out/uploaded.json" '"chip": "Apple M2"' "(a) the chip is recorded"
contains "$out/uploaded.json" '"go_version": "go1.99.0 darwin/arm64"' "(a) the Go version is recorded"
contains "$out/uploaded.json" '"id": "M1"' "(a) row M1 is recorded"
contains "$out/uploaded.json" '"note": "said \"hi\" \\ done"' "(a) a note with a quote and a backslash is escaped"
contains "$out/go.log" "GOENV=off GOOS=darwin GOARCH=arm64 CGO=0 GOFLAGS=-mod=readonly EFFECTIVE_GOFLAGS=-mod=readonly REQUIRE=1" "(a) the native run ignores the go env file, pins GOOS, GOARCH, CGO_ENABLED=0 (the shipped build) and GOFLAGS, and sets AGENT_BOX_REQUIRE_ABDUCO=1"
contains "$out/go.log" "GOENV=off GOOS=darwin GOARCH=amd64 CGO=0 GOFLAGS=-mod=readonly EFFECTIVE_GOFLAGS=-mod=readonly REQUIRE=1" "(a) the Rosetta run pins GOARCH=amd64 and the rest"
contains "$out/go.log" "ARGS=test -count=1 -v ./..." "(a) go test runs with -count=1 -v ./..."
contains "$out/uploaded.json" '"test_summary": "go test ./... ok: 3 test(s) passed, 1 skipped, in 2 package(s)"' "(a) the summary counts tests, not packages"
contains "$out/uploaded.json" '"test_env": "CGO_ENABLED=0 GOOS=darwin GOARCH=arm64"' "(a) the record says the tests ran with cgo off"
for a in arm64 amd64; do
  contains "$out/uploaded.json" "\"binary_sha256\": \"$(sha256sum "$case_dir/stage-$a/culibox" | cut -d ' ' -f 1)\"" "(a) the record holds the sha256 of the $a binary that was run"
done
contains "$case_dir/stdout" "Run the manual rows with these exact binaries" "(a) the prompt names the binaries the rows must use"
contains "$case_dir/stdout" "arm64: $case_dir/tmpdir/" "(a) the arm64 binary path is printed"
contains "$out/go.log" "PWD=$case_dir/tmpdir/" "(a) go test runs inside the work dir's clone"
absent "$out/go.log" "ABDUCO=none" "(a) the installed abduco is on PATH for go test"
contains "$out/arch.log" "-x86_64 $case_dir/tmpdir/" "(a) the amd64 binary runs under arch -x86_64"
contains "$case_dir/stdout" "Mac test guide rows for $tag (ID=pass|fail|na [note]), empty line to finish:" "(a) the manual prompt is exact"
contains "$out/gh.log" "GH_REPO=culiops/culibox " "(a) gh is pointed at culiops/culibox"
up=$(grep -n 'release upload' "$out/gh.log" | head -1 | cut -d: -f1)
ed=$(grep -n 'release edit' "$out/gh.log" | head -1 | cut -d: -f1)
if [ -n "$up" ] && [ -n "$ed" ] && [ "$up" -lt "$ed" ]; then pass "(a) the record is uploaded before the notes are edited"; else fail "(a) upload (line '$up') must come before edit (line '$ed') in gh.log"; fi
if [ -z "$(ls -A "$case_dir/tmpdir")" ]; then pass "(a) nothing is left in TMPDIR"; else fail "(a) TMPDIR still holds: $(ls "$case_dir/tmpdir")"; fi

# ---- (d) a second run for the same tag keeps the history ---------------------------
echo "== (d) re-run keeps runs[]"
new_case d
cp "$tmp/a/out/uploaded.json" "$fix/macos-verification.json"
run_script $'M1=pass\n\n'
expect_rc "$rc" 0 "(d) a re-run exits 0"
n=$(grep -c '"verdict":' "$out/uploaded.json" || true)
if [ "$n" = 2 ]; then pass "(d) the re-run's record holds both runs"; else fail "(d) the record holds $n runs, want 2"; fi

# ---- (b) a tampered tarball stops before go test -----------------------------------
echo "== (b) tampered tarball"
new_case b
printf 'x' >>"$fix/$arm_tgz"
run_script $'M1=pass\n\n'
if [ "$rc" -ne 0 ]; then pass "(b) a tampered tarball makes the script exit non-zero"; else fail "(b) a tampered tarball exited 0"; fi
missing "$out/go-called.marker" "(b) go test never ran"
missing "$out/arch.log" "(b) no binary ran under arch"
missing "$out/abduco-install.log" "(b) abduco was not installed"
missing "$out/uploaded.json" "(b) nothing was uploaded"
missing "$out/notes.out" "(b) the notes were not edited"
contains "$case_dir/stderr" "mismatch" "(b) the error names the sha mismatch"
contains "$case_dir/stderr" "$case_dir/tmpdir/macos-verify." "(b) the failed run says where its logs are"

# ---- (c) empty manual input is partial ---------------------------------------------
echo "== (c) empty manual input"
new_case c
run_script ''
expect_rc "$rc" 3 "(c) a recorded non-verified run exits 3"
expect_verdict "$out/uploaded.json" partial "(c) empty manual input gives verdict partial"
contains "$out/notes.out" "⚠️ automated checks passed, manual checks not run" "(c) the notes say manual checks did not run"
absent "$out/notes.out" "✅" "(c) the notes never show a green tick"
contains "$out/uploaded.json" '"manual": []' "(c) no manual row is invented"

# ---- (e) a failing amd64 test run is recorded as failed ----------------------------
echo "== (e) go test fails under Rosetta"
new_case e
run_script $'M1=pass\n\n' STUB_GO_FAIL_AMD64=1
expect_rc "$rc" 3 "(e) a failed run exits 3"
expect_verdict "$out/uploaded.json" failed "(e) the verdict is failed"
contains "$out/notes.out" "❌ failed: amd64 tests failed" "(e) the notes name the failing arch"
contains "$out/uploaded.json" '"test_summary": "go test failed' "(e) the summary says go test failed"
exists "$case_dir/tmpdir"/macos-verify.*/test-amd64.log "(e) the amd64 log is kept"

# ---- (f) a binary stamped with another commit fails its arch -----------------------
echo "== (f) version stamp mismatch"
new_case f "0000000000000000000000000000000000000000"
run_script $'M1=pass\n\n'
expect_rc "$rc" 3 "(f) a stamp mismatch exits 3"
expect_verdict "$out/uploaded.json" failed "(f) the verdict is failed"
contains "$out/notes.out" "amd64 tests failed" "(f) amd64 is the failing arch"
absent "$out/notes.out" "arm64 tests failed" "(f) arm64 still passes"
contains "$out/uploaded.json" "culibox $tag (0000000000000000000000000000000000000000)" "(f) the bad version_line is kept"

# ---- (g) a prior record for another commit aborts before go test -------------------
echo "== (g) moved tag"
new_case g
sed "s/\"commit\": \"[0-9a-f]*\"/\"commit\": \"1111111111111111111111111111111111111111\"/" "$tmp/a/out/uploaded.json" >"$fix/macos-verification.json"
run_script $'M1=pass\n\n'
if [ "$rc" -ne 0 ]; then pass "(g) a record for another commit exits non-zero"; else fail "(g) exit 0 on a commit mismatch"; fi
missing "$out/go-called.marker" "(g) go test never ran"
missing "$out/uploaded.json" "(g) nothing was uploaded"

# ---- (h) a malformed manual row is an error without a tty --------------------------
echo "== (h) malformed manual row"
new_case h
run_script $'M1=yes\n\n'
if [ "$rc" -ne 0 ]; then pass "(h) a malformed row exits non-zero without a tty"; else fail "(h) a malformed row exited 0"; fi
missing "$out/uploaded.json" "(h) nothing was uploaded"
contains "$case_dir/stderr" "M1=yes" "(h) the error quotes the bad row"

# ---- (i) it refuses to run off a Mac -----------------------------------------------
echo "== (i) refuses off Darwin/arm64"
new_case i
run_script '' MACOS_VERIFY_TEST=0
if [ "$rc" -ne 0 ]; then pass "(i) it exits non-zero off a Mac"; else fail "(i) it ran off a Mac"; fi
contains "$case_dir/stderr" "Darwin" "(i) the error names Darwin"
missing "$out/gh.log" "(i) gh was never called"

# ---- (k) a tag that is not on main stops before anything runs --------------------
# The release workflow's own "tag is on main" check lives in the tagged commit, so a
# tag pushed with an edited workflow skips it. The Mac must not trust that check.
echo "== (k) tag not on main"
new_case k "$offmain_sha"
run_script $'M1=pass\n\n' MACOS_VERIFY_GIT_URL="file://$tmp/repo-offmain"
if [ "$rc" -ne 0 ]; then pass "(k) a tag that is not on main exits non-zero"; else fail "(k) a tag off main was verified"; fi
contains "$case_dir/stderr" "not on main" "(k) the error says the tag is not on main"
missing "$out/go-called.marker" "(k) go test never ran"
missing "$out/arch.log" "(k) no binary ran"
missing "$out/abduco-install.log" "(k) abduco was not installed"
missing "$out/uploaded.json" "(k) nothing was uploaded"
missing "$out/notes.out" "(k) the notes were not edited"

# ---- (m) public repo: the tarballs must carry a provenance attestation -----------
echo "== (m) attestation, public repo"
new_case m
run_script $'M1=pass\n\n' STUB_VISIBILITY=PUBLIC
expect_rc "$rc" 0 "(m) a public repo with valid attestations is verified"
for f in "$arm_tgz" "$amd_tgz"; do
  contains "$out/gh.log" "attestation verify $case_dir/tmpdir/" "(m) $f is attestation-verified"
done
# An exact identity (the whole workflow URL with this tag's ref), not --signer-workflow:
# older gh versions match that flag as a prefix, so a look-alike workflow file name
# would satisfy it. --deny-self-hosted-runners keeps a runner the owner does not host out.
contains "$out/gh.log" "--cert-identity https://github.com/culiops/culibox/.github/workflows/release.yml@refs/tags/$tag" "(m) the signer is pinned to release.yml at this tag by exact identity"
contains "$out/gh.log" "--deny-self-hosted-runners" "(m) attestations from self-hosted runners are refused"
absent "$out/gh.log" "--signer-workflow" "(m) no prefix-matched --signer-workflow"
contains "$out/gh.log" "--source-ref refs/tags/$tag" "(m) the attestation must be for this tag"
contains "$out/gh.log" "--source-digest $sha" "(m) the attestation must be for the tag's commit"

echo "== (m2) attestation fails"
new_case m2
run_script $'M1=pass\n\n' STUB_VISIBILITY=PUBLIC STUB_ATTEST_FAIL=1
if [ "$rc" -ne 0 ]; then pass "(m2) a failed attestation exits non-zero"; else fail "(m2) a failed attestation was ignored"; fi
contains "$case_dir/stderr" "attestation" "(m2) the error names the attestation"
missing "$out/go-called.marker" "(m2) go test never ran"
missing "$out/arch.log" "(m2) no binary ran"
missing "$out/uploaded.json" "(m2) nothing was uploaded"

# Only an exact PRIVATE skips the check; any other answer (INTERNAL, empty, garbage) is an
# error, never a quiet skip.
for v in INTERNAL EMPTY public Private; do
  echo "== (m4) visibility '$v'"
  new_case m4
  run_script $'M1=pass\n\n' STUB_VISIBILITY="$v"
  if [ "$rc" -ne 0 ]; then pass "(m4) visibility '$v' exits non-zero"; else fail "(m4) visibility '$v' was accepted"; fi
  contains "$case_dir/stderr" "visibility" "(m4) the error names the visibility"
  missing "$out/go-called.marker" "(m4) go test never ran for '$v'"
  absent "$out/gh.log" "attestation verify" "(m4) no attestation check for '$v'"
done

echo "== (m3) attestation, private repo"
new_case m3
run_script $'M1=pass\n\n' STUB_VISIBILITY=PRIVATE
expect_rc "$rc" 0 "(m3) a private repo (no attestations exist) still verifies"
absent "$out/gh.log" "attestation verify" "(m3) no attestation check is attempted"
contains "$case_dir/stderr" "attestation check skipped" "(m3) the skip is announced"
# ---- (n) hermetic: the test PATH reaches no real gh or go ----------------------------
echo "== (n) hermetic PATH"
new_case n
run_script '' PATH="$tmp/tools"
if [ "$rc" -ne 0 ]; then pass "(n) without the stubs the script cannot find gh, whatever the box has"; else fail "(n) it ran without a gh stub: the PATH reaches a real gh"; fi
contains "$case_dir/stderr" "gh is not on PATH" "(n) the error names gh"
for tool in gh go; do
  got=$(PATH="$hermetic_path" command -v "$tool")
  if [ "$got" = "$tmp/bin/$tool" ]; then pass "(n) $tool on the test PATH is the stub"; else fail "(n) $tool resolves to $got"; fi
done

# ---- (o) the Go environment is pinned ---------------------------------------------
echo "== (o) pinned go environment"
new_case o
# A GOFLAGS set with `go env -w` lives in the GOENV file and survives an empty GOFLAGS.
printf 'GOFLAGS=-tags=sneaky\n' >"$case_dir/goenv"
run_script $'M1=pass\n\n' GOOS=linux CGO_ENABLED=1 GOFLAGS=-race GOARCH=s390x GOENV="$case_dir/goenv"
expect_rc "$rc" 0 "(o) a hostile Go environment still verifies"
contains "$out/go.log" "GOENV=off GOOS=darwin GOARCH=arm64 CGO=0 GOFLAGS=-mod=readonly " "(o) GOENV, GOOS, GOARCH, CGO_ENABLED and GOFLAGS are set by the script, not inherited"
contains "$out/go.log" "EFFECTIVE_GOFLAGS=-mod=readonly" "(o) the flags go really uses are the pinned ones, not the go env file's"
absent "$out/go.log" "sneaky" "(o) a GOFLAGS from the caller's go env file never reaches go test"
absent "$out/go.log" "GOOS=linux" "(o) the caller's GOOS never reaches go test"
absent "$out/go.log" "s390x" "(o) the caller's GOARCH never reaches go test"

# ---- (p) an abduco install failure is recorded, not an abort ----------------------
echo "== (p) abduco install fails"
new_case p
run_script $'M1=pass\n\n' STUB_ABDUCO_FAIL=1
expect_rc "$rc" 3 "(p) a failed abduco install is a recorded run (exit 3)"
expect_verdict "$out/uploaded.json" failed "(p) the verdict is failed"
contains "$out/uploaded.json" "abduco" "(p) the record names the abduco failure"
contains "$out/notes.out" "❌ failed: arm64, amd64 tests failed" "(p) the notes say the tests failed"
missing "$out/go-called.marker" "(p) go test was not run without abduco"
contains "$out/uploaded.json" "culibox $tag ($sha)" "(p) the version stamps are still checked"
exists "$case_dir/tmpdir"/macos-verify.*/abduco-install.log "(p) the install log is kept"

# ---- (q) a record without a verdict line is reported -------------------------------
echo "== (q) missing verdict"
new_case q
run_script $'M1=pass\n\n' RELEASE_STATUS="$tmp/bin/rs-noverdict"
if [ "$rc" -ne 0 ]; then pass "(q) a record without a verdict exits non-zero"; else fail "(q) a record without a verdict exited 0"; fi
contains "$case_dir/stderr" "no verdict" "(q) the error says there is no verdict (not a silent exit)"
missing "$out/uploaded.json" "(q) nothing was uploaded"

# ---- (r) a failed upload says what may have changed --------------------------------
echo "== (r) upload fails"
new_case r
run_script $'M1=pass\n\n' STUB_UPLOAD_FAIL=1
if [ "$rc" -ne 0 ]; then pass "(r) a failed upload exits non-zero"; else fail "(r) a failed upload exited 0"; fi
absent "$case_dir/stderr" "nothing was changed" "(r) it does not claim nothing changed (--clobber may already have replaced the asset)"
contains "$case_dir/stderr" "may have been replaced or removed" "(r) it says the asset may have changed"
missing "$out/notes.out" "(r) the notes were not edited"

# ---- (s) an older tick is never left standing after a failed notes edit ------------
# A release that is already marked verified, verified again, now with a failing
# amd64 run. The old tick is cleared BEFORE the new record is uploaded.
mk_verified_case() { # NAME
  new_case "$1"
  cp "$tmp/a/out/uploaded.json" "$fix/macos-verification.json"
  "$tmp/release-status" rewrite --notes "$fix/notes.md" --record "$fix/macos-verification.json" >"$fix/notes.verified"
  mv "$fix/notes.verified" "$fix/notes.md"
  grep -qF "✅ verified on macOS" "$fix/notes.md" || fail "($1) fixture notes lack the older tick"
}
echo "== (s1) final notes edit fails after an older tick"
mk_verified_case s1
run_script $'M1=pass\n\n' STUB_GO_FAIL_AMD64=1 STUB_EDIT_FAIL_FROM=2
expect_rc "$rc" 1 "(s1) a failed notes edit exits 1"
expect_verdict "$out/uploaded.json" failed "(s1) the new record (failed) was uploaded"
absent "$out/current-notes.md" "✅" "(s1) the notes no longer show the older tick"
contains "$out/current-notes.md" "NOT yet verified on macOS" "(s1) the notes say not verified"
contains "$case_dir/stderr" "the notes were NOT updated" "(s1) the failure is loud"
contains "$case_dir/stderr" "run this again" "(s1) it says what to do"
n=$(cat "$out/edit.count")
if [ "$n" -ge 3 ]; then pass "(s1) the final edit was retried ($n edits)"; else fail "(s1) the final edit was not retried ($n edits)"; fi

echo "== (s2) the first (clearing) edit fails: nothing is uploaded"
mk_verified_case s2
run_script $'M1=pass\n\n' STUB_EDIT_FAIL_FROM=1
expect_rc "$rc" 1 "(s2) a failed clearing edit exits 1"
missing "$out/uploaded.json" "(s2) nothing was uploaded"
contains "$case_dir/stderr" "nothing was uploaded" "(s2) the message is accurate"

echo "== (s3) one final edit fails, the retry works"
mk_verified_case s3
run_script $'M1=pass\n\n' STUB_EDIT_FAIL_ONLY=2
expect_rc "$rc" 0 "(s3) a retried notes edit succeeds"
contains "$out/current-notes.md" "✅ verified on macOS 15.1" "(s3) the final notes show the new tick"
contains "$case_dir/stderr" "retrying" "(s3) the retry is announced"

echo "== (s4) a first verification makes no clearing edit"
new_case s4
run_script $'M1=pass\n\n'
expect_rc "$rc" 0 "(s4) a first verification exits 0"
n=$(cat "$out/edit.count")
if [ "$n" = 1 ]; then pass "(s4) one notes edit when there was no tick to clear"; else fail "(s4) $n notes edits, want 1"; fi

# A tick whose spacing differs (the notes were edited by hand) is still a tick: release-status
# reads it that way, so the clearing step must too.
for variant in "two spaces" "no space" "tab" "no-break space"; do
  echo "== (s5) older tick with spacing variant: $variant"
  mk_verified_case "s5-${variant// /_}"
  case "$variant" in
    "two spaces") sed -i.bak -E 's@^- darwin/(arm64|amd64): @- darwin/\1:   @' "$fix/notes.md" ;;
    "no space") sed -i.bak -E 's@^- darwin/(arm64|amd64): @- darwin/\1:@' "$fix/notes.md" ;;
    "tab") sed -i.bak -E "s@^- darwin/(arm64|amd64): @- darwin/\1:$(printf '\t')@" "$fix/notes.md" ;;
    "no-break space") sed -i.bak -E "s@^- darwin/(arm64|amd64): @- darwin/\1:$(printf '\302\240')@" "$fix/notes.md" ;;
  esac
  rm -f "$fix/notes.md.bak"
  run_script $'M1=pass\n\n' STUB_GO_FAIL_AMD64=1 STUB_EDIT_FAIL_FROM=2
  expect_rc "$rc" 1 "(s5 $variant) a failed final edit exits 1"
  if [ -f "$out/current-notes.md" ] && ! grep -qF "✅" "$out/current-notes.md"; then pass "(s5 $variant) the older tick was cleared before the upload"; else fail "(s5 $variant) the notes still show the older tick"; fi
done

# ---- (j) a bad tag is a usage error ------------------------------------------------
echo "== (j) bad tag"
if "$script" --upload-pack=x >/dev/null 2>&1; then fail "(j) a tag that is not vX.Y.Z was accepted"; else pass "(j) a tag that is not vX.Y.Z is refused"; fi
# A second line must not slip past the format check (grep matches any one line).
for bad in $'v0.0.1\n--upload-pack=x' $'--upload-pack=x\nv0.0.1' $'v0.0.1\n'; do
  if "$script" "$bad" >/dev/null 2>&1; then rc=0; else rc=$?; fi
  expect_rc "$rc" 2 "(j) a multi-line tag is a usage error ($(printf '%s' "$bad" | tr '\n' '|'))"
done

echo
if [ "$fails" -ne 0 ]; then
  echo "macos-verify_test: $fails check(s) failed" >&2
  exit 1
fi
echo "macos-verify_test: all checks passed"
