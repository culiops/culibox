#!/usr/bin/env bash
# Scans what this checkout would bring home with .gitleaks.toml, the way the host's
# pre-push gate and CI's secrets job will scan it when it is pushed:
#   1. the commits this checkout has and its base lacks (default origin/main; a box
#      clone's origin is the host repo, so that is the host's main as of the last refresh);
#   2. the files not committed yet (modified, staged, untracked and not ignored), so a
#      fixture written before test.sh runs is caught before it is committed.
# scripts/test.sh runs it with the pinned gitleaks on PATH (see gitleaks-install.sh).
#
# This helps an honest agent find a leak early. It is not a security boundary: an agent
# in a box controls this script, its environment and the scanner it runs. The host's
# pre-push gate and CI's secrets job remain the gate.
set -euo pipefail
cd "$(dirname "$0")/../.."
root=$PWD
cfg=$root/.gitleaks.toml
flags=(--config "$cfg" --redact --no-banner --log-level warn)

need_gitleaks() {
  if ! command -v gitleaks >/dev/null 2>&1; then
    echo "branch-scan: gitleaks is not on PATH (run it through scripts/test.sh)" >&2
    exit 1
  fi
}

# A checkout git cannot read (no repo, or one it refuses to use) is a failure: without
# this the steps below would read as "nothing to scan".
if ! git rev-parse --git-dir >/dev/null; then
  echo "branch-scan: git cannot read this checkout" >&2
  exit 1
fi
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

rc=0
base_ref=${AB_SCAN_BASE:-origin/main}
case $base_ref in
-*)
  echo "branch-scan: refusing base '$base_ref'" >&2
  exit 1
  ;;
esac

# 1. Commits. The base goes to gitleaks as a commit id: gitleaks splits --log-opts on
# spaces, and a ref spelled with one must not reach git log as an option.
if base=$(git rev-parse -q --verify "$base_ref^{commit}"); then
  n=$(git rev-list --count "$base..HEAD")
  if [ "$n" = 0 ]; then
    echo "branch-scan: no commit in $base_ref..HEAD"
  else
    need_gitleaks
    echo "branch-scan: scanning $n commit(s) in $base_ref..HEAD with .gitleaks.toml"
    if ! gitleaks git "${flags[@]}" --log-opts="$base..HEAD -m" .; then
      echo "branch-scan: a commit in $base_ref..HEAD matches .gitleaks.toml (see above). Rewrite that commit: a later fix commit does not help, because every commit is scanned when it is pushed." >&2
      rc=1
    fi
  fi
else
  echo "branch-scan: commits SKIPPED ($base_ref is not in this clone)" >&2
fi

# 2. Uncommitted files (the working copy), copied by their paths into a temp dir and
# scanned there, so the path rules of .gitleaks.toml see the same repo-relative paths.
# The list is built in plain commands, not a process substitution, so a git failure
# stops the script (set -e) instead of reading as an empty list.
git ls-files -z -m -o --exclude-standard >"$tmp/list"
git diff -z --cached --name-only --diff-filter=d >>"$tmp/list"
sort -zu "$tmp/list" >"$tmp/files"
n=0
while IFS= read -r -d '' f; do
  [ -f "$f" ] && [ ! -L "$f" ] || continue
  mkdir -p -- "$tmp/tree/$(dirname -- "$f")"
  cp -- "$f" "$tmp/tree/$f"
  n=$((n + 1))
done <"$tmp/files"
if [ "$n" -gt 0 ]; then
  need_gitleaks
  echo "branch-scan: scanning $n uncommitted file(s) with .gitleaks.toml"
  if ! (cd "$tmp/tree" && gitleaks dir "${flags[@]}" .); then
    echo "branch-scan: an uncommitted file matches .gitleaks.toml (see above): fix it before you commit." >&2
    rc=1
  fi
fi
exit "$rc"
