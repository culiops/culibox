package clone

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// What the user's git reads from its config and then does in the host repo
// (#370). A box commit that changes a work-tree file the config includes, or a
// program the config runs from the work tree, changes what the host's git does
// at the next commit, status or checkout, so the merge refuses it as it refuses
// a hook.

// configEntry is one line of `git config --list -z --show-origin`.
type configEntry struct {
	scope    string // "" when the call did not ask for scopes
	origin   string // "file:<path>", "command line:", "blob:<id>", ...
	key      string // section and variable name lower-cased, as git prints it
	value    string
	hasValue bool // false for a key set with no "=", which git reads as true
}

// parseConfigList reads `config -z --show-origin [--show-scope] --list` output
// (measured, git 2.39.5): per entry, the scope when asked for, the origin, then
// "key\nvalue", each NUL-terminated; a key with no value has no "\n". Anything
// else is an error, so a cut-off output never reads as a shorter list.
func parseConfigList(out []byte, scoped bool) ([]configEntry, error) {
	if len(out) == 0 {
		return nil, nil
	}
	if out[len(out)-1] != 0 {
		return nil, errors.New("the config list is cut short")
	}
	toks := strings.Split(string(out[:len(out)-1]), "\x00")
	per := 2
	if scoped {
		per = 3
	}
	if len(toks)%per != 0 {
		return nil, errors.New("the config list is cut short")
	}
	var es []configEntry
	for i := 0; i < len(toks); i += per {
		var e configEntry
		kv := toks[i+per-1]
		if scoped {
			e.scope = toks[i]
		}
		e.origin = toks[i+per-2]
		if j := strings.IndexByte(kv, '\n'); j >= 0 {
			e.key, e.value, e.hasValue = kv[:j], kv[j+1:], true
		} else {
			e.key = kv
		}
		if e.key == "" {
			return nil, errors.New("the config list has an entry with no key")
		}
		es = append(es, e)
	}
	return es, nil
}

// gitConfigScan is what the user's git config says that the merge must keep a
// box from changing.
type gitConfigScan struct {
	hooks  []string // repo-relative core.hooksPath directories, as spelled
	denied []string // repo-relative files and folders, as importDenied reads them
}

// gitConfig reads the config the user's git reads for clone_of, one git call
// for the repo and one per global or system file that exists.
//
// The repo's own values cannot be read through a key lookup: hardenFlags put
// core.hooksPath and core.fsmonitor on the command line, which a lookup would
// print (measured, git 2.39.5), so the list is read with scopes and every entry
// from scope "command" is dropped. CleanEnv hides the global and system config,
// which the user's git reads: each file is read on its own with --file (which
// leaves out the command line), with HOME set to home so a ~ include resolves
// as for the user. The repo call has the same HOME, with the global config
// still off. home == "" skips the global files and the ~ includes. Every value
// found counts, whatever its precedence.
//
// In the list git has already followed the includes, and it still prints each
// include.path / includeIf.*.path entry with the file that holds it, so the
// targets are read from the same call, including those of an includeIf that
// does not match now and a target that does not exist yet.
func (m *merger) gitConfig(home string) (*gitConfigScan, error) {
	sc := &gitConfigScan{}
	var hookVals []string

	repoEnv := m.env()
	if home != "" {
		repoEnv = withHome(repoEnv, home)
	}
	out, err := m.run(repoEnv, "config", "-z", "--show-origin", "--show-scope", "--list")
	if err != nil {
		return nil, err
	}
	es, err := parseConfigList(out, true)
	if err != nil {
		return nil, err
	}
	es = slices.DeleteFunc(es, func(e configEntry) bool { return e.scope == "command" })
	hookVals = sc.scan(m.of, home, es, hookVals)

	var files []string
	env := m.env()
	if home != "" {
		files = append(files, filepath.Join(home, ".gitconfig"))
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			files = append(files, filepath.Join(xdg, "git", "config"))
		} else {
			files = append(files, filepath.Join(home, ".config", "git", "config"))
		}
		env = append(globalEnv(home), "GIT_NO_REPLACE_OBJECTS=1")
	}
	files = append(files, mergeSystemGitConfig)
	for _, f := range files {
		if _, err := os.Stat(f); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		out, err := m.run(env, "config", "-z", "--includes", "--file", f, "--show-origin", "--list")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		es, err := parseConfigList(out, false)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		hookVals = sc.scan(m.of, home, es, hookVals)
	}
	for _, v := range hookVals {
		if d := hooksDirOf(m.of, home, v); d != "" && !slices.Contains(sc.hooks, d) {
			sc.hooks = append(sc.hooks, d)
		}
	}
	return sc, nil
}

// withHome is env with HOME set to home.
func withHome(env []string, home string) []string {
	out := slices.DeleteFunc(slices.Clone(env), func(kv string) bool { return strings.HasPrefix(kv, "HOME=") })
	return append(out, "HOME="+home)
}

// scan adds what the entries deny to sc.denied and returns hookVals with every
// core.hooksPath value among them.
func (sc *gitConfigScan) scan(of, home string, es []configEntry, hookVals []string) []string {
	var raws []string // command spellings, resolved against the repo top
	deny := func(rel string) {
		if rel != "" && rel != "." && !slices.Contains(sc.denied, rel) {
			sc.denied = append(sc.denied, rel)
		}
	}
	for _, e := range es {
		holder := ""
		if p, ok := strings.CutPrefix(e.origin, "file:"); ok {
			if !filepath.IsAbs(p) {
				p = filepath.Join(of, p) // git ran in of
			}
			holder = filepath.Clean(p)
			deny(absInside(of, holder))
		}
		switch {
		case strings.EqualFold(e.key, "core.hookspath"):
			if e.hasValue {
				hookVals = append(hookVals, e.value)
			}
		case isIncludeKey(e.key):
			if holder == "" || !e.hasValue {
				continue
			}
			if t := includeTarget(home, holder, e.value); t != "" {
				deny(absInside(of, t))
			}
		case isProgramKey(e):
			paths, _ := commandPaths(e.value)
			raws = append(raws, paths...)
		}
	}
	for _, rel := range repoDenyPaths(of, raws) {
		deny(rel)
	}
	return hookVals
}

// isIncludeKey reports whether key (as git prints it) is include.path or
// includeIf.<condition>.path.
func isIncludeKey(key string) bool {
	k := strings.ToLower(key)
	return k == "include.path" || (strings.HasPrefix(k, "includeif.") && strings.HasSuffix(k, ".path"))
}

// includeTarget is the absolute path an include.path value names: "~/" is
// under home, any other relative path is relative to the directory of the file
// that holds it. "" when it names nothing this check can resolve.
func includeTarget(home, holder, v string) string {
	switch {
	case v == "":
		return ""
	case v == "~" || strings.HasPrefix(v, "~/"):
		if home == "" {
			return ""
		}
		return filepath.Join(home, v[1:])
	case strings.HasPrefix(v, "~"):
		return "" // another user's home
	case filepath.IsAbs(v):
		return filepath.Clean(v)
	}
	return filepath.Join(filepath.Dir(holder), v)
}

// isProgramKey reports whether the entry sets a command git runs: a filter,
// diff or merge driver, a pager or editor, a credential or gpg helper, a shell
// alias, an ssh command or an fsmonitor hook.
func isProgramKey(e configEntry) bool {
	if !e.hasValue || e.value == "" {
		return false
	}
	first := strings.IndexByte(e.key, '.')
	last := strings.LastIndexByte(e.key, '.')
	if first < 0 {
		return false
	}
	k := strings.ToLower(e.key[:first]) // the section
	name := strings.ToLower(e.key[last+1:])
	switch k {
	case "core":
		switch name {
		case "sshcommand", "pager", "editor", "askpass":
			return first == last
		case "fsmonitor":
			return first == last && !isConfigBool(e.value)
		}
	case "sequence":
		return first == last && name == "editor"
	case "diff":
		return (first == last && name == "external") || (first != last && (name == "textconv" || name == "command"))
	case "pager":
		return true
	case "interactive":
		return first == last && name == "difffilter"
	case "filter":
		return first != last && (name == "clean" || name == "smudge" || name == "process")
	case "merge":
		return first != last && name == "driver"
	case "gpg":
		return name == "program"
	case "credential":
		return name == "helper"
	case "alias":
		return strings.HasPrefix(e.value, "!")
	}
	return false
}

// isConfigBool reports whether v is one of git's spellings of true or false.
func isConfigBool(v string) bool {
	switch strings.ToLower(v) {
	case "true", "false", "yes", "no", "on", "off", "1", "0":
		return true
	}
	return false
}
