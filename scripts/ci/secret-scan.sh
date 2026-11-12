#!/usr/bin/env bash
# The CI secret scan (ci.yml, job secrets). Needs `gitleaks` on PATH and a full clone
# (fetch-depth: 0). Reads GITHUB_EVENT_NAME, GITHUB_REF, GITHUB_SHA and BEFORE (the
# push event's before-sha, passed through env, never interpolated into a script).
#
# Scanning all history would fail forever on the history the founder chose to accept
# (and a baseline file would publish the matched values), so a push to main scans
# only the commits it adds:
#   - BEFORE is in the clone:   the commits BEFORE..sha
#   - BEFORE is not in the clone (a force-push replaced history): the new tip's
#     commits since its newest tag, a superset of what was pushed, AND the checkout.
#     With no tag to anchor the range, the checkout alone, with a warning.
#   - anything else (a manual run, a call from a tag push, an all-zeros or missing
#     BEFORE, another branch): the checkout.
# BEFORE is used only when it is exactly 40 hex digits: it comes from the event, and
# git must never see it as an option or a range.
set -eu

zeros=0000000000000000000000000000000000000000
cfg=(--config .gitleaks.toml --redact)
before=${BEFORE:-}
# [[ =~ ]] matches the whole value: a grep on a pipe checks each line, so a value whose
# first line is a commit id and which goes on would pass.
sha_re='^[0-9a-f]{40}$'

scan_tree() {
  echo "scanning the checkout"
  gitleaks dir "${cfg[@]}" .
}

if [ "${GITHUB_EVENT_NAME:-}" = push ] && [ "${GITHUB_REF:-}" = refs/heads/main ] &&
  [[ $before =~ $sha_re ]] && [ "$before" != "$zeros" ]; then
  if git cat-file -e "${before}^{commit}" 2>/dev/null; then
    echo "scanning the commits $before..$GITHUB_SHA"
    gitleaks git "${cfg[@]}" --log-opts="$before..$GITHUB_SHA" .
    exit 0
  fi
  echo "the push's BEFORE commit $before is not in this clone (a force-push)"
  if base=$(git describe --tags --abbrev=0 "$GITHUB_SHA" 2>/dev/null) && [ -n "$base" ]; then
    echo "scanning the commits $base..$GITHUB_SHA, then the checkout"
    gitleaks git "${cfg[@]}" --log-opts="$base..$GITHUB_SHA" .
  else
    echo "::warning::force-push and no tag to anchor a range: only the final tree is scanned"
  fi
  scan_tree
  exit 0
fi
scan_tree
