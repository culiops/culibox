package clone

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"syscall"
)

// The configs a host agent, direnv and mise read by themselves name files in
// the repo they run or load (an MCP server script, a hook, a sourced env file).
// A box commit that changes such a file or its folder is as trusted as a change
// to the config, so the merge refuses it (#370). The scan reads the host's work
// tree only (the side the box has not written), never fails the merge, and only
// adds refusals: a file it cannot read or parse is skipped, and a spelling it
// over-reads only costs a refusal.
//
// What it does not see: code a named script imports from elsewhere in the repo
// (the script's own folder is refused, other folders are not), configs below
// the repo top (a nested .mcp.json), .env contents, CI definitions.

const (
	mergeRefFiles     = 128  // config files read per check
	mergeRefPaths     = 2048 // path spellings kept per check
	mergeRefDirScan   = 4096 // entries read of the repo top, for mise.*.toml
	mergeRefLines     = 20000
	mergeRefStmtLines = 200  // lines one TOML statement may span
	mergeRefDepth     = 24   // JSON nesting followed
	mergeRefListings  = 64   // folders read for plain words
	mergeRefPlain     = 8192 // plain word look-ups
)

// runnerManifests are the files a task runner reads at the repo top and runs
// what they say. Matching is case-insensitive, so one spelling each.
var runnerManifests = map[string][]string{
	"npm": nodeManifests, "npx": nodeManifests, "pnpm": nodeManifests, "pnpx": nodeManifests,
	"yarn": nodeManifests, "bun": nodeManifests, "bunx": nodeManifests, "node": nodeManifests,
	"deno":   {"deno.json", "deno.jsonc", "package.json"},
	"make":   {"Makefile", "GNUmakefile"},
	"just":   {"justfile", ".justfile"},
	"task":   {"Taskfile.yml", "Taskfile.yaml"},
	"uv":     {"pyproject.toml"},
	"uvx":    {"pyproject.toml"},
	"poetry": {"pyproject.toml"},
	"pipenv": {"Pipfile"},
	"cargo":  {"Cargo.toml", "build.rs"},
	"go":     {"go.mod"},
	"rake":   {"Rakefile"},
	"bundle": {"Gemfile"},
	"mise":   miseNames,
	"nix":    {"flake.nix", "shell.nix", "default.nix"},
	"devbox": {"devbox.json"},
	"devenv": devenvNames,
}

var (
	nodeManifests = []string{"package.json", ".npmrc", ".yarnrc.yml", ".pnpmfile.cjs"}
	devenvNames   = []string{"devenv.nix", "devenv.yaml", "devenv.lock", "devenv.local.nix", "devenv.local.yaml"}
	// miseNames are the fixed places mise reads config from; mise.<env>.toml
	// files are found by listing the repo top (MergeDenyPatterns covers them).
	miseNames = []string{"mise.toml", ".mise.toml", "mise.local.toml", ".mise.local.toml",
		"mise/config.toml", "mise/config.local.toml", ".mise/config.toml", ".mise/config.local.toml",
		".config/mise.toml", ".config/mise/config.toml", ".config/mise/config.local.toml"}
)

// refConfigFiles are the files scanConfigRefs reads, besides the mise ones
// (miseNames and the top mise.<env>.toml files). The symlink check follows
// each one: a link there leads the read to another file.
var refConfigFiles = append([]string{".mcp.json", ".claude/settings.json", ".claude/settings.local.json", ".envrc", ".envrc.local",
	".pre-commit-config.yaml", ".overcommit.yml", ".vscode/tasks.json"}, lefthookNames()...)

// refSink collects what the scanners find, as path spellings.
type refSink struct {
	of      string
	root    *os.Root // of; nil when it cannot be opened
	reads   int      // config files read
	raws    []string // spellings denied with their folder
	exact   []string // spellings denied as themselves (a directory)
	runners map[string]bool

	listings  map[string]*dirListing // folders read for plain words, by repo-relative path
	plainSeen map[string]bool        // plain word look-ups made, "base\x00word"
}

// dirListing is the names in one folder: lower-cased name -> name as spelled.
// truncated is set when the folder holds more than mergeRefDirScan entries or
// cannot be read as a folder of the repo.
type dirListing struct {
	names     map[string]string
	truncated bool
}

// scanConfigRefs reads the protected configs at the top of the host work tree
// of, and the files in its hook folders (hooks: the repo-relative core.hooksPath
// folders, besides .husky), and then the user's own Claude Code config
// (scanUser: home, and the managed settings), and returns what they name. The
// error is a user file the scan could not read.
func scanConfigRefs(of string, hooks []string, home string) (*refSink, error) {
	s := &refSink{of: filepath.Clean(of), runners: map[string]bool{},
		listings: map[string]*dirListing{}, plainSeen: map[string]bool{}}
	if root, err := os.OpenRoot(s.of); err == nil {
		defer root.Close()
		s.root = root
		s.scanRepo(hooks)
	}
	return s, s.scanUser(home)
}

// scanRepo reads the host work tree's own configs.
func (s *refSink) scanRepo(hooks []string) {
	s.file(".mcp.json", scanMCPFile)
	s.file(".claude/settings.json", scanClaudeSettings)
	s.file(".claude/settings.local.json", scanClaudeSettings)
	s.file(".envrc", scanEnvrc)
	s.file(".envrc.local", scanEnvrc)
	for _, d := range hookScanDirs(hooks) {
		s.hookDir(d)
	}
	s.file(".pre-commit-config.yaml", yamlCommands("entry"))
	for _, n := range lefthookNames() {
		s.file(n, yamlCommands("run"))
	}
	s.file(".overcommit.yml", yamlCommands("command"))
	s.file(".vscode/tasks.json", scanVSCodeTasks)
	miseSeen := map[string]bool{}
	for _, f := range miseNames {
		miseSeen[strings.ToLower(f)] = true
		s.file(f, scanMise)
	}
	for _, f := range s.topMiseFiles() {
		if !miseSeen[strings.ToLower(f)] {
			s.file(f, scanMise)
		}
	}
}

// file reads rel (a regular file, through the root, so no link out of the
// repo is followed) and hands what it holds to scan. A file that is absent,
// not regular or unreadable is skipped.
func (s *refSink) file(rel string, scan func(*refSink, []byte)) {
	if s.root == nil || s.reads >= mergeRefFiles {
		return
	}
	f, err := s.root.OpenFile(filepath.FromSlash(rel), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return
	}
	s.reads++
	if data, err := readRegular(f); err == nil {
		scan(s, data)
	}
}

// topMiseFiles lists the repo top's mise.<env>.toml and .mise.<env>.toml.
func (s *refSink) topMiseFiles() []string {
	d, err := s.root.Open(".")
	if err != nil {
		return nil
	}
	ents, _ := d.ReadDir(mergeRefDirScan)
	d.Close()
	var out []string
	for _, e := range ents {
		n := strings.ToLower(e.Name())
		if e.IsDir() || !strings.HasSuffix(n, ".toml") {
			continue
		}
		if strings.HasPrefix(n, "mise.") || strings.HasPrefix(n, ".mise.") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func (s *refSink) full() bool { return len(s.raws)+len(s.exact) >= mergeRefPaths }

// addRaw keeps a path spelling to deny with its folder.
func (s *refSink) addRaw(p string) {
	if p != "" && !s.full() && !slices.Contains(s.raws, p) {
		s.raws = append(s.raws, p)
	}
}

// pathArg keeps a token the config names as a path whatever it looks like
// (an argument of source_env, an env.path entry).
func (s *refSink) pathArg(tok string) { s.addRaw(cleanRaw(tok)) }

// dir keeps a directory the config names: the directory itself, not its folder.
func (s *refSink) dir(tok string) {
	if t := cleanRaw(tok); t != "" && !s.full() && !slices.Contains(s.exact, t) {
		s.exact = append(s.exact, t)
	}
}

// command reads a command line for the paths it names and the runners it calls.
func (s *refSink) command(c string) {
	if c == "" {
		return
	}
	ci := parseCommand(s.of, c)
	for _, p := range ci.paths {
		s.addRaw(p)
	}
	for _, d := range ci.dirs {
		s.dir(d)
	}
	for _, r := range ci.runners {
		s.runners[r] = true
	}
	for _, pw := range ci.plain {
		s.plainWord(pw)
	}
}

// plainWord denies the entries a plain word of a command names: an entry of
// that name in the repo top or in a folder the command moved to (a script, a
// program's folder), and the same with a script extension ("node server" runs
// server.js or server/index.js). A word with no such entry names nothing. Past
// the look-up caps, or in a folder too big to list, the word is denied as
// spelled: more refusals, never fewer.
func (s *refSink) plainWord(pw plainWord) {
	if s.root == nil {
		return
	}
	for _, base := range pw.bases {
		key := base + "\x00" + pw.word
		if s.plainSeen[key] {
			continue
		}
		if len(s.plainSeen) >= mergeRefPlain {
			s.addRaw(path.Join(base, pw.word))
			continue
		}
		s.plainSeen[key] = true
		l := s.listing(base)
		if l.truncated {
			s.addRaw(path.Join(base, pw.word))
			continue
		}
		for _, cand := range append([]string{""}, scriptExts...) {
			if name, ok := l.names[strings.ToLower(pw.word+cand)]; ok {
				s.addRaw(path.Join(base, name))
			}
		}
	}
}

// listing reads the folder base (repo-relative, "." for the top) through the
// root, once.
func (s *refSink) listing(base string) *dirListing {
	if l, ok := s.listings[base]; ok {
		return l
	}
	if len(s.listings) >= mergeRefListings {
		return &dirListing{truncated: true} // not kept: the cap bounds the memory too
	}
	l := &dirListing{names: map[string]string{}}
	s.listings[base] = l
	// O_DIRECTORY: a FIFO or a file at the name is not opened.
	d, err := s.root.OpenFile(filepath.FromSlash(base), os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_DIRECTORY, 0)
	if err != nil {
		return l // nothing there (or not a folder of the repo): no entries
	}
	defer d.Close()
	ents, _ := d.ReadDir(mergeRefDirScan + 1)
	if len(ents) > mergeRefDirScan {
		l.truncated = true
		return l
	}
	for _, e := range ents {
		k := strings.ToLower(e.Name())
		if _, ok := l.names[k]; !ok {
			l.names[k] = e.Name()
		}
	}
	return l
}

// envValue reads an environment value as a command line; NODE_OPTIONS holds
// node's own options.
func (s *refSink) envValue(key, v string) {
	if strings.EqualFold(key, "NODE_OPTIONS") {
		v = "node " + v
	}
	s.command(v)
}

// paths returns the repo-relative, slash-separated paths to refuse: each
// spelling and its folder (never the repo top), each directory, and the
// manifests of every runner a command called.
func (s *refSink) paths() []string {
	raws := slices.Clone(s.raws)
	names := make([]string, 0, len(s.runners))
	for r := range s.runners {
		names = append(names, r)
	}
	sort.Strings(names)
	for _, r := range names {
		raws = append(raws, runnerManifests[r]...)
	}
	out := repoDenyPaths(s.of, raws)
	for _, e := range s.exact {
		if rel := repoRel(s.of, e); rel != "" && rel != "." && !slices.Contains(out, rel) {
			out = append(out, rel)
		}
	}
	return out
}

// cleanRaw turns one token a config names into a path spelling: the project
// dir spellings become "./", quotes go, and what is not a repo path is "".
func cleanRaw(tok string) string {
	for _, sp := range projectDirSpellings {
		tok = strings.ReplaceAll(tok, sp, "./")
	}
	tok = strings.Trim(strings.TrimSpace(tok), "\"'")
	if tok == "" || tok == "." || tok == ".." || strings.HasPrefix(tok, "-") || strings.HasPrefix(tok, "$") ||
		strings.HasPrefix(tok, "~") || strings.Contains(tok, "://") {
		return ""
	}
	return tok
}

// configRefDenies reads the host's agent, direnv, mise, hook manager and editor
// configs, and the user's Claude Code config (home), for the repo paths they
// name (scanConfigRefs) and returns whether a repo-relative path from the range
// is at, under or a parent of one of them (importDenied's rule, case-folded),
// with the concrete list for the symlink check. It makes no git call. The error
// is a user file that is there and cannot be read or parsed: the merge then does
// not happen.
func (m *merger) configRefDenies(home string, hooks []string) (func(p string) bool, []string, error) {
	sink, err := scanConfigRefs(m.of, hooks, home)
	if err != nil {
		return nil, nil, err
	}
	refs := sink.paths()
	split := make([][]string, len(refs))
	for i, r := range refs {
		split[i] = strings.Split(r, "/")
	}
	return func(p string) bool {
		pc := strings.Split(p, "/")
		for _, tc := range split {
			if foldPrefix(pc, tc) || foldPrefix(tc, pc) {
				return true
			}
		}
		return false
	}, refs, nil
}

// ---- JSON: .mcp.json and .claude/settings*.json

func jsonDoc(data []byte) map[string]any {
	var v any
	if json.Unmarshal(data, &v) != nil {
		return nil
	}
	m, _ := v.(map[string]any)
	return m
}

func str(v any) string { s, _ := v.(string); return s }

func sortedKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func scanMCPFile(s *refSink, data []byte) {
	if m := jsonDoc(data); m != nil {
		s.mcpServers(m["mcpServers"])
	}
}

// mcpServers reads an mcpServers object: each server's command, args and
// environment values are command lines, its cwd a directory.
func (s *refSink) mcpServers(v any) {
	servers, _ := v.(map[string]any)
	for _, name := range sortedKeys(servers) {
		srv, _ := servers[name].(map[string]any)
		// The command and its arguments are one command line: "-m" and its
		// module, or "--directory" and its folder, are separate arguments.
		line := []string{str(srv["command"])}
		args, _ := srv["args"].([]any)
		for _, a := range args {
			line = append(line, str(a))
		}
		s.command(strings.Join(line, " "))
		env, _ := srv["env"].(map[string]any)
		for _, k := range sortedKeys(env) {
			s.envValue(k, str(env[k]))
		}
		s.dir(str(srv["cwd"]))
	}
}

// scanClaudeSettings reads the commands Claude Code runs from a project
// settings file and the local directories it loads.
func scanClaudeSettings(s *refSink, data []byte) {
	if m := jsonDoc(data); m != nil {
		scanClaudeSettingsDoc(s, m)
	}
}

// scanClaudeSettingsDoc is scanClaudeSettings on a parsed settings object.
func scanClaudeSettingsDoc(s *refSink, m map[string]any) {
	commandsIn(m["hooks"], 0, s.command)
	switch v := m["statusLine"].(type) {
	case string:
		s.command(v)
	case map[string]any:
		s.command(str(v["command"]))
	}
	for _, k := range []string{"apiKeyHelper", "otelHeadersHelper", "awsAuthRefresh", "awsCredentialExport"} {
		s.command(str(m[k]))
	}
	env, _ := m["env"].(map[string]any)
	for _, k := range sortedKeys(env) {
		s.envValue(k, str(env[k])) // BASH_ENV and the like name a file the shell sources
	}
	s.mcpServers(m["mcpServers"])
	mk, _ := m["extraKnownMarketplaces"].(map[string]any)
	for _, k := range sortedKeys(mk) {
		entry, _ := mk[k].(map[string]any)
		src, _ := entry["source"].(map[string]any)
		s.dir(str(src["path"]))
	}
}

// commandsIn calls f with every string under a "command" key in v.
func commandsIn(v any, depth int, f func(string)) {
	if depth > mergeRefDepth {
		return
	}
	switch t := v.(type) {
	case map[string]any:
		for _, k := range sortedKeys(t) {
			if c, ok := t[k].(string); ok && k == "command" {
				f(c)
			} else {
				commandsIn(t[k], depth+1, f)
			}
		}
	case []any:
		for _, e := range t {
			commandsIn(e, depth+1, f)
		}
	}
}

// ---- .envrc

// scanEnvrc reads a direnv file line by line. direnv trusts the file's own
// content, but what it sources, loads or watches is read again without a new
// "direnv allow". Every line is also read as a command.
func scanEnvrc(s *refSink, data []byte) {
	for i, line := range strings.Split(string(data), "\n") {
		if i >= mergeRefLines {
			return
		}
		t := strings.TrimSpace(line)
		if t == "" || t[0] == '#' {
			continue
		}
		for _, sp := range projectDirSpellings {
			t = strings.ReplaceAll(t, sp, "./")
		}
		toks := strings.FieldsFunc(t, commandDelim)
		if len(toks) == 0 {
			continue
		}
		first, args := toks[0], nonFlags(toks[1:])
		if name, ok := strings.CutPrefix(first, "use_"); ok { // use_nix is use nix
			first, args = "use", append([]string{name}, args...)
		}
		switch first {
		case "source_env", "source_env_if_exists", "source", ".", "watch_file", "watch_dir", "PATH_add", "MANPATH_add":
			for _, a := range args {
				s.pathArg(a)
			}
		case "path_add": // path_add VAR dir...
			for _, a := range args[min(1, len(args)):] {
				s.pathArg(a)
			}
		case "dotenv", "dotenv_if_exists":
			if len(args) == 0 {
				s.addRaw(".env")
			}
			for _, a := range args {
				s.pathArg(a)
			}
		case "use":
			s.envrcUse(args)
		}
		s.command(t)
	}
}

func nonFlags(toks []string) []string {
	var out []string
	for _, t := range toks {
		if !strings.HasPrefix(t, "-") {
			out = append(out, t)
		}
	}
	return out
}

// envrcUse reads "use <what> args": the files nix-direnv and devenv watch.
func (s *refSink) envrcUse(args []string) {
	if len(args) == 0 {
		return
	}
	switch args[0] {
	case "flake":
		dir := ""
		if len(args) > 1 {
			ref, _, _ := strings.Cut(args[1], "#")
			ref = strings.TrimPrefix(ref, "path:")
			if !strings.Contains(ref, ":") { // a remote flake is not a repo path
				dir = ref
			}
		}
		s.addRaw(path.Join(cleanDirRaw(dir), "flake.nix"))
		s.addRaw(path.Join(cleanDirRaw(dir), "flake.lock"))
		if dir != "" {
			s.dir(dir)
		}
	case "nix":
		s.addRaw("shell.nix")
		s.addRaw("default.nix")
		for _, a := range args[1:] {
			s.pathArg(a)
		}
	case "devenv":
		for _, n := range devenvNames {
			s.addRaw(n)
		}
	}
}

// cleanDirRaw is a flake's directory spelling, "." when it is empty or not a path.
func cleanDirRaw(d string) string {
	if c := cleanRaw(d); c != "" {
		return c
	}
	return "."
}

// ---- mise

var (
	miseSectionRE = regexp.MustCompile(`^\[\[?\s*([^\[\]]+?)\s*\]\]?\s*(#.*)?$`)
	quotedRE      = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"|'([^']*)'`)
)

// scanMise reads a mise config with a tolerant line scanner (no TOML parser):
// the files [env] sources or loads, the commands [hooks] runs, and a command
// an env value's exec() template runs.
func scanMise(s *refSink, data []byte) {
	lines := strings.Split(string(data), "\n")
	if len(lines) > mergeRefLines {
		lines = lines[:mergeRefLines]
	}
	sec := ""
	for i := 0; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if t == "" || t[0] == '#' {
			continue
		}
		if m := miseSectionRE.FindStringSubmatch(t); m != nil {
			sec = strings.ToLower(strings.Trim(m[1], "\"' "))
			continue
		}
		switch {
		case sec == "hooks" || strings.HasPrefix(sec, "hooks."):
			s.command(t)
		case sec == "env" || sec == "env._" || sec == "settings":
			key, rhs, ok := strings.Cut(t, "=")
			if !ok {
				continue
			}
			key = strings.ReplaceAll(strings.TrimSpace(key), " ", "")
			stmt := rhs
			if depth := bracketDepth(stmt); depth > 0 {
				for n := 0; depth > 0 && n < mergeRefStmtLines && i+1 < len(lines); n++ {
					i++
					stmt += "\n" + lines[i]
					depth = bracketDepth(stmt)
				}
			}
			switch {
			case sec == "env" && (key == "_.source" || key == "_.file" || key == "_.path" || key == "_"),
				sec == "env._" && (key == "source" || key == "file" || key == "path"),
				sec == "settings" && key == "env_file":
				for _, q := range quotedRE.FindAllStringSubmatch(stmt, -1) {
					s.pathArg(q[1] + q[2])
				}
			case strings.Contains(stmt, "exec("):
				s.command(stmt)
			}
		}
	}
}

// bracketDepth counts the [ and { not closed in s, outside quoted strings and
// comments (a quote is closed at the end of its line).
func bracketDepth(s string) int {
	depth := 0
	for _, line := range strings.Split(s, "\n") {
		var q rune
	chars:
		for _, r := range line {
			switch {
			case q != 0:
				if r == q {
					q = 0
				}
			case r == '"' || r == '\'':
				q = r
			case r == '#':
				break chars
			case r == '[' || r == '{':
				depth++
			case r == ']' || r == '}':
				depth--
			}
		}
	}
	return depth
}
