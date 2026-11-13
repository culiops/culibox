#!/bin/sh
# Build the release assets: one tarball per platform plus SHA256SUMS.
#
#   scripts/release/build.sh <tag> <outdir>
#
# <tag> is vX.Y.Z or vX.Y.Z-<prerelease>. <outdir> is created if missing and must
# hold no culibox_* file yet, so assets from an earlier run never mix into this one.
#
# Each tarball, culibox_<tag without v>_<os>_<arch>.tar.gz, holds culibox,
# LICENSE and README.md at its top level. The binary is stamped with the tag and
# with `git rev-parse HEAD`, so `culibox version` prints `culibox <tag> (<sha>)`.
#
# The tarballs are reproducible: the same commit and tag give byte-identical files
# (fixed mtime from the commit, sorted names, owner 0, no gzip timestamp), and so an
# identical SHA256SUMS. Needs GNU tar (--sort, --mtime): on Linux, not stock macOS.
set -eu

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <tag> <outdir>" >&2
  exit 2
fi
tag=$1
outdir=$2

if ! printf '%s\n' "$tag" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
  echo "build.sh: tag '$tag' is not vX.Y.Z or vX.Y.Z-<prerelease>" >&2
  exit 2
fi

cd "$(dirname "$0")/../.."
repo=$(pwd)

mkdir -p "$outdir"
outdir=$(cd "$outdir" && pwd)
# Captured first, so a find that fails cannot read as "nothing there".
stale=$(find "$outdir" -maxdepth 1 -name 'culibox_*' -print -quit) || exit 1
if [ -n "$stale" ] || [ -e "$outdir/SHA256SUMS" ]; then
  echo "build.sh: $outdir already holds culibox_* or SHA256SUMS files; use an empty directory" >&2
  exit 1
fi

sha=$(git rev-parse HEAD)
mtime=$(git log -1 --format=%ct)
version=${tag#v}

# Staged inside outdir, never in /tmp (small tmpfs in a box), and removed on exit.
stage=$(mktemp -d "$outdir/.stage.XXXXXX")
trap 'rm -rf "$stage"' EXIT INT TERM

for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  os=${target%/*}
  arch=${target#*/}
  name=culibox_${version}_${os}_${arch}
  dir=$stage/$name
  mkdir -p "$dir"
  echo "build $name"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
    -ldflags "-s -w -X main.version=$tag -X main.commit=$sha" \
    -o "$dir/culibox" ./cmd/culibox
  cp "$repo/LICENSE" "$repo/README.md" "$dir/"
  # Modes are recorded in the tar; the umask must not leak into it.
  chmod 755 "$dir/culibox"
  chmod 644 "$dir/LICENSE" "$dir/README.md"
  # POSIX sh has no pipefail, so `tar | gzip` would hide a failing tar: write the
  # .tar first (set -e stops on its exit status), then compress it.
  tar -C "$dir" --sort=name --mtime="@$mtime" --owner=0 --group=0 --numeric-owner \
    -cf "$stage/$name.tar" culibox LICENSE README.md
  gzip -n <"$stage/$name.tar" >"$outdir/$name.tar.gz"
  rm -f "$stage/$name.tar"
done

# Every tarball must read back and hold exactly the three members, before any sum
# is written: SHA256SUMS must never cover a broken file. (CI smoke-tests only
# linux/amd64.) The assignment fails the script if tar -tzf fails.
for f in "$outdir"/culibox_*.tar.gz; do
  # No pipe: a pipeline's status is its last command's, so `tar -tzf | sort` would
  # pass when tar prints the right names and then fails (a truncated archive).
  list=$(tar -tzf "$f") || {
    echo "build.sh: tar -tzf failed on $f" >&2
    exit 1
  }
  members=$(printf '%s\n' "$list" | LC_ALL=C sort | tr '\n' ' ')
  if [ "$members" != "LICENSE README.md culibox " ]; then
    echo "build.sh: $f holds '$members', want exactly culibox, LICENSE and README.md" >&2
    exit 1
  fi
done

(cd "$outdir" && sha256sum culibox_*.tar.gz >SHA256SUMS)
echo "wrote 4 tarballs and SHA256SUMS to $outdir"
