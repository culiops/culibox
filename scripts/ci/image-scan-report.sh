#!/usr/bin/env sh
# Turn Grype JSON reports into a markdown summary:
#   scripts/ci/image-scan-report.sh [-a accepted.txt] [-d YYYY-MM-DD] <label>=<grype.json>...
#
# One section per label: findings per severity (and how many have a fix), then
# the HIGH and CRITICAL findings that have a fix and are not accepted. The last
# line is `<!-- fixable: N -->`, N summed over the labels, for the weekly job.
#
# It REPORTS: findings never make it fail. It exits 1 on a report it cannot read
# or parse, or on a malformed accepted file, and 2 on bad usage.
#
# Accepted file: `<id> <YYYY-MM-DD> <reason>` per line, `#` starts a comment.
# The id is a CVE or GHSA id; the date is when the finding was accepted, and the
# acceptance lapses 180 days after it (expired entries count again, named on
# stderr). A date after today is refused: it would stretch the 180 days.
#
# Every value taken from a report is reduced to [A-Za-z0-9._:+~/-] (anything else
# becomes ?) before it reaches markdown: package names come from the image.
set -eu

usage() {
  echo "usage: $0 [-a accepted.txt] [-d YYYY-MM-DD] <label>=<grype.json>..." >&2
  exit 2
}

# day YYYY-MM-DD: days since the epoch; fails on anything that is not a real date.
day() {
  printf '%s' "$1" | grep -Eq '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' || return 1
  s=$(date -u -d "$1" +%s 2>/dev/null) || return 1
  [ "$(date -u -d "@$s" +%F)" = "$1" ] || return 1
  echo $((s / 86400))
}

# load_accepted FILE: sets ids to a JSON array of the ids still accepted today.
# A function, so the `set --` below splits a line without touching the reports.
load_accepted() {
  [ -r "$1" ] || {
    echo "image-scan-report: cannot read $1" >&2
    exit 1
  }
  file=$1 n=0
  while IFS= read -r line || [ -n "$line" ]; do
    n=$((n + 1))
    line=${line%%#*}
    # shellcheck disable=SC2086 # split the line into its fields on purpose
    set -- $line
    [ "$#" -gt 0 ] || continue
    if [ "$#" -lt 3 ] ||
      ! printf '%s' "$1" | grep -Eq '^(CVE-[0-9]{4}-[0-9]{4,}|GHSA(-[23456789cfghjmpqrvwx]{4}){3})$'; then
      echo "image-scan-report: $file:$n: want '<CVE or GHSA id> <YYYY-MM-DD> <reason>'" >&2
      exit 1
    fi
    d=$(day "$2") || {
      echo "image-scan-report: $file:$n: $2 is not a date" >&2
      exit 1
    }
    if [ "$d" -gt "$now" ]; then
      echo "image-scan-report: $file:$n: $2 is after today ($today)" >&2
      exit 1
    fi
    if [ $((now - d)) -gt 180 ]; then
      echo "image-scan-report: $1 was accepted on $2, more than 180 days ago: expired, counted again" >&2
      continue
    fi
    ids=$(printf '%s' "$ids" | jq -c --arg id "$1" '. + [$id]')
  done <"$file"
}

accepted="" today=""
while getopts a:d: opt; do
  case $opt in
  a) accepted=$OPTARG ;;
  d) today=$OPTARG ;;
  *) usage ;;
  esac
done
shift $((OPTIND - 1))
[ "$#" -gt 0 ] || usage
for arg in "$@"; do
  case $arg in
  ?*=?*) ;;
  *) usage ;;
  esac
done

[ -n "$today" ] || today=$(date -u +%F)
now=$(day "$today") || {
  echo "image-scan-report: -d $today is not a date" >&2
  exit 2
}
ids="[]"
[ -z "$accepted" ] || load_accepted "$accepted"

# Findings, one per (id, package, version): Grype lists a finding once per
# location of the package.
# shellcheck disable=SC2016 # jq program, not shell
findings='def s: tostring | gsub("[^A-Za-z0-9._:+~/-]"; "?");
  [(.matches // [])[] | {
    id: (.vulnerability.id | s),
    sev: (.vulnerability.severity // "Unknown" | s),
    fixed: (.vulnerability.fix.state == "fixed"),
    fix: ([(.vulnerability.fix.versions // [])[] | s] | join(", ")),
    name: (.artifact.name | s), ver: (.artifact.version | s), type: (.artifact.type | s)
  }] | unique_by([.id, .name, .ver])
  | map(.listed = ((.sev == "Critical" or .sev == "High") and .fixed))
  | map(.accepted = (.listed and (.id as $id | $acc | index($id) != null)))'

total=0
for arg in "$@"; do
  # The path is after the LAST =: a label may hold one, a runner path does not.
  label=$(printf '%s' "${arg%=*}" | tr -c 'A-Za-z0-9._:+~/ -' '?')
  report=${arg##*=}
  [ -r "$report" ] || {
    echo "image-scan-report: cannot read $report" >&2
    exit 1
  }
  if ! out=$(jq -r --argjson acc "$ids" --arg lbl "$label" "$findings"' as $f
    | ($f | map(select(.listed and (.accepted | not)))) as $open
    | ($f | map(select(.accepted))) as $taken
    | "### \($lbl)\n",
      if ($f | length) == 0 then "No findings.\n" else
        "| Severity | Findings | With a fix |", "|---|---|---|",
        (["Critical", "High", "Medium", "Low", "Negligible", "Unknown"][] as $sev
          | ($f | map(select(.sev == $sev))) as $g
          | select($g | length > 0)
          | "| \($sev) | \($g | length) | \($g | map(select(.fixed)) | length) |"),
        "",
        "HIGH or CRITICAL with a fix, not accepted: **\($open | length)**\n",
        if ($open | length) > 0 then
          "| Vulnerability | Package | Installed | Fixed in | Type |", "|---|---|---|---|---|",
          ($open | sort_by(.sev, .id)[:300][] | "| \(.id) | \(.name) | \(.ver) | \(.fix) | \(.type) |"),
          (if ($open | length) > 300 then "\n…and \(($open | length) - 300) more in the JSON artifact." else empty end),
          ""
        else empty end,
        if ($taken | length) > 0 then
          "Accepted (not counted): \($taken | map("\(.id) (\(.name))") | join(", "))\n"
        else empty end
      end,
      "<!-- open: \($open | length) -->"' "$report"); then
    echo "image-scan-report: $report is not a Grype JSON report" >&2
    exit 1
  fi
  n=$(printf '%s\n' "$out" | sed -n 's/^<!-- open: \([0-9]*\) -->$/\1/p')
  total=$((total + n))
  printf '%s\n' "$out" | grep -v '^<!-- open: '
done
echo "<!-- fixable: $total -->"
