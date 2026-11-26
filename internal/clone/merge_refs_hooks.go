package clone

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"syscall"
)

// Hook managers and editor tasks run what they name when the user commits,
// pushes or runs a task (#370). The merge refuses a box commit that touches the
// manager's own file by name; this file reads those files, in the host's work
// tree, for the commands they run, so what the commands name is refused too:
// the files directly in .husky/ and in a repo-relative core.hooksPath folder,
// the entry: values of .pre-commit-config.yaml, the run: values of lefthook
// files, the command: values of .overcommit.yml and the commands of
// .vscode/tasks.json. The same bounds as the other scans: regular files read
// through the root, at most mergeImportBytes each, an unreadable or malformed
// file skipped, only refusals added.
//
// Not read: lefthook's scripts: folder, pre-commit's args:, .vscode/launch.json
// and a task's options.cwd.

const mergeRefDirFiles = 64 // files read per hook folder

// lefthookNames are the lefthook files at the repo top (the names of
// MergeDenyNames that start with lefthook or .lefthook).
func lefthookNames() []string {
	var out []string
	for _, n := range MergeDenyNames {
		if strings.HasPrefix(n, "lefthook") || strings.HasPrefix(n, ".lefthook") {
			out = append(out, n)
		}
	}
	return out
}

// hookScanDirs are the folders whose files are git hooks of the host repo: .husky
// and the repo-relative core.hooksPath folders (the repo top is not one: the
// merge refuses everything then).
func hookScanDirs(hooks []string) []string {
	dirs := []string{".husky"}
	for _, h := range hooks {
		if h = path.Clean(h); h != "." && h != "" && !slices.ContainsFunc(dirs, func(d string) bool { return strings.EqualFold(d, h) }) {
			dirs = append(dirs, h)
		}
	}
	return dirs
}

// dirFiles lists the files directly in the repo-relative folder rel (not its
// folders), by name. More than limit files, or a folder that cannot be listed,
// fails the scan.
func (s *refSink) dirFiles(rel string, limit int) []string {
	if s.root == nil {
		return nil
	}
	d, err := s.root.OpenFile(filepath.FromSlash(rel), os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_DIRECTORY, 0)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			s.fail(fmt.Errorf("cannot list %s: %w", rel, err))
		}
		return nil
	}
	defer d.Close()
	ents, err := d.ReadDir(mergeRefDirScan + 1)
	if len(ents) > mergeRefDirScan {
		s.fail(errScanCap)
		return nil
	}
	if err != nil && !errors.Is(err, io.EOF) {
		s.fail(fmt.Errorf("cannot list %s: %w", rel, err))
		return nil
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() && e.Name() != "_" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) > limit {
		s.fail(errScanCap)
		return nil
	}
	return names
}

// hookDir reads every file directly in the folder as a shell script.
func (s *refSink) hookDir(rel string) {
	for _, n := range s.dirFiles(rel, mergeRefDirFiles) {
		s.file(path.Join(rel, n), scanShell)
	}
}

// scanShell reads a script line by line: every line that is not a comment is a
// command.
func scanShell(s *refSink, data []byte) {
	for i, line := range strings.Split(string(data), "\n") {
		if i >= mergeRefLines {
			if moreLines(strings.Split(string(data), "\n"), i) {
				s.fail(errScanCap)
			}
			return
		}
		if t := strings.TrimSpace(line); t != "" && t[0] != '#' {
			s.command(t)
		}
	}
}

var (
	yamlBlockRE = regexp.MustCompile(`^[|>][+-]?[0-9]*$`)
	// yamlListRepl turns a flow list ['a', 'b'] into words.
	yamlListRepl = strings.NewReplacer("[", " ", "]", " ", ",", " ")
)

// yamlCommands returns a scanner for the values of the given keys, with a line
// scanner (no YAML library): "key: value" on a line, where a "- " may come
// before the key, and a value that is empty or a block scalar (| or >) read from
// the lines indented more than the key (an empty value also from "- " lines at
// the key's own indent). Every value is read as a command line; a flow list is
// read as its words.
func yamlCommands(keys ...string) func(*refSink, []byte) {
	re := regexp.MustCompile(`^(\s*(?:-\s+)*)(?:` + strings.Join(keys, "|") + `):\s*(.*)$`)
	return func(s *refSink, data []byte) {
		lines := strings.Split(string(data), "\n")
		if len(lines) > mergeRefLines {
			lines = lines[:mergeRefLines]
		}
		for i := 0; i < len(lines); i++ {
			m := re.FindStringSubmatch(strings.TrimRight(lines[i], "\r"))
			if m == nil || strings.HasPrefix(strings.TrimSpace(lines[i]), "#") {
				continue
			}
			keyIndent := len(m[1])
			val, _, _ := strings.Cut(m[2], " #")
			val = strings.TrimSpace(val)
			if val != "" && !yamlBlockRE.MatchString(val) {
				s.command(yamlListRepl.Replace(val))
				continue
			}
			for n := 0; n < mergeRefStmtLines && i+1 < len(lines); n++ {
				next := strings.TrimRight(lines[i+1], "\r")
				t := strings.TrimSpace(next)
				indent := len(next) - len(strings.TrimLeft(next, " \t"))
				switch {
				case t == "":
				case indent > keyIndent, val == "" && indent == keyIndent && strings.HasPrefix(t, "- "):
				default:
					n = mergeRefStmtLines // the block ended
					continue
				}
				i++
				for strings.HasPrefix(t, "- ") {
					t = strings.TrimSpace(t[2:])
				}
				if t != "" && t[0] != '#' {
					s.command(yamlListRepl.Replace(t))
				}
			}
		}
	}
}

// ---- .vscode/tasks.json

// scanVSCodeTasks reads a tasks file (JSON with comments and trailing commas):
// the command and arguments of every task, and of its windows, linux and osx
// variants. A file that does not parse after the comments and trailing commas
// are taken out is skipped.
func scanVSCodeTasks(s *refSink, data []byte) {
	m := jsonDoc(stripJSONC(data))
	if m == nil {
		return
	}
	s.vscodeTask(m) // the old format has command and args at the top
	tasks, _ := m["tasks"].([]any)
	for _, t := range tasks {
		if tm, ok := t.(map[string]any); ok {
			s.vscodeTask(tm)
		}
	}
}

func (s *refSink) vscodeTask(m map[string]any) {
	s.vscodeCommand(m)
	for _, osName := range []string{"windows", "linux", "osx"} {
		if sub, ok := m[osName].(map[string]any); ok {
			s.vscodeCommand(sub)
		}
	}
}

// vscodeCommand reads a task's command and args as one command line. Each is a
// string or a {"value": ...} object.
func (s *refSink) vscodeCommand(m map[string]any) {
	word := func(v any) string {
		if o, ok := v.(map[string]any); ok {
			return str(o["value"])
		}
		return str(v)
	}
	line := []string{word(m["command"])}
	args, _ := m["args"].([]any)
	for _, a := range args {
		line = append(line, word(a))
	}
	s.command(strings.Join(line, " "))
}

// stripJSONC takes the comments (// and /* */) and the trailing commas out of
// JSON with comments, leaving strings alone. It is tolerant: an unterminated
// comment or string just ends the text, which then fails to parse.
func stripJSONC(b []byte) []byte {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	out := make([]byte, 0, len(b))
	inStr := false
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case inStr:
			out = append(out, c)
			if c == '\\' && i+1 < len(b) {
				i++
				out = append(out, b[i])
			} else if c == '"' {
				inStr = false
			}
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			for i < len(b) && b[i] != '\n' {
				i++
			}
			if i < len(b) {
				out = append(out, '\n')
			}
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			i += 2
			for i+1 < len(b) && !(b[i] == '*' && b[i+1] == '/') {
				i++
			}
			i++ // the "*"; the loop's own step passes the "/"
			out = append(out, ' ')
		default:
			out = append(out, c)
		}
	}
	res := make([]byte, 0, len(out))
	inStr = false
	for i := 0; i < len(out); i++ {
		c := out[i]
		switch {
		case inStr:
			res = append(res, c)
			if c == '\\' && i+1 < len(out) {
				i++
				res = append(res, out[i])
			} else if c == '"' {
				inStr = false
			}
		case c == '"':
			inStr = true
			res = append(res, c)
		case c == ',':
			j := i + 1
			for j < len(out) && (out[j] == ' ' || out[j] == '\t' || out[j] == '\n' || out[j] == '\r') {
				j++
			}
			if j < len(out) && (out[j] == '}' || out[j] == ']') {
				continue
			}
			res = append(res, c)
		default:
			res = append(res, c)
		}
	}
	return res
}

var (
	recipeKeyRE     = regexp.MustCompile(`^[A-Za-z0-9_./-]+:(?:\s+|$)`)
	recipeIncludeRE = regexp.MustCompile(`^(include|-include|sinclude|import\??|mod\??)\s+(.*)$`)
	taskIncludeRE   = regexp.MustCompile(`^\s*(?:-\s+)?taskfile:\s*(\S.*)$`)
	taskEntryRE     = regexp.MustCompile(`^\s+[A-Za-z0-9_.-]+:\s+(\S.*)$`)
)

// flowYAML reports whether a YAML value is written in flow form ({...} or [...]).
func flowYAML(v string) bool {
	v = strings.TrimSpace(v)
	return strings.HasPrefix(v, "{") || strings.HasPrefix(v, "[")
}

// moreLines reports whether lines (from index i) hold a line that is not empty
// or a comment: a cap that stops before one of those must fail.
func moreLines(lines []string, i int) bool {
	for ; i < len(lines); i++ {
		if t := strings.TrimSpace(lines[i]); t != "" && t[0] != '#' {
			return true
		}
	}
	return false
}

// scanRecipes reads a make, just or task recipe file at the repo top.
func scanRecipes(s *refSink, data []byte) { s.scanRecipesIn(data, ".") }

// readRecipe reads the recipe file rel (repo-relative) once, whatever named it.
func (s *refSink) readRecipe(rel string) {
	rel = path.Clean(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || path.IsAbs(rel) {
		return
	}
	k := path.Dir(rel) + "\x00" + path.Base(rel)
	if s.recipeSeen[k] {
		return
	}
	s.recipeSeen[k] = true
	dir := path.Dir(rel)
	s.file(rel, func(s *refSink, data []byte) { s.scanRecipesIn(data, dir) })
}

// recipeInclude reads the file a recipe file (in dir) includes, imports or
// names: relative to dir, then to the repo top. A name with "*" or "?" is
// expanded over the folder's listing; one that is not a literal ("$", "(",
// "{{", a backtick) fails the scan.
func (s *refSink) recipeInclude(dir string, cands []string) {
	for _, c := range cands {
		if strings.ContainsAny(c, "$(`") || strings.Contains(c, "{{") || strings.HasPrefix(strings.Trim(c, "\"'"), "~") {
			s.fail(errScanCap)
			continue
		}
		if c = cleanRaw(c); c == "" {
			continue
		}
		if path.IsAbs(c) {
			s.recipeAbs(c, dir)
			continue
		}
		for _, d := range []string{dir, "."} {
			s.readRecipeName(path.Join(d, c))
			if dir == "." {
				break
			}
		}
	}
}

// recipeAbs reads a recipe file named by an absolute path: inside the repo as
// that repo path, outside it at its real path (readOutsideRecipe). A glob
// outside the repo fails the scan.
func (s *refSink) recipeAbs(c, dir string) {
	if rel := absInside(s.of, filepath.FromSlash(c)); rel != "" {
		s.readRecipeName(rel)
		return
	}
	if strings.ContainsAny(c, "*?[") {
		s.fail(errScanCap)
		return
	}
	s.readOutsideRecipe(filepath.FromSlash(c), dir)
}

// readOutsideRecipe reads a recipe file outside the repo (host-owned) once, by
// its path, and scans it as run from dir. A missing file or a folder is
// skipped; a file that is not regular, cannot be read or is over the size cap
// fails the scan; it counts against mergeRefFiles.
func (s *refSink) readOutsideRecipe(abs, dir string) {
	abs = filepath.Clean(abs)
	k := "\x00abs\x00" + abs
	if s.recipeSeen[k] {
		return
	}
	s.recipeSeen[k] = true
	if s.reads >= mergeRefFiles {
		s.fail(errScanCap)
		return
	}
	s.reads++ // a look-up counts, found or not
	// The type first: a device or FIFO is never opened.
	fi, err := os.Stat(abs)
	switch {
	case err != nil:
		if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			s.fail(fmt.Errorf("cannot read %s: %w", abs, err))
		}
		return
	case fi.IsDir():
		return
	case !fi.Mode().IsRegular():
		s.fail(fmt.Errorf("cannot read %s: not a regular file", abs))
		return
	case fi.Size() >= mergeImportBytes:
		s.fail(errScanCap)
		return
	}
	f, err := os.OpenFile(abs, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		s.fail(fmt.Errorf("cannot read %s: %w", abs, err))
		return
	}
	data, err := readRegular(f)
	if err != nil {
		s.fail(fmt.Errorf("cannot read %s: %w", abs, err))
		return
	}
	if len(data) >= mergeImportBytes {
		s.fail(errScanCap)
		return
	}
	s.scanRecipesIn(data, dir)
}

// readRecipeName reads rel, or every file a glob in its last element matches.
func (s *refSink) readRecipeName(rel string) {
	d, pat := path.Split(rel)
	d = path.Clean(d)
	if strings.ContainsAny(d, "*?[") {
		s.fail(errScanCap)
		return
	}
	if !strings.ContainsAny(pat, "*?[") {
		s.readRecipe(rel)
		return
	}
	pat = strings.ToLower(pat)
	if _, err := path.Match(pat, ""); err != nil {
		s.fail(errScanCap)
		return
	}
	s.pm.addGlob(d, pat) // a file the range adds that matches is refused too
	l := s.listing(d)
	for _, k := range l.sorted {
		if ok, _ := path.Match(pat, k); ok {
			name := path.Join(d, l.names[k])
			s.pathArg(name)
			s.readRecipe(name)
		}
	}
}

// scanRecipesIn reads a make, just or task recipe file (in repo folder dir)
// line by line (no parser): every line that is not empty or a comment is a
// command, after the recipe prefixes (@ - +), a "- " list marker and a leading
// "key:" are stripped, so the lines of a Makefile, a justfile and the cmds of a
// Taskfile all read as commands. The files make includes, just imports or mods
// and a Taskfile's includes name are paths and are read too (recipeInclude). A
// file with more than mergeRefLines lines fails the scan.
func (s *refSink) scanRecipesIn(data []byte, dir string) {
	lines := strings.Split(string(data), "\n")
	includesIndent := -1 // indent of a Taskfile "includes:" key, -1 outside it
	entryIndent := -1    // indent of its entries, -1 until the first
	for i, line := range lines {
		if i >= mergeRefLines {
			if moreLines(lines, i) {
				s.fail(errScanCap)
			}
			return
		}
		t := strings.TrimSpace(line)
		if t == "" || t[0] == '#' {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if includesIndent >= 0 && indent <= includesIndent {
			includesIndent, entryIndent = -1, -1
		}
		if t == "includes:" {
			includesIndent = indent
			continue
		}
		if v, ok := strings.CutPrefix(t, "includes:"); ok && flowYAML(v) {
			s.fail(errScanCap) // "includes: {a: ./a}": a flow map is not followed (#370)
			continue
		}
		if m := taskIncludeRE.FindStringSubmatch(line); m != nil {
			v := strings.TrimSpace(m[1])
			if flowYAML(v) {
				s.fail(errScanCap)
				continue
			}
			s.pathArg(v)
			s.recipeInclude(dir, []string{v, v + "/Taskfile.yml", v + "/Taskfile.yaml"})
		} else if includesIndent >= 0 {
			if entryIndent < 0 {
				entryIndent = indent
			}
			// An entry ("name: ./path" or "name: {taskfile: ...}"); the deeper
			// lines of a block entry are its keys (taskfile:, dir:, vars:).
			if m := taskEntryRE.FindStringSubmatch(line); m != nil && indent == entryIndent {
				v := strings.TrimSpace(m[1])
				if flowYAML(v) {
					s.fail(errScanCap) // "sub: {taskfile: ./sub}": not followed (#370)
					continue
				}
				s.pathArg(v)
				s.recipeInclude(dir, []string{v, v + "/Taskfile.yml", v + "/Taskfile.yaml"})
			}
		}
		for n := 0; n < 8; n++ { // a few prefixes: "- @cmd", "- key: cmd"
			switch {
			case strings.ContainsRune("@-+", rune(t[0])):
				t = strings.TrimSpace(t[1:])
			case recipeKeyRE.MatchString(t):
				t = strings.TrimSpace(t[strings.IndexByte(t, ':')+1:])
			default:
				n = 8
			}
			if t == "" {
				break
			}
		}
		if t == "" {
			continue
		}
		if m := recipeIncludeRE.FindStringSubmatch(t); m != nil {
			kw, args := strings.TrimSuffix(m[1], "?"), strings.Fields(m[2])
			if kw == "mod" {
				s.justMod(dir, args)
			} else {
				for _, a := range args {
					s.pathArg(a)
					s.recipeInclude(dir, []string{a})
				}
			}
		}
		s.command(t)
		if dir != "." { // an included file's recipes may run in its folder
			s.command("cd " + dir + " && " + t)
		}
	}
}

// justMod reads "mod NAME [PATH]": the module is PATH when given (a file, or a
// folder holding mod.just or a justfile), else NAME.just or a folder NAME with
// mod.just, justfile or .justfile (found case-insensitively). Every candidate
// is also denied, found or not.
func (s *refSink) justMod(dir string, args []string) {
	if len(args) == 0 {
		return
	}
	raw := args[0]
	if len(args) > 1 {
		raw = args[len(args)-1]
	}
	base := cleanRaw(raw)
	if base == "" || strings.ContainsAny(raw, "$*?[(`~") || strings.Contains(raw, "{{") {
		s.fail(errScanCap) // a module path the scan cannot name (#370 R9b)
		return
	}
	cands := []string{base + ".just", base + "/mod.just", base + "/justfile", base + "/.justfile"}
	if len(args) > 1 {
		cands = append([]string{base}, cands[1:]...)
	}
	for _, c := range cands {
		s.pathArg(c)
		if path.IsAbs(c) {
			s.recipeAbs(c, dir)
			continue
		}
		for _, d := range []string{dir, "."} {
			rel := path.Join(d, c)
			l := s.listing(path.Dir(rel))
			if name, ok := l.names[strings.ToLower(path.Base(rel))]; ok {
				s.readRecipe(path.Join(path.Dir(rel), name))
			}
			if dir == "." {
				break
			}
		}
	}
}
