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
// table, a build file, a lock-in config) and run what it says. npx, pnpx and
// bunx run a local bin (which reads its config from package.json) unless the
// spec is clearly remote (fetchRemote); uvx always fetches into an isolated
// environment, so it is no runner.
var commandRunners = map[string]bool{
	"npm": true, "npx": true, "pnpm": true, "pnpx": true, "yarn": true, "bun": true, "bunx": true,
	"node": true, "deno": true, "make": true, "just": true, "task": true, "uv": true,
	"poetry": true, "pipenv": true, "cargo": true, "go": true, "rake": true, "bundle": true,
	"mise": true, "nix": true, "devbox": true, "devenv": true,
}

// Where a command line runs a word as a file (#370 B-M1). Only the command word,
// an interpreter's script and the tool a fetcher runs are looked up as plain
// words; a subcommand, a flag value or another argument is not.
var (
	// interpreterRE: their first non-option argument is a script file.
	interpreterRE = regexp.MustCompile(`^(node|nodejs|python[0-9.]*|pypy[0-9.]*|py|bash|sh|zsh|dash|ksh|fish|deno|bun|ruby|perl|php|tsx|ts-node|lua)$`)
	// shellRE: "-c" makes what follows a new command.
	shellRE = regexp.MustCompile(`^(bash|sh|zsh|dash|ksh|fish)$`)
)

// fetchers: the next non-option word is the tool they run.
var fetchers = map[string]bool{"npx": true, "bunx": true, "pnpx": true, "uvx": true}

// fetchSubs: after the subcommand, the next non-option word is the tool.
var fetchSubs = map[string]map[string]bool{"pnpm": {"dlx": true, "exec": true}, "yarn": {"dlx": true, "exec": true},
	"npm": {"exec": true, "x": true}, "pipx": {"run": true}, "bun": {"x": true}}

// noManifestSubs always fetch into an isolated environment: they add no runner.
var noManifestSubs = map[string]map[string]bool{"pnpm": {"dlx": true}, "yarn": {"dlx": true}}

// restartSubs: the next non-option word is a new command word (they run a
// command, so reading it as one only adds refusals).
var restartSubs = map[string]map[string]bool{"uv": {"run": true}, "poetry": {"run": true}, "pipenv": {"run": true},
	"pdm": {"run": true}, "hatch": {"run": true}, "rye": {"run": true}, "bundle": {"exec": true}}

// commandWrappers: the next word is still the command word.
var commandWrappers = map[string]bool{"env": true, "exec": true, "command": true, "builtin": true, "nohup": true,
	"time": true, "nice": true, "sudo": true, "xargs": true}

// shellKeywords start a compound command: the next word is still the command
// word, and the keyword itself names nothing.
var shellKeywords = map[string]bool{"if": true, "then": true, "else": true, "elif": true, "do": true,
	"while": true, "until": true}

// fetchRemoteFlags make a fetcher fetch the package, not run a local bin.
var fetchRemoteFlags = map[string]bool{"-y": true, "--yes": true, "-p": true, "--package": true}

// wordRole is what the next plain word of a simple command is.
type wordRole int

const (
	roleCmd    wordRole = iota // the command word
	roleSub                    // a subcommand of the command word (fetchSubs, restartSubs)
	roleScript                 // an interpreter's script argument
	roleTool                   // the tool a fetcher runs
	roleNone                   // an argument: not looked up
)

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
	tool  bool     // the tool-config prefix rule applies: the command word and a fetcher's tool
}

// cmdInfo is what parseCommand finds in a command line.
type cmdInfo struct {
	paths   []string    // spellings that are paths: each is denied with its folder
	dirs    []string    // repo-relative folders named by a flag or cd: denied as themselves
	runners []string    // commandRunners the command calls
	plain   []plainWord // words that name an entry only if one exists
	scripts []scriptRef // package scripts and recipes the runners called run
	over    bool        // a cap stopped the parse: mergeRefPaths or maxCommandCwds
}

// scriptRef is a call of a runner that runs scripts or recipes kept in the
// repo (#370): npm, pnpm, yarn and bun run package.json scripts, make, just and
// task run recipes. dirs are the folders it may run in: "." and the folders the
// command moved to.
type scriptRef struct {
	runner    string
	names     []string // package scripts the call runs, with their pre and post scripts
	all       bool     // the name is not known: every script (a recipe runner: the whole file)
	dirs      []string
	workspace bool     // a workspace call: the scripts of other packages cannot be followed
	badFile   bool     // a file option whose value is no literal path
	files     []string // recipe files the call names ("make -f x.mk", "just --justfile x", "task -t x")
}

// recipeFileOpt is the option that names the recipe file of a runner: its long
// names and its short letter. recipeValueLetters are the short options of the
// runner that take a value of their own (a cluster stops at one).
var recipeFileOpt = map[string]struct {
	long   []string
	letter byte
}{
	"make": {[]string{"--file", "--makefile"}, 'f'},
	"just": {[]string{"--justfile"}, 'f'},
	"task": {[]string{"--taskfile"}, 't'},
}

var recipeValueLetters = map[string]string{"make": "CIjloW", "just": "dEscC", "task": "dCoJ"}

// recipeFilesIn returns the values of the runner's file options in args: "-f x",
// "-fx", a cluster such as "-sf x" or "-sfx", "--file x" and "--file=x". bad is
// true when a value is missing or is not a literal path ($, parentheses, "{{",
// a backtick): the file cannot be known.
func recipeFilesIn(runner string, args []string) (files []string, bad bool) {
	opt := recipeFileOpt[runner]
	add := func(val string) {
		if val == "" || strings.ContainsAny(val, "$(`") || strings.Contains(val, "{{") {
			bad = true
			return
		}
		files = append(files, val)
	}
	next := func(i int) string {
		if i+1 < len(args) {
			return args[i+1]
		}
		return ""
	}
	for i, a := range args {
		if a == "--" {
			break
		}
		switch {
		case strings.HasPrefix(a, "--"):
			name, val, hasVal := strings.Cut(a, "=")
			if !slices.Contains(opt.long, name) {
				continue
			}
			if !hasVal {
				val = next(i)
			}
			add(val)
		case strings.HasPrefix(a, "-") && len(a) > 1:
			cluster := a[1:]
			for k := 0; k < len(cluster); k++ {
				if cluster[k] == opt.letter {
					val := cluster[k+1:]
					if val == "" {
						val = next(i)
					}
					add(val)
					break
				}
				if strings.IndexByte(recipeValueLetters[runner], cluster[k]) >= 0 {
					break // this option takes the rest as its value
				}
			}
		}
	}
	return files, bad
}

// nodeScriptRunners read package.json scripts; recipeRunners read a recipe file.
var (
	nodeScriptRunners = map[string]bool{"npm": true, "pnpm": true, "yarn": true, "bun": true}
	recipeRunners     = map[string]bool{"make": true, "just": true, "task": true}
)

var installScripts = []string{"preinstall", "install", "postinstall", "prepublish", "preprepare", "prepare", "postprepare"}

// scriptNames returns the package scripts "<runner> args..." runs (args are the
// words after the runner up to the end of the command): the script named, with
// its pre and post scripts, or all when the name is not known (an option came
// first, or the name is a variable). npm's own names (test, start, install ...)
// map to the scripts they run; a first word that is no subcommand is read as a
// script name too (yarn, pnpm and bun run a script by its bare name), which
// over-reads npm's other subcommands and only adds refusals.
func scriptNames(runner string, args []string) (names []string, all bool) {
	wrap := func(n string) []string { return []string{"pre" + n, n, "post" + n} }
	if len(args) == 0 {
		if runner == "npm" {
			return nil, false
		}
		return slices.Clone(installScripts), false
	}
	first := strings.Trim(args[0], "\"'")
	if first == "" || strings.HasPrefix(first, "-") || strings.HasPrefix(first, "$") {
		return nil, true
	}
	switch first {
	case "run", "run-script", "rum", "urn":
		if len(args) < 2 {
			return nil, true
		}
		n := strings.Trim(args[1], "\"'")
		if n == "" || strings.HasPrefix(n, "-") || strings.HasPrefix(n, "$") {
			return nil, true
		}
		return wrap(n), false
	case "test", "t", "tst":
		return wrap("test"), false
	case "start", "stop":
		return wrap(first), false
	case "restart":
		return slices.Concat(wrap("restart"), wrap("stop"), wrap("start")), false
	case "install", "i", "ci", "add":
		return slices.Clone(installScripts), false
	case "exec", "x", "dlx":
		return nil, false
	case "pack", "publish":
		return []string{"prepack", "prepare", "postpack", "prepublishOnly", "prepublish", "publish", "postpublish"}, false
	case "it", "install-test", "cit", "install-ci-test":
		return slices.Concat(installScripts, wrap("test")), false
	case "rebuild":
		return []string{"preinstall", "install", "postinstall", "prepare"}, false
	case "version":
		return wrap("version"), false
	}
	return wrap(first), false
}

// workspaceFlags make a package manager run a script in other workspace
// packages; -w is npm's and pnpm's.
var workspaceFlags = map[string]bool{"--workspaces": true, "--workspace": true, "-ws": true, "--filter": true,
	"-F": true, "-r": true, "--recursive": true}

// workspaceCall reports whether "<runner> args..." runs scripts of workspace
// packages (an option, or yarn's workspace subcommands). Their package.json
// files are not followed: the scan fails. Arguments after "--" belong to the
// script.
func workspaceCall(runner string, args []string) bool {
	if runner == "yarn" && len(args) > 0 && (args[0] == "workspace" || args[0] == "workspaces") {
		return true
	}
	for _, a := range args {
		if a == "--" {
			break
		}
		if a == "exec" || a == "dlx" { // runs a program, not a package script
			return false
		}
	}
	for _, a := range args {
		if a == "--" {
			break
		}
		name, _, _ := strings.Cut(a, "=")
		if workspaceFlags[name] || (name == "-w" && (runner == "npm" || runner == "pnpm")) {
			return true
		}
	}
	return false
}

// scriptCall reads tok (with the words after it) as a call of a runner that
// runs scripts or recipes kept in the repo.
func scriptCall(tok string, args []string) (scriptRef, bool) {
	rb := path.Base(tok)
	if !(nodeScriptRunners[rb] || recipeRunners[rb] || rb == "node") || strings.HasPrefix(tok, "-") ||
		strings.HasPrefix(tok, "$") || envAssignRE.MatchString(tok) {
		return scriptRef{}, false
	}
	ref := scriptRef{runner: rb, all: recipeRunners[rb]}
	switch {
	case rb == "node": // "node --run name" runs that script only
		n, ok := nodeRunName(args)
		if !ok {
			return scriptRef{}, false
		}
		ref.all = n == "" || strings.HasPrefix(n, "$") || strings.HasPrefix(n, "-")
		if !ref.all {
			ref.names = []string{n}
		}
	case nodeScriptRunners[rb]:
		ref.workspace = workspaceCall(rb, args)
		ref.names, ref.all = scriptNames(rb, args)
	default:
		ref.files, ref.badFile = recipeFilesIn(rb, args)
	}
	return ref, true
}

// nodeRunName returns the script "node --run NAME" runs ("" when it is not
// that call); nameKnown is false for a variable.
func nodeRunName(args []string) (name string, ok bool) {
	for i, a := range args {
		if v, found := strings.CutPrefix(a, "--run="); found {
			return strings.Trim(v, "\"'"), true
		}
		if a == "--run" {
			if i+1 < len(args) {
				return strings.Trim(args[i+1], "\"'"), true
			}
			return "", true
		}
	}
	return "", false
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
//     and in those folders only where a tool runs it as a file: the command word
//     of each simple command (after env assignments, wrappers such as env, sudo
//     or xargs, shell keywords, "uv run" / "bundle exec" and the like, and
//     "bash -c"), the script argument of an interpreter (interpreterRE; after an
//     option with no "=", the next two words), and the tool a fetcher runs
//     (fetchers, fetchSubs; its "@version" cut). The fetched tool then reads as
//     a command word (a runner keeps its manifest: "npx -y yarn install"). An
//     interpreter, fetcher or wrapper later in the command starts a new command
//     word ("cross-env X=1 node srv"), except among a script's own arguments;
//     a variable command word ("$PY srv") makes the next word a script.
//     Subcommands, flag values and other arguments are not looked up. The
//     command word and a fetcher's tool also get the tool-config prefix rule
//     (plainWord.tool);
//   - runners: every word whose base is a commandRunner, except a fetcher whose
//     spec is clearly remote (a version, a scope, or -y/--yes/-p/--package), and
//     "pnpm dlx" / "yarn dlx", which add none.
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
	plain := func(w string, tool bool) {
		bases := append([]string{"."}, cwds...)
		k := w + "\x00" + strings.Join(bases, "\x00")
		if tool {
			k += "\x00t"
		}
		if !plainSeen[k] {
			if len(ci.plain) >= mergeRefPaths {
				ci.over = true
				return
			}
			plainSeen[k] = true
			ci.plain = append(ci.plain, plainWord{word: w, bases: bases, tool: tool})
		}
	}
	addRunner := func(b string) {
		if !slices.Contains(ci.runners, b) {
			ci.runners = append(ci.runners, b)
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
		var (
			role    = roleCmd
			cmd     string // the base of the last command word
			tries   int    // > 0: an option came first, so its value may come before the word
			pending string // a fetcher runner waiting to see whether its spec is remote
			remote  bool
			// scriptDone: an interpreter's script was read; what follows is its
			// own arguments, not a new command.
			scriptDone bool
			segRefs    []int // ci.scripts made in this segment: their folders are set at its end
		)
		// classify sets the role of the word after the command word b.
		classify := func(b string, maybeValue bool) {
			cmd, scriptDone = b, false
			switch {
			case commandWrappers[b] || shellKeywords[b]:
				role = roleCmd
			case fetchSubs[b] != nil || restartSubs[b] != nil:
				role = roleSub
			case fetchers[b]:
				role = roleTool
			case interpreterRE.MatchString(b) || b == "source" || b == ".":
				role = roleScript
			default:
				role = roleNone
			}
			if maybeValue && role == roleNone { // "sudo -u root node x": root may be the -u value
				role = roleCmd
			}
		}
		// next ends the script or tool slot, or keeps it for one more word.
		next := func() {
			if tries > 1 {
				tries--
				return
			}
			role, tries = roleNone, 0
		}
		// cmdWord reads tok (base b) as a command word.
		cmdWord := func(tok, b string, pathy bool) {
			maybeValue := tries > 0
			tries = 0
			if !pathy && !shellKeywords[tok] && plainWordRE.MatchString(tok) {
				plain(tok, true)
			}
			if (fetchers[b] || fetchSubs[b] != nil) && commandRunners[b] {
				if pending != "" && !remote {
					addRunner(pending)
				}
				pending, remote = b, false
			} else if commandRunners[b] {
				addRunner(b)
			}
			classify(b, maybeValue)
		}
		// restarts: a word that starts a new command wherever it stands
		// ("cross-env X=1 node srv", "timeout 60 node srv"), unless it is an
		// argument of an interpreter's script.
		restarts := func(b string) bool {
			return !scriptDone && (interpreterRE.MatchString(b) || fetchers[b] || commandWrappers[b])
		}
		toks := strings.FieldsFunc(seg, commandDelim)
		for i := 0; i < len(toks); i++ {
			tok := strings.TrimRight(strings.TrimLeft(toks[i], "!{"), "}") // git's "!" alias prefix, a shell function's braces
			if tok == "" {
				continue // a lone "!" or brace: the next word is still the command word
			}
			if ref, ok := scriptCall(tok, toks[i+1:]); ok {
				if len(ci.scripts) >= mergeRefPaths {
					ci.over = true
				} else {
					ci.scripts = append(ci.scripts, ref)
					segRefs = append(segRefs, len(ci.scripts)-1)
				}
			}
			cands := []string{tok}
			direct := false // a word of its own, not a value read out of an option or assignment
			switch {
			case strings.HasPrefix(tok, "-"):
				name, val, hasVal := strings.Cut(tok, "=")
				if role == roleTool && pending != "" && fetchRemoteFlags[name] {
					remote = true
				}
				if role == roleScript && shellRE.MatchString(cmd) && shellRunsArg(name) {
					role, tries = roleCmd, 0 // bash -c "node x": node is a command word
					continue
				}
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
					role, tries, scriptDone = roleNone, 0, true // the module is the program
					continue
				case pyish && !hasVal && len(name) > 2 && strings.HasPrefix(name, "-m"):
					emitModule(name[2:]) // "-mmodule", no space
					role, tries, scriptDone = roleNone, 0, true
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
						plain(val, false) // as spelled: "a.b" may be an entry too
					}
					continue
				}
				if !hasVal {
					// An option with no "=" may take the next word as its value:
					// the word after it is tried too.
					if role == roleScript || role == roleCmd || role == roleSub || role == roleTool && (name == "-p" || name == "--package") {
						tries = 2
					}
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
				skip := tok == "" || tok == "." || tok == ".." || strings.HasPrefix(tok, "-") ||
					strings.HasPrefix(tok, "$") || strings.HasPrefix(tok, "~") || strings.Contains(tok, "://")
				b := path.Base(tok)
				if skip {
					if direct && role == roleCmd && tok != "" && tok != ".." { // "$PY srv", ". ./env.sh"
						classify(b, tries > 0)
						tries = 0
						if strings.HasPrefix(tok, "$") && !strings.Contains(tok, "/") {
							role = roleScript // "$PY srv": the variable may name an interpreter
						}
					} else if direct && (role == roleScript || role == roleTool) {
						next()
					}
					continue
				}
				pathy := strings.Contains(tok, "/") || strings.HasPrefix(tok, ".") || hasScriptExt(tok)
				if pathy {
					emit(tok)
				}
				if !direct {
					if commandRunners[b] {
						addRunner(b)
					}
					continue
				}
				switch role {
				case roleCmd:
					cmdWord(tok, b, pathy)
				case roleSub:
					switch {
					case fetchSubs[cmd][tok]:
						role, tries = roleTool, 0
						if noManifestSubs[cmd][tok] {
							pending = ""
						}
					case restartSubs[cmd][tok]:
						role, tries = roleCmd, 0
					case tries > 0: // "pnpm --filter web exec": maybe the option's value
						tries = 0
						if interpreterRE.MatchString(cmd) && !pathy && plainWordRE.MatchString(tok) {
							plain(tok, false) // or the script of "bun --hot server"
						}
					case restarts(b):
						cmdWord(tok, b, pathy)
					case interpreterRE.MatchString(cmd): // "bun server": the script
						if !pathy && plainWordRE.MatchString(tok) {
							plain(tok, false)
						}
						if commandRunners[b] {
							addRunner(b)
						}
						role, scriptDone = roleNone, true
					default:
						if commandRunners[b] {
							addRunner(b)
						}
						role = roleNone
					}
				case roleScript:
					if !pathy && plainWordRE.MatchString(tok) {
						plain(tok, false)
					}
					if commandRunners[b] {
						addRunner(b)
					}
					next()
					if role == roleNone {
						scriptDone = true
					}
				case roleTool:
					if pending != "" && strings.Contains(tok, "@") { // a scope or a version: fetched
						remote = true
					}
					w := tok
					if !strings.HasPrefix(w, "@") {
						w, _, _ = strings.Cut(w, "@")
					}
					if !pathy && plainWordRE.MatchString(w) {
						plain(w, true)
					}
					// The fetched tool runs like a command word: a runner keeps its
					// manifest ("npx -y yarn install", "uvx poetry run pytest").
					tb := path.Base(w)
					if commandRunners[tb] {
						addRunner(tb)
					}
					if tries > 1 {
						tries--
					} else {
						tries = 0
						classify(tb, false)
					}
				default:
					if restarts(b) {
						cmdWord(tok, b, pathy)
					} else if commandRunners[b] {
						addRunner(b)
					}
				}
			}
		}
		if pending != "" && !remote {
			addRunner(pending)
		}
		// The folders the segment moved to (cd, -C, --prefix) apply to its runners.
		for _, k := range segRefs {
			ci.scripts[k].dirs = append([]string{"."}, cwds...)
		}
	}
	return ci
}

// shellRunsArg reports whether a shell option makes the next word a command
// line: "-c", or a cluster of short options holding c ("-ec", "-lc").
func shellRunsArg(name string) bool {
	return len(name) > 1 && name[0] == '-' && name[1] != '-' && strings.ContainsRune(name[1:], 'c')
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
