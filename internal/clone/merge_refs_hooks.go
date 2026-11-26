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

// refYAMLFiles are the top-level files whose commands scanYAMLCommands reads.
var refYAMLFiles = []string{".pre-commit-config.yaml", ".overcommit.yml"}

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
	recipeIncludeRE = regexp.MustCompile(`^(include|-include|sinclude|import|mod)\s+(.*)$`)
)

// scanRecipes reads a make, just or task recipe file line by line (no parser):
// every line that is not empty or a comment is a command, after the recipe
// prefixes (@ - +), a "- " list marker and a leading "key:" are stripped, so the
// lines of a Makefile, a justfile and the cmds of a Taskfile all read as
// commands. The files make includes and just imports or mods are paths too.
func scanRecipes(s *refSink, data []byte) {
	for i, line := range strings.Split(string(data), "\n") {
		if i >= mergeRefLines {
			return
		}
		t := strings.TrimSpace(line)
		if t == "" || t[0] == '#' {
			continue
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
			for _, a := range strings.Fields(m[2]) {
				s.pathArg(a)
			}
		}
		s.command(t)
	}
}
