package clone

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// noSystemConfig points the system git config at a file that does not exist.
func noSystemConfig(t *testing.T) {
	t.Helper()
	old := mergeSystemGitConfig
	mergeSystemGitConfig = filepath.Join(t.TempDir(), "none")
	t.Cleanup(func() { mergeSystemGitConfig = old })
	t.Setenv("XDG_CONFIG_HOME", "")
}

// Git's own `config --list -z --show-origin --show-scope` output is what the
// reader parses: scope, origin and entry are separate NUL-terminated fields, an
// entry is key, "\n", value, and a key set with no value has no "\n".
func TestParseConfigListRealGit(t *testing.T) {
	noSystemConfig(t)
	x := newMergeFx(t, "main")
	cfg := filepath.Join(x.of, ".git", "config")
	f, err := os.OpenFile(cfg, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("[core]\n\tbareish\n\tsshCommand = ./x.sh --a=b\n[Filter \"X\"]\n\tClean = ./s/c.sh\n[include]\n\tpath = ../inc.cfg\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(x.of, "inc.cfg"), []byte("[user]\n\tname = inc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := NewExecGit()
	if err != nil {
		t.Skip("no git on PATH")
	}
	out, err := g.Run(context.Background(), x.of, noReplaceEnv(), hardened("config", "-z", "--show-origin", "--show-scope", "--list")...)
	if err != nil {
		t.Fatal(err)
	}
	es, err := parseConfigList(out, true)
	if err != nil {
		t.Fatal(err)
	}
	find := func(key string) *configEntry {
		for i := range es {
			if es[i].key == key {
				return &es[i]
			}
		}
		t.Fatalf("no entry %q in %+v", key, es)
		return nil
	}
	if e := find("core.sshcommand"); e.value != "./x.sh --a=b" || !e.hasValue || e.scope != "local" || e.origin != "file:.git/config" {
		t.Errorf("sshcommand entry = %+v", e)
	}
	if e := find("core.bareish"); e.hasValue || e.value != "" {
		t.Errorf("bare key entry = %+v", e)
	}
	if e := find("filter.X.clean"); e.value != "./s/c.sh" {
		t.Errorf("subsection case: %+v", e) // section and name fold, the subsection keeps its case
	}
	if e := find("include.path"); e.value != "../inc.cfg" {
		t.Errorf("include entry = %+v", e)
	}
	if e := find("user.name"); e.value != "inc" || !strings.HasPrefix(e.origin, "file:") || !strings.HasSuffix(e.origin, "inc.cfg") {
		t.Errorf("included entry = %+v", e)
	}
	var cmd int
	for _, e := range es {
		if e.scope == "command" {
			cmd++
		}
	}
	if cmd == 0 {
		t.Error("hardenFlags values are not listed with scope command")
	}
}

func TestParseConfigListMalformed(t *testing.T) {
	for _, tc := range []struct {
		out    string
		scoped bool
	}{
		{"local\x00file:x\x00a.b\nc", true},           // no final NUL
		{"local\x00file:x\x00", true},                 // a cut-off entry
		{"file:x\x00a.b\x00file:x\x00", false},        // cut-off
		{"local\x00file:x\x00\nvalue-only\x00", true}, // an empty key
	} {
		if _, err := parseConfigList([]byte(tc.out), tc.scoped); err == nil {
			t.Errorf("parseConfigList(%q) did not fail", tc.out)
		}
	}
	if es, err := parseConfigList(nil, true); err != nil || len(es) != 0 {
		t.Errorf("empty list = %v, %v", es, err)
	}
}

// The repo config reaches a work-tree file through an include, or runs a
// program from the tree: a box commit that changes that file or its folder is
// refused.
func TestMergeHomeGitConfigDenied(t *testing.T) {
	cases := []struct {
		name    string
		config  string            // appended to .git/config
		host    map[string]string // committed on the host before the box starts
		file    string            // what the box commits
		guarded bool
	}{
		{"include of a tracked file", "[include]\n\tpath = ../.gitconfig\n", map[string]string{".gitconfig": "[user]\n\tname = a\n"}, ".gitconfig", true},
		{"include of an untracked file", "[include]\n\tpath = ../local.cfg\n", nil, "local.cfg", true},
		{"includeIf to a file that does not exist yet", "[includeIf \"gitdir:/nowhere/\"]\n\tpath = ../later.cfg\n", nil, "later.cfg", true},
		{"include one level down", "[include]\n\tpath = ../cfg/a.inc\n", nil, "cfg/a.inc", true},
		{"include outside the work tree", "[include]\n\tpath = /elsewhere/x.cfg\n", nil, "x.cfg", false},
		{"include above the work tree", "[include]\n\tpath = ../../x.cfg\n", nil, "x.cfg", false},
		{"filter clean", "[filter \"x\"]\n\tclean = ./scripts/clean.sh\n", nil, "scripts/clean.sh", true},
		{"filter clean, a neighbour", "[filter \"x\"]\n\tclean = ./scripts/clean.sh\n", nil, "scripts/other.sh", true},
		{"filter smudge", "[filter \"x\"]\n\tsmudge = scripts/s.sh %f\n", nil, "scripts/s.sh", true},
		{"filter process", "[filter \"x\"]\n\tprocess = tools/lfs.py\n", nil, "tools/lfs.py", true},
		{"diff textconv", "[diff \"x\"]\n\ttextconv = tools/tc.sh\n", nil, "tools/tc.sh", true},
		{"diff command", "[diff \"x\"]\n\tcommand = tools/d.sh\n", nil, "tools/d.sh", true},
		{"diff external", "[diff]\n\texternal = bin/ext\n", nil, "bin/ext", true},
		{"merge driver", "[merge \"x\"]\n\tdriver = tools/m.sh %O %A %B\n", nil, "tools/m.sh", true},
		{"core sshCommand", "[core]\n\tsshCommand = ssh -F cfg/ssh_config\n", nil, "cfg/ssh_config", true},
		{"core pager", "[core]\n\tpager = ./bin/pg\n", nil, "bin/pg", true},
		{"core editor", "[core]\n\teditor = ./bin/ed\n", nil, "bin/ed", true},
		{"core askpass", "[core]\n\taskpass = ./bin/ask\n", nil, "bin/ask", true},
		{"core fsmonitor", "[core]\n\tfsmonitor = ./hk/fsm\n", nil, "hk/fsm", true},
		{"sequence editor", "[sequence]\n\teditor = ./bin/seq\n", nil, "bin/seq", true},
		{"pager.log", "[pager]\n\tlog = ./bin/lp\n", nil, "bin/lp", true},
		{"interactive diffFilter", "[interactive]\n\tdiffFilter = ./bin/df\n", nil, "bin/df", true},
		{"gpg program", "[gpg]\n\tprogram = ./bin/gpg\n", nil, "bin/gpg", true},
		{"gpg ssh program", "[gpg \"ssh\"]\n\tprogram = ./bin/sshsign\n", nil, "bin/sshsign", true},
		{"credential helper", "[credential]\n\thelper = !./bin/cred\n", nil, "bin/cred", true},
		{"credential url helper", "[credential \"https://h.example\"]\n\thelper = /ABS/bin/cred2\n", nil, "bin/cred2", true},
		{"shell alias", "[alias]\n\tst = !./bin/st.sh\n", nil, "bin/st.sh", true},
		{"gpg ssh defaultKeyCommand", "[gpg \"ssh\"]\n\tdefaultKeyCommand = ./bin/keycmd\n", nil, "bin/keycmd", true},
		{"core gitProxy", "[core]\n\tgitProxy = ./bin/proxy for example.com\n", nil, "bin/proxy", true},
		{"difftool cmd", "[difftool \"x\"]\n\tcmd = ./bin/dt \"$LOCAL\" \"$REMOTE\"\n", nil, "bin/dt", true},
		{"mergetool cmd", "[mergetool \"x\"]\n\tcmd = ./bin/mt \"$MERGED\"\n", nil, "bin/mt", true},
		{"core attributesFile, relative", "[core]\n\tattributesFile = tools/attrs\n", nil, "tools/attrs", true},
		{"core attributesFile, absolute in the tree", "[core]\n\tattributesFile = /ABS/cfg/attrs\n", nil, "cfg/attrs", true},
		{"core attributesFile folds case", "[Core]\n\tAttributesFile = Tools/Attrs\n", nil, "tools/attrs", true},
		{"core attributesFile outside the tree", "[core]\n\tattributesFile = /elsewhere/attrs\n", nil, "elsewhere/attrs", false},
		{"core attributesFile escaping the tree", "[core]\n\tattributesFile = ../attrs\n", nil, "attrs", false},
		{"keys fold case", "[Filter \"X\"]\n\tClean = ./scripts/clean.sh\n", nil, "scripts/clean.sh", true},
		{"path case folds", "[filter \"x\"]\n\tclean = ./Scripts/Clean.sh\n", nil, "scripts/clean.sh", true},
		{"textconv plain word", "[diff \"x\"]\n\ttextconv = node conv\n", map[string]string{"conv.js": "h\n"}, "conv.js", true},
		{"filter plain word, a folder", "[filter \"x\"]\n\tclean = node cleaner\n", map[string]string{"cleaner/index.js": "h\n"}, "cleaner/index.js", true},
		{"filter python -mmodule", "[filter \"x\"]\n\tclean = python -mcleaner2\n", nil, "cleaner2/__main__.py", true},
		{"pager's own config at the top", "[core]\n\tpager = mypager\n", map[string]string{".mypagerrc": "h\n"}, ".mypagerrc", true},
		{"textconv tool prefix rule", "[diff \"x\"]\n\ttextconv = npx eslint\n", nil, "eslint.config.js", true},
		{"textconv plain word, README still merges", "[diff \"x\"]\n\ttextconv = node conv\n", map[string]string{"conv.js": "h\n"}, "README.md", false},
		{"textconv plain word, nothing there", "[diff \"x\"]\n\ttextconv = node nothere\n", nil, "nothere.js", true},
		{"textconv runner manifest is not added", "[diff \"x\"]\n\ttextconv = node conv\n", nil, "package.json", false},
		// What is not a program or an include is not denied.
		{"alias that is not a shell command", "[alias]\n\tst = status ./bin/st.sh\n", nil, "bin/st.sh", false},
		{"unrelated key naming a path", "[core]\n\texcludesFile = tools/ignore\n[user]\n\tsigningkey = keys/me.pub\n", nil, "tools/ignore", false},
		{"fsmonitor as a boolean", "[core]\n\tfsmonitor = true\n", nil, "true", false},
		{"program outside the work tree", "[filter \"x\"]\n\tclean = /usr/bin/cat\n", nil, "usr/bin/cat", false},
		{"program that escapes the work tree", "[filter \"x\"]\n\tclean = ../tools/clean.sh\n", nil, "tools/clean.sh", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			noSystemConfig(t)
			x := newMergeFx(t, "main")
			if c.host != nil {
				x.commit("host", c.host)
			}
			cfg := strings.ReplaceAll(c.config, "/ABS/", x.of+"/")
			f, err := os.OpenFile(filepath.Join(x.of, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.WriteString(cfg); err != nil {
				t.Fatal(err)
			}
			f.Close()
			before := x.tip()
			x.box("main", map[string]string{c.file: "changed\n"})
			a := &auditRec{}
			got := x.run(newRec(t), a)
			if !c.guarded {
				wantMerged(t, got)
				return
			}
			wantMergeReason(t, got, MergeGuarded)
			if !slices.Contains(got.Paths, c.file) {
				t.Fatalf("Paths = %q, want %q", got.Paths, c.file)
			}
			if x.tip() != before || len(a.calls) != 0 {
				t.Fatalf("main moved or audit written: %q", a.calls)
			}
		})
	}
}

// A symlink at a parent of a program re-points it, as for the hooks dir.
func TestMergeHomeGitConfigProgramParentSymlinkDenied(t *testing.T) {
	noSystemConfig(t)
	x := newMergeFx(t, "main")
	fixtureGit(t, x.of, "config", "filter.x.clean", "./tools/sub/clean.sh")
	fixtureGit(t, x.of, "checkout", "-q", "-b", "side")
	x.write("evil/sub/clean.sh", "#!/bin/sh\n")
	if err := os.Symlink("evil", filepath.Join(x.of, "tools")); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, x.of, "add", "evil", "tools")
	fixtureGit(t, x.of, "commit", "-q", "-m", "link")
	fixtureGit(t, x.of, "update-ref", "refs/heads/box/main", x.out("rev-parse", "HEAD"))
	fixtureGit(t, x.of, "checkout", "-q", "main")
	got := x.run(newRec(t), &auditRec{})
	wantMergeReason(t, got, MergeGuarded)
	if !slices.Contains(got.Paths, "tools") {
		t.Fatalf("Paths = %q, want tools", got.Paths)
	}
}

// The user's global and system config, and what they include, count the same.
func TestMergeHomeGlobalGitConfigDenied(t *testing.T) {
	put := func(t *testing.T, p, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name    string
		setup   func(t *testing.T, home, of string)
		file    string
		guarded bool
	}{
		{"global includes an absolute work-tree file", func(t *testing.T, home, of string) {
			put(t, filepath.Join(home, ".gitconfig"), "[include]\n\tpath = "+of+"/shared.cfg\n")
		}, "shared.cfg", true},
		{"global includes a work-tree file that is there", func(t *testing.T, home, of string) {
			put(t, filepath.Join(of, "shared.cfg"), "[user]\n\tname = s\n")
			put(t, filepath.Join(home, ".gitconfig"), "[include]\n\tpath = "+of+"/shared.cfg\n")
		}, "shared.cfg", true},
		{"global includeIf of a work-tree file", func(t *testing.T, home, of string) {
			put(t, filepath.Join(home, ".gitconfig"), "[includeIf \"gitdir:~/nowhere/\"]\n\tpath = "+of+"/work.cfg\n")
		}, "work.cfg", true},
		{"global includes a file under home that includes a work-tree file", func(t *testing.T, home, of string) {
			put(t, filepath.Join(home, ".gitconfig"), "[include]\n\tpath = ~/.gitconfig.local\n")
			put(t, filepath.Join(home, ".gitconfig.local"), "[include]\n\tpath = "+of+"/deep.cfg\n")
		}, "deep.cfg", true},
		{"global program in the work tree", func(t *testing.T, home, of string) {
			put(t, filepath.Join(home, ".gitconfig"), "[filter \"x\"]\n\tclean = "+of+"/bin/clean\n")
		}, "bin/clean", true},
		{"global attributesFile in the work tree", func(t *testing.T, home, of string) {
			put(t, filepath.Join(home, ".gitconfig"), "[core]\n\tattributesFile = "+of+"/ga/attrs\n")
		}, "ga/attrs", true},
		{"global attributesFile under home", func(t *testing.T, home, of string) {
			put(t, filepath.Join(home, ".gitconfig"), "[core]\n\tattributesFile = ~/.gitattributes\n")
		}, "gitattributes", false},
		{"global program by a relative path", func(t *testing.T, home, of string) {
			put(t, filepath.Join(home, ".config", "git", "config"), "[core]\n\tpager = tools/pager.sh\n")
		}, "tools/pager.sh", true},
		{"system include", func(t *testing.T, home, of string) {
			p := filepath.Join(t.TempDir(), "gitconfig")
			put(t, p, "[include]\n\tpath = "+of+"/sys.cfg\n")
			old := mergeSystemGitConfig
			mergeSystemGitConfig = p
			t.Cleanup(func() { mergeSystemGitConfig = old })
		}, "sys.cfg", true},
		{"global include outside the work tree", func(t *testing.T, home, of string) {
			put(t, filepath.Join(home, ".gitconfig"), "[include]\n\tpath = ~/.gitconfig.local\n")
			put(t, filepath.Join(home, ".gitconfig.local"), "[user]\n\tname = u\n")
		}, "gitconfig.local", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			noSystemConfig(t)
			home := t.TempDir()
			x := newMergeFx(t, "main")
			c.setup(t, home, x.of)
			x.box("main", map[string]string{c.file: "changed\n"})
			got := MergeHome(context.Background(), newRec(t), MergeRequest{Of: x.of, Home: home, Audit: (&auditRec{}).fn, CloneTips: x.tips()})
			if !c.guarded {
				wantMerged(t, got)
				return
			}
			wantMergeReason(t, got, MergeGuarded)
			if !slices.Contains(got.Paths, c.file) {
				t.Fatalf("Paths = %q, want %q", got.Paths, c.file)
			}
		})
	}
}

// A repo config that includes a ~ path reads it from the user's home, not from
// the clean env's.
func TestMergeHomeRepoConfigIncludesFromHome(t *testing.T) {
	noSystemConfig(t)
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".hk.cfg"), []byte("[core]\n\thooksPath = scripts/hooks\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	x := newMergeFx(t, "main")
	fixtureGit(t, x.of, "config", "include.path", "~/.hk.cfg")
	x.box("main", map[string]string{"scripts/hooks/pre-commit": "#!/bin/sh\n"})
	got := MergeHome(context.Background(), newRec(t), MergeRequest{Of: x.of, Home: home, Audit: (&auditRec{}).fn, CloneTips: x.tips()})
	wantMergeReason(t, got, MergeGuarded)
}

// failConfigGit fails every `config` call, as an unreadable config would.
type failConfigGit struct{ *recGit }

func (f failConfigGit) Run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	if slices.Contains(args, "config") {
		return nil, errors.New("git -c: exit status 128: bad config line 3")
	}
	return f.recGit.Run(ctx, dir, env, args...)
}

func TestMergeHomeGitConfigErrorFailsTheMerge(t *testing.T) {
	noSystemConfig(t)
	x := newMergeFx(t, "main")
	before := x.tip()
	x.box("main", map[string]string{"b.txt": "b"})
	a := &auditRec{}
	got := x.run(failConfigGit{newRec(t)}, a)
	wantMergeReason(t, got, MergeFailed)
	if !strings.HasPrefix(got.Detail, "cannot read git config") {
		t.Fatalf("Detail = %q", got.Detail)
	}
	if x.tip() != before || len(a.calls) != 0 {
		t.Fatal("merged or audited despite a config read failure")
	}
	// The same for a broken repo config, with real git.
	y := newMergeFx(t, "main")
	y.box("main", map[string]string{"b.txt": "b"})
	tips := y.tips()
	f, err := os.OpenFile(filepath.Join(y.of, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("[core\n")
	f.Close()
	wantMergeReason(t, MergeHome(context.Background(), newRec(t), MergeRequest{Of: y.of, Audit: (&auditRec{}).fn, CloneTips: tips}), MergeFailed)
}

// The reader costs one git call for the repo and one per global or system file
// that exists: the same as the core.hooksPath read it replaced.
func TestMergeHomeGitConfigCallCount(t *testing.T) {
	count := func(r *recGit) (n int) {
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, c := range r.calls {
			if slices.Contains(c.args, "config") {
				n++
			}
		}
		return n
	}
	noSystemConfig(t)
	x := newMergeFx(t, "main")
	x.box("main", map[string]string{"b.txt": "b"})
	rec := newRec(t)
	wantMerged(t, x.run(rec, &auditRec{}))
	if n := count(rec); n != 1 {
		t.Errorf("no global or system file: %d config calls, want 1", n)
	}

	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[user]\n\tname = u\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sys := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(sys, []byte("[user]\n\temail = u@x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mergeSystemGitConfig = sys
	y := newMergeFx(t, "main")
	y.box("main", map[string]string{"b.txt": "b"})
	rec = newRec(t)
	wantMerged(t, MergeHome(context.Background(), rec, MergeRequest{Of: y.of, Home: home, Audit: (&auditRec{}).fn, CloneTips: y.tips()}))
	if n := count(rec); n != 3 {
		t.Errorf("repo, ~/.gitconfig and system: %d config calls, want 3", n)
	}
}

// A git config with more programs than the scan keeps ends the merge failed.
func TestMergeHomeGitConfigProgramCapFailsClosed(t *testing.T) {
	noSystemConfig(t)
	noManagedSettings(t)
	x := newMergeFx(t, "main")
	before := x.tip()
	x.box("main", map[string]string{"b.txt": "b"})
	var b strings.Builder
	b.WriteString("[alias]\n")
	for i := 0; i < 2*mergeRefPaths; i++ {
		b.WriteString("\ta" + strconv.Itoa(i) + " = !./d" + strconv.Itoa(i) + "/x.sh\n")
	}
	f, err := os.OpenFile(filepath.Join(x.of, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(b.String())
	f.Close()
	a := &auditRec{}
	got := x.run(newRec(t), a)
	wantMergeReason(t, got, MergeFailed)
	if !strings.Contains(got.Detail, "too many paths to check") {
		t.Fatalf("Detail = %q", got.Detail)
	}
	if len(a.calls) != 0 || x.tip() != before {
		t.Fatalf("audited or merged: %q", a.calls)
	}
}
