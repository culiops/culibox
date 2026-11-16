#!/bin/sh
# Install a checksum-verified culibox into ${PREFIX:-$HOME/.local/bin}, then run
# `culibox init`. Everything else (config, VM, token, doctor) is init's job, in Go.
# A release tarball that carries abduco gets it installed next to culibox, where
# culibox looks first; with --from-source, abduco is built from its pinned source
# when none is installed and cc and make are there.
#
#   setup.sh [--version vX.Y.Z] [--from-source] [--prefix DIR]
#
# Without --version it installs the newest published release. With --from-source it
# builds that release from a fresh clone instead of downloading a binary; the binary is
# built in a temp dir, never in a source tree you keep. Nothing downloaded runs before
# its SHA-256 matched the release's SHA256SUMS, and nothing here uses sudo or writes
# outside the prefix. Read it before you run it; it is meant to be read.
set -eu

REPO=culiops/culibox
# first release with "culibox init"; the host confirms when tagging
MIN_VERSION="v0.35.0"

WORK=
say() { printf '==> %s\n' "$*"; }
note() { printf '    %s\n' "$*"; }
die() {
  printf 'setup.sh: %s\n' "$*" >&2
  exit 1
}
cleanup() {
  if [ -n "$WORK" ]; then rm -rf "$WORK"; fi
}
usage() {
  echo "usage: setup.sh [--version vX.Y.Z] [--from-source] [--prefix DIR]"
}

VERSION=
FROM_SOURCE=0
PREFIX=${PREFIX:-}
while [ $# -gt 0 ]; do
  case $1 in
    --version)
      [ $# -ge 2 ] || die "--version needs a value (vX.Y.Z)"
      VERSION=$2
      shift 2
      ;;
    --version=*)
      VERSION=${1#--version=}
      shift
      ;;
    --from-source)
      FROM_SOURCE=1
      shift
      ;;
    --prefix)
      [ $# -ge 2 ] || die "--prefix needs a directory"
      PREFIX=$2
      shift 2
      ;;
    --prefix=*)
      PREFIX=${1#--prefix=}
      shift
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      usage >&2
      die "unknown argument: $1"
      ;;
  esac
done
if [ -z "$PREFIX" ]; then
  [ -n "${HOME:-}" ] || die "HOME is not set; pass --prefix DIR"
  PREFIX=$HOME/.local/bin
fi
case $PREFIX in
  /*) ;;
  *) PREFIX=$(pwd)/$PREFIX ;;
esac
while [ "${PREFIX%/}" != "$PREFIX" ] && [ "$PREFIX" != / ]; do PREFIX=${PREFIX%/}; done
BIN=$PREFIX/culibox

is_version() {
  printf '%s\n' "$1" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'
}

# version_lt A B: A < B, comparing each dot-separated number as a number ("v" prefix
# optional, a missing part counts as 0), so v0.9.0 < v0.10.0.
version_lt() {
  vl_a=${1#v}
  vl_b=${2#v}
  vl_i=1
  while [ "$vl_i" -le 3 ]; do
    vl_x=$(printf '%s' "$vl_a" | cut -d. -f"$vl_i")
    vl_y=$(printf '%s' "$vl_b" | cut -d. -f"$vl_i")
    vl_x=${vl_x:-0}
    vl_y=${vl_y:-0}
    if [ "$vl_x" -lt "$vl_y" ]; then return 0; fi
    if [ "$vl_x" -gt "$vl_y" ]; then return 1; fi
    vl_i=$((vl_i + 1))
  done
  return 1
}

# gh counts only when it is logged in: the release files of a private repo need it, and
# an unauthenticated gh can do nothing curl cannot.
have_gh() {
  command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1
}

# no_release: GitHub answered, and there is no published release. The one failure of
# the latest-release lookup that is not an error to retry.
no_release() {
  die "No published CuliBox release yet. Use --from-source --version <a pushed tag >= $MIN_VERSION> to build one."
}

# lookup_failed WHY: the lookup itself failed (rate limit, no network, DNS, a GitHub
# error), which says nothing about whether a release exists.
lookup_failed() {
  die "could not look up the newest release: $1. Retry later, or pass --version vX.Y.Z (a published release) to skip the lookup."
}

# latest_tag: sets TAG to the newest published release, or dies saying what failed. A 404
# is "no release"; anything else (a 403 rate limit, no network, DNS) is not.
latest_tag() {
  TAG=
  if have_gh; then
    lt_rc=0
    lt_out=$(gh release view --repo "$REPO" --json tagName --jq .tagName 2>&1) || lt_rc=$?
    if [ "$lt_rc" -ne 0 ]; then
      # gh words its 404 "release not found"; any other message is a failure to show.
      case $lt_out in
        *"release not found"*) no_release ;;
      esac
      lookup_failed "gh said: $(printf '%s\n' "$lt_out" | head -n 1)"
    fi
    TAG=$(printf '%s\n' "$lt_out" | tail -n 1)
  else
    # No -f: an HTTP error must reach us as a status, not as curl's bare exit 22. The
    # status is the last line of the output.
    lt_rc=0
    lt_out=$(curl -sL -w '\n%{http_code}' "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null) || lt_rc=$?
    if [ "$lt_rc" -ne 0 ]; then
      case $lt_rc in
        127) lookup_failed "curl is not installed" ;;
        6) lookup_failed "could not resolve api.github.com (curl exit 6)" ;;
        7) lookup_failed "could not connect to api.github.com (curl exit 7)" ;;
        28) lookup_failed "api.github.com timed out (curl exit 28)" ;;
        *) lookup_failed "curl could not reach api.github.com (curl exit $lt_rc)" ;;
      esac
    fi
    lt_status=$(printf '%s\n' "$lt_out" | tail -n 1)
    case $lt_status in
      200) ;;
      404) no_release ;;
      403 | 429) lookup_failed "GitHub answered HTTP $lt_status, usually its API rate limit for your address" ;;
      *) lookup_failed "GitHub answered HTTP $lt_status" ;;
    esac
    TAG=$(printf '%s\n' "$lt_out" |
      sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)
  fi
  [ -n "$TAG" ] || lookup_failed "GitHub's reply had no tag_name"
}

# ---- 1. platform --------------------------------------------------------------------
say "checking the platform"
case $(uname -s) in
  Linux) OS=linux ;;
  Darwin) OS=darwin ;;
  *) die "unsupported operating system: $(uname -s) (CuliBox runs on Linux and macOS)" ;;
esac
case $(uname -m) in
  x86_64 | amd64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
  *) die "unsupported architecture: $(uname -m) (CuliBox runs on amd64 and arm64)" ;;
esac
note "$OS/$ARCH"

# ---- 2. version ---------------------------------------------------------------------
if [ -n "$VERSION" ]; then
  is_version "$VERSION" || die "--version must look like vX.Y.Z (got '$VERSION')"
  WHAT="the requested version ($VERSION)"
else
  say "finding the newest published release"
  latest_tag
  is_version "$TAG" || die "the newest release is named '$TAG', not vX.Y.Z; pass --version vX.Y.Z"
  VERSION=$TAG
  WHAT="the newest published release ($VERSION)"
  note "$VERSION"
fi
if version_lt "$VERSION" "$MIN_VERSION"; then
  die "$WHAT predates \`culibox init\` (first in $MIN_VERSION). Pick $MIN_VERSION or newer with --version, or wait for a release that has it."
fi

# ---- 3. already installed? ----------------------------------------------------------
# Before any download or build: the same version needs nothing but init.
INSTALLED=
if [ -x "$BIN" ] && [ ! -d "$BIN" ]; then
  INSTALLED=$("$BIN" version 2>/dev/null | sed -n 's/^culibox \([^ ]*\) .*/\1/p' | head -n 1) || INSTALLED=
fi

# ---- helpers for the binary and source paths ----------------------------------------
sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    die "neither sha256sum nor shasum is installed, so the download cannot be verified"
  fi
}

# fetch_asset NAME: a release file into $WORK, from the release of $VERSION.
fetch_asset() {
  if have_gh; then
    gh release download "$VERSION" --repo "$REPO" --pattern "$1" --dir "$WORK" >/dev/null 2>&1 ||
      die "could not download $1 from release $VERSION with gh"
  else
    curl -fsSL -o "$WORK/$1" "https://github.com/$REPO/releases/download/$VERSION/$1" 2>/dev/null ||
      die "could not download $1 from release $VERSION (no such file, or no network)"
  fi
}

verify_checksum() { # TARBALL NAME
  vc_lines=$(awk -v n="$2" '$2 == n || $2 == "*" n {print $1}' "$WORK/SHA256SUMS")
  if [ -z "$vc_lines" ]; then
    die "checksum failed: SHA256SUMS has no entry for $2; nothing was installed"
  fi
  if [ "$(printf '%s\n' "$vc_lines" | wc -l | tr -d ' ')" != 1 ]; then
    die "checksum failed: SHA256SUMS lists $2 more than once; nothing was installed"
  fi
  vc_got=$(sha256_of "$1")
  vc_want=$(printf '%s' "$vc_lines" | tr 'A-F' 'a-f')
  if [ "$vc_got" != "$vc_want" ]; then
    die "checksum failed for $2 (expected $vc_want, got $vc_got); nothing was installed"
  fi
  note "checksum ok"
}

# verify_provenance TARBALL: the build attestation, when gh can check it.
verify_provenance() {
  vp_why=
  if ! command -v gh >/dev/null 2>&1; then
    vp_why="gh is not installed"
  elif ! gh auth status >/dev/null 2>&1; then
    vp_why="gh is not logged in"
  elif ! gh attestation --help >/dev/null 2>&1; then
    vp_why="this gh has no attestation command"
  fi
  if [ -n "$vp_why" ]; then
    note "provenance not checked: $vp_why; only the checksum was checked"
    return 0
  fi
  vp_vis=$(gh repo view "$REPO" --json visibility --jq .visibility 2>/dev/null) || vp_vis=
  case $vp_vis in
    PUBLIC)
      gh attestation verify "$1" --repo "$REPO" >/dev/null 2>&1 ||
        die "provenance check failed: gh attestation verify rejected ${1##*/}; nothing was installed"
      note "provenance ok (build attestation verified)"
      ;;
    PRIVATE | INTERNAL)
      note "skipped: attestations start when the repo is public"
      ;;
    *)
      die "could not tell whether $REPO is public, so its attestation cannot be required; nothing was installed"
      ;;
  esac
}

# ---- 4a. binary path ----------------------------------------------------------------
get_binary() {
  say "downloading culibox $VERSION"
  gb_name=culibox_${VERSION#v}_${OS}_${ARCH}.tar.gz
  fetch_asset "$gb_name"
  fetch_asset SHA256SUMS
  say "verifying the download"
  verify_checksum "$WORK/$gb_name" "$gb_name"
  verify_provenance "$WORK/$gb_name"
  mkdir "$WORK/extract"
  tar -xzf "$WORK/$gb_name" -C "$WORK/extract" culibox ||
    die "$gb_name does not hold a top-level culibox file"
  if [ ! -f "$WORK/extract/culibox" ] || [ -L "$WORK/extract/culibox" ]; then
    die "$gb_name does not hold a regular file named culibox"
  fi
  NEW=$WORK/extract/culibox
  # Releases from #317 on carry a pinned abduco next to culibox; older ones do not.
  gb_list=$(tar -tzf "$WORK/$gb_name") || die "could not list $gb_name"
  if printf '%s\n' "$gb_list" | grep -qx abduco; then
    tar -xzf "$WORK/$gb_name" -C "$WORK/extract" abduco ||
      die "$gb_name lists abduco but it could not be extracted"
    if [ ! -f "$WORK/extract/abduco" ] || [ -L "$WORK/extract/abduco" ]; then
      die "$gb_name does not hold a regular file named abduco"
    fi
    NEW_ABDUCO=$WORK/extract/abduco
  elif ! command -v abduco >/dev/null 2>&1; then
    ABDUCO_NOTE="this release does not include abduco; culibox init checks for abduco"
  fi
}

# ---- 4b. from source ----------------------------------------------------------------
# ensure_go NEEDED: a go at least NEEDED is on PATH, or say how to get one and stop.
ensure_go() {
  eg_have=
  if command -v go >/dev/null 2>&1; then
    eg_have=$(go env GOVERSION 2>/dev/null | sed -n 's/^go\([0-9][0-9.]*\).*/\1/p' | head -n 1) || eg_have=
    if [ -z "$eg_have" ]; then
      eg_have=$(go version 2>/dev/null | sed -n 's/^go version go\([0-9][0-9.]*\).*/\1/p' | head -n 1) || eg_have=
    fi
  fi
  if [ -n "$eg_have" ] && ! version_lt "$eg_have" "$1"; then
    note "go $eg_have"
    return 0
  fi
  if [ -z "$eg_have" ]; then eg_msg="go is not installed"; else eg_msg="go $eg_have is older than the $1 this release needs"; fi
  if [ "$OS" != darwin ]; then
    die "$eg_msg. Install Go $1 or newer from https://go.dev/dl/ and run setup.sh again."
  fi
  if ! command -v brew >/dev/null 2>&1; then
    printf '%s\n' "setup.sh: $eg_msg, and Homebrew is not installed." >&2
    # Printed for the user to run; this script never runs it.
    printf '%s\n' "Install Homebrew from https://brew.sh :" >&2
    # shellcheck disable=SC2016  # literal text for the user, not an expansion
    printf '%s\n' '/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"' >&2
    printf '%s\n' "then run setup.sh again." >&2
    exit 1
  fi
  printf 'Install go via brew? [y/N] ' >&2
  eg_ans=
  { read -r eg_ans </dev/tty; } 2>/dev/null || eg_ans=
  case $eg_ans in
    y | Y | yes | YES | Yes) ;;
    *)
      printf '\n' >&2
      die "$eg_msg. Run: brew install go   then run setup.sh again."
      ;;
  esac
  say "installing go with brew"
  brew install go || die "brew install go failed"
  eg_have=$(go env GOVERSION 2>/dev/null | sed -n 's/^go\([0-9][0-9.]*\).*/\1/p' | head -n 1) || eg_have=
  if [ -z "$eg_have" ] || version_lt "$eg_have" "$1"; then
    die "brew installed go ${eg_have:-?}, but $1 or newer is needed. Update it (brew upgrade go) and run setup.sh again."
  fi
  note "go $eg_have"
}

build_from_source() {
  command -v git >/dev/null 2>&1 || die "git is not installed (needed for --from-source)"
  say "cloning $REPO $VERSION"
  if have_gh; then
    gh repo clone "$REPO" "$WORK/src" -- --quiet --depth 1 --branch "$VERSION" >/dev/null 2>&1 ||
      die "could not clone $REPO at $VERSION with gh (is that tag pushed?)"
  else
    git clone --quiet --depth 1 --branch "$VERSION" "https://github.com/$REPO.git" "$WORK/src" >/dev/null 2>&1 ||
      die "could not clone $REPO at $VERSION (is that tag pushed?)"
  fi
  bs_sha=$(git -C "$WORK/src" rev-parse --short HEAD) || die "could not read the commit of the clone"
  bs_need=$(awk '$1 == "go" {print $2; exit}' "$WORK/src/go.mod")
  [ -n "$bs_need" ] || die "the clone's go.mod has no go line"
  say "checking go (needs $bs_need or newer)"
  ensure_go "$bs_need"
  say "building culibox $VERSION ($bs_sha)"
  (
    cd "$WORK/src" || exit 1
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-X main.version=$VERSION -X main.commit=$bs_sha" \
      -o "$WORK/culibox" ./cmd/culibox
  ) || die "go build failed"
  NEW=$WORK/culibox
  # abduco: no prebuilt binary is downloaded here. When none is installed yet, the
  # clone's installer fetches the pinned abduco source, checks its sha256 before
  # unpacking, and builds it.
  if [ -e "$PREFIX/abduco" ] || command -v abduco >/dev/null 2>&1; then
    return 0
  fi
  if [ ! -f "$WORK/src/scripts/ci/install-abduco.sh" ]; then
    ABDUCO_NOTE="this release cannot build abduco; $(abduco_hint)"
  elif ! command -v cc >/dev/null 2>&1 || ! command -v make >/dev/null 2>&1; then
    ABDUCO_NOTE="building abduco needs cc and make; $(abduco_hint)"
  else
    say "building abduco (pinned version, checksum-verified source)"
    if sh "$WORK/src/scripts/ci/install-abduco.sh" "$WORK/abduco"; then
      NEW_ABDUCO=$WORK/abduco/bin/abduco
    else
      ABDUCO_NOTE="could not build abduco; $(abduco_hint)"
    fi
  fi
}

abduco_hint() {
  if [ "$OS" = darwin ]; then
    echo "install abduco with: brew install abduco (culibox init checks for it)"
  else
    echo "install abduco with your package manager, or build it from https://github.com/martanne/abduco where there is no package (culibox init checks for it)"
  fi
}

# ---- 5. install ---------------------------------------------------------------------
install_binary() {
  say "installing $BIN"
  mkdir -p "$PREFIX"
  if [ -e "$BIN" ]; then
    # The binary being replaced stays as the one-step rollback (the deploy-prod.sh convention).
    rm -f "$BIN.prev"
    cp -p "$BIN" "$BIN.prev"
    KEPT=1
  fi
  # A side file and a rename, never a write through whatever $BIN.new already is.
  rm -f "$BIN.new"
  cp "$NEW" "$BIN.new"
  chmod 755 "$BIN.new"
  mv -f "$BIN.new" "$BIN"
  note "installed $VERSION"
  if [ "$KEPT" = 1 ]; then
    note "kept the previous binary as $BIN.prev; to roll back, run:"
    note "rollback: mv -f $BIN.prev $BIN"
    note "to also undo data changes, restore the backup taken at upgrade: see docs/UPGRADING.md"
  fi
}

# install_abduco: put NEW_ABDUCO next to culibox, where culibox looks first.
install_abduco() {
  if [ -z "$NEW_ABDUCO" ]; then
    if [ -n "$ABDUCO_NOTE" ]; then note "$ABDUCO_NOTE"; fi
    return 0
  fi
  ia_dest=$PREFIX/abduco
  if [ -d "$ia_dest" ]; then
    note "$ia_dest is a directory, so the bundled abduco was not installed; $(abduco_hint)"
    return 0
  fi
  if [ -f "$ia_dest" ] && cmp -s "$NEW_ABDUCO" "$ia_dest"; then
    note "$ia_dest is already this abduco"
    return 0
  fi
  say "installing $ia_dest"
  if [ -e "$ia_dest" ]; then
    rm -f "$ia_dest.prev"
    cp -p "$ia_dest" "$ia_dest.prev"
    note "kept the previous abduco as $ia_dest.prev"
  fi
  rm -f "$ia_dest.new"
  cp "$NEW_ABDUCO" "$ia_dest.new"
  chmod 755 "$ia_dest.new"
  mv -f "$ia_dest.new" "$ia_dest"
}

KEPT=0
NEW_ABDUCO=
ABDUCO_NOTE=
if [ "$INSTALLED" = "$VERSION" ]; then
  say "culibox $VERSION is already installed at $BIN"
else
  WORK=$(mktemp -d "${TMPDIR:-/tmp}/culibox-setup.XXXXXX") || die "could not create a temp directory"
  trap cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  if [ "$FROM_SOURCE" = 1 ]; then build_from_source; else get_binary; fi
  install_binary
  install_abduco
  cleanup
  WORK=
fi

# Where the shell will find it.
found=$(command -v culibox 2>/dev/null || true)
case ":$PATH:" in
  *":$PREFIX:"*) ;;
  *)
    note "$PREFIX is not on your PATH. Add this to your shell's rc file:"
    note "export PATH=\"$PREFIX:\$PATH\""
    ;;
esac
if [ -n "$found" ] && [ "$found" != "$BIN" ]; then
  note "note: 'culibox' on your PATH currently resolves to $found, not $BIN"
fi

# ---- 6. init ------------------------------------------------------------------------
say "running culibox init"
exec "$BIN" init
