#!/usr/bin/env sh
# Install the pinned Grype image scanner: scripts/ci/install-grype.sh <prefix>
# leaves <prefix>/bin/grype. box-image.yml and box-image-scan.yml run it in jobs
# that hold a read-only token only.
#
# Downloaded and checked here instead of through a scanner's GitHub Action: on
# 2026-03-19 the tags of aquasecurity/trivy-action and the Trivy v0.69.4 binary
# were replaced with a credential stealer (CVE-2026-33634). A version pin plus a
# sha256 checked BEFORE unpacking runs no third-party CI code.
#
# Grype 0.121.2 (released 2026-11-05, at least two weeks old when pinned). The
# sum is the GitHub asset digest of grype_0.121.2_linux_amd64.tar.gz:
#   gh api repos/anchore/grype/releases/tags/v0.121.2 --jq '.assets[]|.name+" "+.digest'
# To bump: take a release at least two weeks old, compare the asset digest with
# the release's grype_<v>_checksums.txt, and update both lines below.
#
# linux/amd64 only: the scan jobs run on ubuntu-24.04 and scan either arch by
# --platform, so no other sum is pinned.
set -eu

GRYPE_VERSION=0.121.2
GRYPE_SHA256=9c8965ced9df657e5e851c14021bae838802f711edd7a48f9bc3c923a8d28f12

if [ "$#" -ne 1 ] || [ -z "$1" ]; then
  echo "usage: $0 <prefix>" >&2
  exit 2
fi
prefix=$1

uname=${INSTALL_GRYPE_UNAME:-uname}
platform="$("$uname" -s)/$("$uname" -m)"
if [ "$platform" != Linux/x86_64 ]; then
  echo "install-grype: linux/amd64 only, this is $platform" >&2
  exit 1
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
tarball="$work/grype.tar.gz"

"${INSTALL_GRYPE_CURL:-curl}" -fsSL "https://github.com/anchore/grype/releases/download/v${GRYPE_VERSION}/grype_${GRYPE_VERSION}_linux_amd64.tar.gz" -o "$tarball"
got=$(sha256sum "$tarball" | cut -d ' ' -f 1)
if [ "$got" != "$GRYPE_SHA256" ]; then
  echo "install-grype: sha256 mismatch: got $got, want $GRYPE_SHA256" >&2
  exit 1
fi

tar -C "$work" -xzf "$tarball" grype
mkdir -p "$prefix/bin"
install -m 0755 "$work/grype" "$prefix/bin/grype"
GRYPE_CHECK_FOR_APP_UPDATE=false "$prefix/bin/grype" version | grep -qF "$GRYPE_VERSION"
echo "install-grype: installed $prefix/bin/grype (grype $GRYPE_VERSION)"
