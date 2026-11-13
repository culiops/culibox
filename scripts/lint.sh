#!/usr/bin/env sh
# Check the browser scripts for references that resolve to nothing at runtime.
#
# internal/web/static/*.js are classic <script> tags: they share ONE global
# scope and call each other's functions with no import, so nothing verifies that
# graph. A helper deleted from one file leaves working-looking call sites in the
# others, and `node --test` cannot see it — the modules it can require are only
# part of the page.
#
# So model the browser exactly: concatenate the scripts in the order index.html
# loads them, which IS that shared scope, and let eslint's no-undef read the
# result. One rule, no style opinions — this is a linker check, not a linter.
#
# eslint is installed with `npm ci --ignore-scripts` from scripts/lint/package.json and
# its lockfile: an exact version, every package checked against the lockfile's
# integrity hash, no install script run. (npx eslint@<range> would resolve the newest
# match and its dependencies fresh on each run, and CI runs this with the network.)
# The install needs the network, and this must not be the thing that stops you
# working on a train, so an unavailable eslint SKIPS loudly rather than failing.
set -e
cd "$(dirname "$0")/.."

STATIC=internal/web/static
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

# The load order is index.html's, not the directory's: a file that redefines a
# name must be linted in the position where it actually wins.
FILES=$(sed -n 's|.*<script src="/\([a-z-]*\.js\)".*|\1|p' "$STATIC/index.html")
if [ -z "$FILES" ]; then
  echo "lint: found no page scripts in $STATIC/index.html — has the markup changed?" >&2
  exit 1
fi

# Concatenate, recording where each file starts so an error can be reported
# against the file a human edits rather than a line in a temporary bundle.
: > "$WORK/bundle.js"
: > "$WORK/offsets.txt"
for f in $FILES; do
  echo "$f $(wc -l < "$WORK/bundle.js")" >> "$WORK/offsets.txt"
  cat "$STATIC/$f" >> "$WORK/bundle.js"
  echo >> "$WORK/bundle.js"
done

# Globals the page really has: the browser, plus the vendored libraries loaded
# from their own <script> tags, plus the CommonJS names the module.exports
# guards at the foot of each file test for.
cat > "$WORK/eslint.config.mjs" <<'EOF'
const names = [
  "window", "document", "location", "navigator", "console", "history", "screen",
  "localStorage", "sessionStorage", "matchMedia", "getComputedStyle",
  "setTimeout", "clearTimeout", "setInterval", "clearInterval",
  "requestAnimationFrame", "cancelAnimationFrame", "queueMicrotask",
  "fetch", "WebSocket", "XMLHttpRequest", "FormData", "Headers", "Request", "Response",
  "Blob", "File", "FileReader", "URL", "URLSearchParams", "AbortController", "AbortSignal",
  "TextEncoder", "TextDecoder", "atob", "btoa", "structuredClone", "performance",
  "Event", "CustomEvent", "KeyboardEvent", "MouseEvent", "WheelEvent", "DragEvent", "DataTransfer",
  "HTMLElement", "Element", "Node", "NodeList", "Image", "DOMParser",
  "ResizeObserver", "MutationObserver", "IntersectionObserver", "Notification",
  // alert/confirm/prompt are deliberately absent: the page uses its own dialogs
  // (shell.js askConfirm/askPrompt), so a bare call to one is now a no-undef
  // error here as well as a failure in no-native-dialogs.test.js.
  "getSelection", "CSS", "Highlight", "NodeFilter",
  // vendored, loaded from their own <script> tags
  "Terminal", "FitAddon", "WebglAddon", "marked", "hljs", "DOMPurify", "CodeMirror",
  // the CommonJS guard each file ends with, so node --test can require it
  "module", "exports", "require",
];
const globals = {};
for (const n of names) globals[n] = "readonly";
export default [{
  files: ["bundle.js"],
  languageOptions: { ecmaVersion: 2022, sourceType: "script", globals },
  // no-undef only. Style is not this check's business.
  rules: { "no-undef": "error" },
}];
EOF

if ! command -v npm >/dev/null 2>&1; then
  echo "lint: SKIPPED — npm not on PATH, so the page scripts were not checked" >&2
  exit 0
fi

mkdir "$WORK/npm"
cp scripts/lint/package.json scripts/lint/package-lock.json "$WORK/npm/"
set +e
(cd "$WORK/npm" && npm ci --ignore-scripts --no-audit --no-fund) >"$WORK/npm.log" 2>&1
install_rc=$?
if [ "$install_rc" -eq 0 ]; then
  # Run from the bundle's own directory: eslint refuses to lint a file outside the
  # base path it derives from the working directory.
  (cd "$WORK" && "$WORK/npm/node_modules/.bin/eslint" --no-config-lookup -c eslint.config.mjs \
    -f json -o report.json bundle.js) >>"$WORK/npm.log" 2>&1
fi
set -e
OUT="$WORK/report.json"

# eslint exits 1 for lint errors and 2 for its own failures; only the latter,
# or a missing report, means the check could not run.
if [ ! -s "$OUT" ]; then
  echo "lint: SKIPPED — could not run eslint (offline?), page scripts unchecked" >&2
  sed 's/^/lint:   /' "$WORK/npm.log" >&2
  exit 0
fi

python3 - "$OUT" "$WORK/offsets.txt" <<'PY'
import json, sys, bisect

report, offsets = sys.argv[1], sys.argv[2]
starts, names = [], []
for line in open(offsets):
    name, off = line.rsplit(" ", 1)
    names.append(name)
    starts.append(int(off))

msgs = [m for f in json.load(open(report)) for m in f.get("messages", [])]
if not msgs:
    print("lint: page scripts OK — every reference resolves")
    sys.exit(0)

for m in msgs:
    line = m.get("line", 0)
    i = max(0, bisect.bisect_right(starts, line - 1) - 1)
    print("%s:%d:%d  %s  (%s)" % (
        names[i], line - starts[i], m.get("column", 0),
        m.get("message", ""), m.get("ruleId") or "fatal"))

n = len(msgs)
print("\nlint: %d reference%s to nothing at runtime — each is a ReferenceError"
      % (n, " resolves" if n == 1 else "s resolve"))
print("the browser will throw and no test can see.")
sys.exit(1)
PY
