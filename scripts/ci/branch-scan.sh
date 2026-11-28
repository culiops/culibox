#!/usr/bin/env bash
# Scans the commits this checkout has and its base lacks (default origin/main) with
# .gitleaks.toml, the way the host's pre-push gate and CI's secrets job will scan them
# when they are pushed. scripts/test.sh runs it, so a box finds a leak before its
# branch comes home. A box clone's origin is the host repo, so origin/main is the
# host's main as of the clone's last refresh. Needs gitleaks on PATH (test.sh puts
# the pinned one there, see gitleaks-install.sh).
set -euo pipefail
cd "$(dirname "$0")/../.."

base=${AB_SCAN_BASE:-origin/main}
case $base in
-*)
  echo "branch-scan: refusing base '$base'" >&2
  exit 1
  ;;
esac
if ! git rev-parse -q --verify "$base^{commit}" >/dev/null; then
  echo "branch-scan: SKIPPED ($base is not in this clone)" >&2
  exit 0
fi
n=$(git rev-list --count "$base..HEAD")
if [ "$n" = 0 ]; then
  echo "branch-scan: nothing to scan (no commit in $base..HEAD)"
  exit 0
fi
if ! command -v gitleaks >/dev/null 2>&1; then
  echo "branch-scan: gitleaks is not on PATH (run it through scripts/test.sh)" >&2
  exit 1
fi
echo "branch-scan: scanning $n commit(s) in $base..HEAD with .gitleaks.toml"
if ! gitleaks git --config .gitleaks.toml --redact --no-banner --log-level warn --log-opts="$base..HEAD -m" .; then
  echo "branch-scan: a commit in $base..HEAD matches .gitleaks.toml (see above). Rewrite that commit: a later fix commit does not help, because every commit is scanned when it is pushed." >&2
  exit 1
fi
