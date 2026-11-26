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
// as a change to the config itself. parseCommand is the shared tokenizer and
// repoDenyPaths turns what it finds into the repo-relative paths the merge
// refuses.

// projectDirSpellings are the ways a command names the project's top; each
// becomes "./" so what follows reads as a relative path.
var projectDirSpellings = []string{
	`"${CLAUDE_PROJECT_DIR}"/`, `"$CLAUDE_PROJECT_DIR"/`, `${CLAUDE_PROJECT_DIR}/`, `$CLAUDE_PROJECT_DIR/`,
	// direnv runs in the repo top, mise names it config_root or the project root.
	`"${PWD}"/`, `"$PWD"/`, `${PWD}/`, `$PWD/`, `"$(pwd)"/`, `$(pwd)/`,
	`{{ config_root }}/`, `{{config_root}}/`, `{{ cwd }}/`, `{{cwd}}/`,
	`"${MISE_PROJECT_ROOT}"/`, `${MISE_PROJECT_ROOT}/`, `$MISE_PROJECT_ROOT/`,
	// VS Code tasks name the folder that is open.
	`"${workspaceFolder}"/`, `${workspaceFolder}/`, `${workspaceRoot}/`,
	`"${MISE_CONFIG_ROOT}"/`, `${MISE_CONFIG_ROOT}/`, `$MISE_CONFIG_ROOT/`,
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

// segmentDelim reports the characters that end one simple command and start the
// next.
func segmentDelim(r rune) bool { return strings.ContainsRune(";&|()`\n", r) }

var (
	// A command that runs Python (or a tool that starts it) reads "-m module".
	pythonishRE = regexp.MustCompile(`^(python[0-9.]*|pypy[0-9.]*|py|uv|uvx|pipx|poetry|pdm|hatch|rye)$`)
	// A command that runs node reads "--import / -r / --require module".
	nodeishRE = regexp.MustCompile(`^(node|nodejs|bun|deno|tsx|ts-node|npx)$`)
	moduleRE  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)
	// A plain word: no "/", no extension (a "." in it makes it a name, not a word).
	plainWordRE = regexp.MustCompile(`^[A-Za-z0-9_@][A-Za-z0-9_@+-]{0,63}$`)
)

// dirFlags take a folder the command runs in or reads its project from.
var dirFlags = map[string]bool{"--directory": true, "--cwd": true, "--prefix": true, "--dir": true,
	"--project": true, "-C": true}

// nodeModuleFlags take a module node loads before the program.
var nodeModuleFlags = map[string]bool{"--import": true, "-r": true, "--require": true}

var nodeModuleExts = []string{".js", ".mjs", ".cjs", ".ts", ".mts"}

const maxCommandCwds = 16

// plainWord is a word of a command that may name an entry at the repo top (or
// in a folder the command moved to): the caller looks it up.
type plainWord struct {
	word  string
	bases []string // repo-relative folders to look in, "." for the repo top
}

// cmdInfo is what parseCommand finds in a command line.
type cmdInfo struct {
	paths   []string    // spellings that are paths: each is denied with its folder
	dirs    []string    // repo-relative folders named by a flag or cd: denied as themselves
	runners []string    // commandRunners the command calls
	plain   []plainWord // words that name an entry only if one exists
	over    bool        // a cap stopped the parse: mergeRefPaths or maxCommandCwds
}

// commandPaths is parseCommand's paths and runners, for callers with no work
// tree to look plain words up in.
func commandPaths(s string) (paths []string, runners []string) {
	ci := parseCommand("", s)
	return ci.paths, ci.runners
}

// parseCommand reads a command line (or a config value) the way a shell would
// split it, without evaluating anything. A token is a path when it holds a "/",
// starts with ".", or ends in a script or config extension; it is returned as
// spelled (the caller resolves it: repoDenyPaths). Also read:
//   - "python -m a.b" (also "-ma.b"), "uv run -m a.b" and "node --import/-r/--require
//     a": the module's first part is a path ("a", "a.py", "a.js" ...), found or not;
//   - the value of --directory, --cwd, --prefix, --dir, --project and -C, and the
//     argument of cd and pushd, is a folder (dirs); every path or plain word that
//     follows is also resolved against it (of makes an absolute folder repo-relative);
//   - a plain word (no "/", no extension) is kept for a look-up at the repo top
//     and in those folders.
//
// Skipped: other options (the value of --flag=value is read), URLs, "~" and
// "$VAR" spellings, ".", "..". It over-approximates on purpose: a wrong path only
// adds a refusal. The repo top itself is never returned as a folder.
func parseCommand(of, s string) cmdInfo {
	for _, sp := range projectDirSpellings {
		s = strings.ReplaceAll(s, sp, "./")
	}
	var (
		ci               cmdInfo
		cwds             []string // repo-relative folders the command may be in, besides the top
		pyish, nodeish   bool
		plainSeen        = map[string]bool{}
		emit, emitModule func(string)
	)
	emit = func(p string) {
		if len(ci.paths) >= mergeRefPaths { // the sink keeps no more than that anyway
			ci.over = true
			return
		}
		ci.paths = append(ci.paths, p)
		if !filepath.IsAbs(p) {
			for _, c := range cwds {
				ci.paths = append(ci.paths, path.Join(c, p))
			}
		}
	}
	emitModule = func(mod string) {
		if !moduleRE.MatchString(mod) {
			return
		}
		first, _, _ := strings.Cut(mod, ".")
		emit(first)
		if pyish {
			emit(first + ".py")
		}
		if nodeish {
			for _, e := range nodeModuleExts {
				emit(first + e)
			}
		}
	}
	plain := func(w string) {
		bases := append([]string{"."}, cwds...)
		if k := w + "\x00" + strings.Join(bases, "\x00"); !plainSeen[k] {
			if len(ci.plain) >= mergeRefPaths {
				ci.over = true
				return
			}
			plainSeen[k] = true
			ci.plain = append(ci.plain, plainWord{word: w, bases: bases})
		}
	}
	addDir := func(raw string) {
		c := cleanRaw(raw)
		if c == "" {
			return
		}
		rel := repoRel(of, c)
		if of == "" && filepath.IsAbs(c) {
			rel = ""
		}
		if rel == "" || rel == "." {
			return
		}
		news := []string{rel}
		for _, e := range cwds {
			news = append(news, path.Join(e, rel))
		}
		for _, n := range news {
			if slices.Contains(cwds, n) {
				continue
			}
			if len(cwds) >= maxCommandCwds {
				ci.over = true
				continue
			}
			cwds = append(cwds, n)
			ci.dirs = append(ci.dirs, n)
		}
	}
	for _, seg := range strings.FieldsFunc(s, segmentDelim) {
		toks := strings.FieldsFunc(seg, commandDelim)
		for i := 0; i < len(toks); i++ {
			tok := strings.TrimRight(strings.TrimLeft(toks[i], "!{"), "}") // git's "!" alias prefix, a shell function's braces
			if tok == "" {
				continue
			}
			cands := []string{tok}
			direct := false // a word of its own, not a value read out of an option or assignment
			switch {
			case strings.HasPrefix(tok, "-"):
				name, val, hasVal := strings.Cut(tok, "=")
				switch {
				case dirFlags[name]:
					if !hasVal && i+1 < len(toks) {
						i++
						val = toks[i]
					}
					addDir(val)
					continue
				case name == "-m" && pyish && !hasVal && i+1 < len(toks):
					i++
					emitModule(toks[i])
					continue
				case pyish && !hasVal && len(name) > 2 && strings.HasPrefix(name, "-m"):
					emitModule(name[2:]) // "-mmodule", no space
					continue
				case nodeModuleFlags[name] && nodeish:
					if !hasVal && i+1 < len(toks) {
						i++
						val = toks[i]
					}
					if strings.ContainsAny(val, "/") || strings.HasPrefix(val, ".") || hasScriptExt(val) {
						if v := cleanRaw(val); v != "" {
							emit(v)
						}
					} else if val != "" {
						emitModule(val)
						plain(val)
					}
					continue
				}
				if !hasVal {
					continue
				}
				cands = []string{val}
			case envAssignRE.MatchString(tok):
				loc := envAssignRE.FindStringIndex(tok)
				if strings.HasPrefix(tok, "NODE_OPTIONS=") {
					nodeish = true
				}
				cands = strings.Split(tok[loc[1]:], ":") // PATH=./bin:$PATH names ./bin
			case tok == "cd" || tok == "pushd":
				j := i + 1
				for j < len(toks) && strings.HasPrefix(toks[j], "-") {
					j++
				}
				if j < len(toks) {
					addDir(toks[j])
					i = j
				}
				continue
			default:
				direct = true
				if b := path.Base(tok); pythonishRE.MatchString(b) {
					pyish = true
				} else if nodeishRE.MatchString(b) {
					nodeish = true
				}
			}
			for _, tok := range cands {
				if tok == "" || tok == "." || tok == ".." || strings.HasPrefix(tok, "-") ||
					strings.HasPrefix(tok, "$") || strings.HasPrefix(tok, "~") || strings.Contains(tok, "://") {
					continue
				}
				if b := path.Base(tok); commandRunners[b] && !slices.Contains(ci.runners, b) {
					ci.runners = append(ci.runners, b)
				}
				switch {
				case strings.Contains(tok, "/") || strings.HasPrefix(tok, ".") || hasScriptExt(tok):
					emit(tok)
				case direct && plainWordRE.MatchString(tok):
					plain(tok)
				}
			}
		}
	}
	return ci
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
		rel := repoRel(of, raw)
		if rel == "" || rel == "." {
			continue
		}
		add(rel)
		add(path.Dir(rel))
	}
	return out
}

// repoRel maps one path spelling (relative ones to the repo top of) to a
// repo-relative, slash-separated path, "" when it leaves the repo.
func repoRel(of, raw string) string {
	if filepath.IsAbs(raw) {
		return absInside(of, raw)
	}
	rel := path.Clean(filepath.ToSlash(raw))
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return ""
	}
	return rel
}
