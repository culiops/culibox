package clone

import (
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// Paths that a protected config names inside the work tree (#370). A command
// line that a git config, an agent setting or an .envrc runs names files the
// host then executes or loads, so a box commit that changes them is as trusted
// as a change to the config itself. commandPaths is the shared tokenizer and
// repoDenyPaths turns what it finds into the repo-relative paths the merge
// refuses.

// projectDirSpellings are the ways a command names the project's top; each
// becomes "./" so what follows reads as a relative path.
var projectDirSpellings = []string{
	`"${CLAUDE_PROJECT_DIR}"/`, `"$CLAUDE_PROJECT_DIR"/`, `${CLAUDE_PROJECT_DIR}/`, `$CLAUDE_PROJECT_DIR/`,
}

// commandRunners are programs that read a manifest at the repo top (a script
// table, a build file, a lock-in config) and run what it says.
var commandRunners = map[string]bool{
	"npm": true, "npx": true, "pnpm": true, "pnpx": true, "yarn": true, "bun": true, "bunx": true,
	"node": true, "deno": true, "make": true, "just": true, "task": true, "uv": true, "uvx": true,
	"poetry": true, "pipenv": true, "cargo": true, "go": true, "rake": true, "bundle": true,
	"mise": true, "nix": true, "devbox": true, "devenv": true,
}

var scriptExts = []string{".js", ".mjs", ".cjs", ".ts", ".mts", ".py", ".sh", ".bash", ".zsh", ".rb", ".pl",
	".php", ".nix", ".toml", ".json", ".yaml", ".yml", ".lua"}

var envAssignRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// commandDelim reports the characters a command line splits on: white space and
// the shell's metacharacters and quotes.
func commandDelim(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune(";&|()<>`'\"", r)
}

// commandPaths reads a command line (or a config value) the way a shell would
// split it, without evaluating anything, and returns the tokens that look like
// file paths, as spelled (the caller resolves them: repoDenyPaths), and the
// runners (commandRunners) it names. A token is a path when it holds a "/",
// starts with ".", or ends in a script or config extension. Skipped: options
// (the value of --flag=value is read), URLs, "~" and "$VAR" spellings, ".", "..".
// It over-approximates on purpose: a wrong path only adds a refusal.
func commandPaths(s string) (paths []string, runners []string) {
	for _, sp := range projectDirSpellings {
		s = strings.ReplaceAll(s, sp, "./")
	}
	for _, tok := range strings.FieldsFunc(s, commandDelim) {
		tok = strings.TrimRight(strings.TrimLeft(tok, "!{"), "}") // git's "!" alias prefix, a shell function's braces
		if strings.HasPrefix(tok, "-") {
			i := strings.Index(tok, "=")
			if i < 0 {
				continue
			}
			tok = tok[i+1:]
		} else if loc := envAssignRE.FindStringIndex(tok); loc != nil {
			tok = tok[loc[1]:]
		}
		if tok == "" || tok == "." || tok == ".." || strings.HasPrefix(tok, "-") ||
			strings.HasPrefix(tok, "$") || strings.HasPrefix(tok, "~") || strings.Contains(tok, "://") {
			continue
		}
		if b := path.Base(tok); commandRunners[b] && !slices.Contains(runners, b) {
			runners = append(runners, b)
		}
		if strings.Contains(tok, "/") || strings.HasPrefix(tok, ".") || hasScriptExt(tok) {
			paths = append(paths, tok)
		}
	}
	return paths, runners
}

func hasScriptExt(tok string) bool {
	l := strings.ToLower(tok)
	for _, e := range scriptExts {
		if strings.HasSuffix(l, e) {
			return true
		}
	}
	return false
}

// repoDenyPaths maps path spellings from a command (relative ones to the repo
// top of, where the host runs them) to repo-relative, slash-separated paths:
// each path inside the repo and its folder when that is not the repo top (a
// script may load its neighbours). The top itself is never returned. A path
// that leaves the repo is skipped.
func repoDenyPaths(of string, raws []string) []string {
	var out []string
	add := func(p string) {
		if p != "" && p != "." && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	for _, raw := range raws {
		var rel string
		if filepath.IsAbs(raw) {
			rel = absInside(of, raw)
		} else {
			rel = path.Clean(filepath.ToSlash(raw))
			if rel == ".." || strings.HasPrefix(rel, "../") {
				rel = ""
			}
		}
		if rel == "" || rel == "." {
			continue
		}
		add(rel)
		add(path.Dir(rel))
	}
	return out
}
