package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/box"
)

func TestParseImports(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"line starts", "@~/.claude/context/projects.md\n@infra.md", []string{"~/.claude/context/projects.md", "infra.md"}},
		{"token runs to whitespace", "see @docs/a.md, then", []string{"docs/a.md,"}},
		{"inline code span", "add one `@~/.claude/context/<name>.md` line", []string{}},
		{"double-backtick span", "x ``a ` @b.md`` y", []string{}},
		{"unmatched backtick is literal", "x ` @b.md", []string{"b.md"}},
		{"backtick fence", "```\n@x.md\n```\n@y.md", []string{"y.md"}},
		{"tilde fence", "~~~\n@x.md\n~~~\n@y.md", []string{"y.md"}},
		{"fence info string", "```md\n@x.md\n```\n@y.md", []string{"y.md"}},
		{"longer fence not closed by shorter", "````\n```\n@x.md\n````\n@y.md", []string{"y.md"}},
		{"unclosed fence swallows rest", "```\n@x.md", []string{}},
		{"html comment inline", "<!-- @hidden.md -->\n@shown.md", []string{"shown.md"}},
		{"html comment multi-line", "<!--\n@hidden.md\n-->\n@shown.md", []string{"shown.md"}},
		{"text after comment close", "<!-- x\n--> @shown.md", []string{"shown.md"}},
		{"email address", "mail admin@culiops.dev", []string{}},
		{"escaped space", "@Design\\ Docs/api.md", []string{"Design Docs/api.md"}},
		{"double-quoted", "@\"quoted.md\"", []string{}},
		{"single-quoted", "@'quoted.md'", []string{}},
		{"bare at", "@", []string{}},
		{"bare at mid-line", "a @ b", []string{}},
		{"two on a line", "@a.md @b.md", []string{"a.md", "b.md"}},
		{"indented", "  @a.md", []string{"a.md"}},
		{"empty", "", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseImports([]byte(tc.in))
			if got == nil {
				got = []string{}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseImports(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// impFixture lays out <base>/home/.claude under base and returns home and the
// config dir, the way a resolveImports caller sees them.
func impFixture(t *testing.T, base string) (home, src string) {
	t.Helper()
	home = filepath.Join(base, "home")
	src = filepath.Join(home, ".claude")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	return home, src
}

func impWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func impSum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func impBoxes(s ImportSet) []string {
	out := []string{}
	for _, f := range s.Files {
		out = append(out, f.Box)
	}
	return out
}

func impRefusal(s ImportSet, imp string) string {
	for _, r := range s.Refused {
		if r.Import == imp {
			return r.Reason
		}
	}
	return ""
}

func TestResolveImportsMapsThreeForms(t *testing.T) {
	tmp := t.TempDir()
	home, src := impFixture(t, tmp)
	abs := filepath.Join(home, "notes", "c.md")
	impWrite(t, filepath.Join(src, "CLAUDE.md"), "@~/.claude/context/a.md\n@rel/b.md\n@"+abs+"\n")
	impWrite(t, filepath.Join(src, "context", "a.md"), "A")
	impWrite(t, filepath.Join(src, "rel", "b.md"), "BB")
	impWrite(t, abs, "CCC")

	s := resolveImports(importOpts{Source: src, HostHome: home})
	want := []string{"/home/agent/.claude/context/a.md", "/home/agent/.claude/rel/b.md", abs}
	if got := impBoxes(s); !reflect.DeepEqual(got, want) {
		t.Fatalf("boxes = %v, want %v (refused %v)", got, want, s.Refused)
	}
	for i, c := range []string{"A", "BB", "CCC"} {
		f := s.Files[i]
		if f.SHA256 != impSum(c) || f.Size != int64(len(c)) || string(f.content) != c {
			t.Errorf("%s: sha %s size %d content %q, want content %q", f.Box, f.SHA256, f.Size, f.content, c)
		}
		if f.Name != filepath.Base(f.Box) {
			t.Errorf("%s: name %q", f.Box, f.Name)
		}
	}
	if s.Files[0].Host != filepath.Join(src, "context", "a.md") || s.Files[2].Host != abs {
		t.Errorf("hosts = %q, %q", s.Files[0].Host, s.Files[2].Host)
	}
	if s.Files[1].Import != "rel/b.md" {
		t.Errorf("import = %q", s.Files[1].Import)
	}
	if len(s.Refused) != 0 {
		t.Errorf("refused = %v", s.Refused)
	}
}

func TestResolveImportsNestedFourHops(t *testing.T) {
	tmp := t.TempDir()
	home, src := impFixture(t, tmp)
	impWrite(t, filepath.Join(src, "CLAUDE.md"), "@~/x/a1.md\n")
	for i := 1; i <= 5; i++ {
		impWrite(t, filepath.Join(home, "x", fmt.Sprintf("a%d.md", i)), fmt.Sprintf("@a%d.md\n", i+1))
	}
	s := resolveImports(importOpts{Source: src, HostHome: home})
	want := []string{"/home/agent/x/a1.md", "/home/agent/x/a2.md", "/home/agent/x/a3.md", "/home/agent/x/a4.md"}
	if got := impBoxes(s); !reflect.DeepEqual(got, want) {
		t.Fatalf("boxes = %v, want %v (refused %v)", got, want, s.Refused)
	}
	if s.Files[1].Host != filepath.Join(home, "x", "a2.md") {
		t.Errorf("a2 host = %q", s.Files[1].Host)
	}
}

func TestResolveImportsCycleAndDuplicate(t *testing.T) {
	tmp := t.TempDir()
	home, src := impFixture(t, tmp)
	impWrite(t, filepath.Join(src, "CLAUDE.md"), "@a.md\n@a.md\n")
	impWrite(t, filepath.Join(src, "a.md"), "@b.md\n")
	impWrite(t, filepath.Join(src, "b.md"), "@a.md\n@b.md\n")

	done := make(chan ImportSet, 1)
	go func() { done <- resolveImports(importOpts{Source: src, HostHome: home}) }()
	var s ImportSet
	select {
	case s = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("resolveImports did not terminate on a cycle")
	}
	want := []string{"/home/agent/.claude/a.md", "/home/agent/.claude/b.md"}
	if got := impBoxes(s); !reflect.DeepEqual(got, want) || len(s.Refused) != 0 {
		t.Fatalf("boxes = %v refused %v, want %v", got, s.Refused, want)
	}
}

func TestResolveImportsMissingIsSilent(t *testing.T) {
	tmp := t.TempDir()
	home, src := impFixture(t, tmp)
	impWrite(t, filepath.Join(src, "CLAUDE.md"), "@nope.md and @name and @gone/dir/x.md\n")
	s := resolveImports(importOpts{Source: src, HostHome: home})
	if len(s.Files) != 0 || len(s.Refused) != 0 {
		t.Fatalf("got %+v", s)
	}
}

func TestResolveImportsSymlinkEscapeRefused(t *testing.T) {
	tmp := t.TempDir()
	home, src := impFixture(t, tmp)
	impWrite(t, filepath.Join(home, ".ssh", "id_rsa"), "KEY")
	impWrite(t, filepath.Join(src, "CLAUDE.md"), "@context/x.md\n")
	if err := os.MkdirAll(filepath.Join(src, "context"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, ".ssh", "id_rsa"), filepath.Join(src, "context", "x.md")); err != nil {
		t.Fatal(err)
	}
	s := resolveImports(importOpts{Source: src, HostHome: home})
	if len(s.Files) != 0 || impRefusal(s, "context/x.md") != ReasonImportSymlinkEscape {
		t.Fatalf("got %+v", s)
	}
}

func TestResolveImportsSymlinkInsideFolderFollowed(t *testing.T) {
	t.Run("leaf link inside its folder", func(t *testing.T) {
		tmp := t.TempDir()
		home, src := impFixture(t, tmp)
		impWrite(t, filepath.Join(src, "CLAUDE.md"), "@context/x.md\n")
		impWrite(t, filepath.Join(src, "context", "real", "x.md"), "REAL")
		if err := os.Symlink(filepath.Join("real", "x.md"), filepath.Join(src, "context", "x.md")); err != nil {
			t.Fatal(err)
		}
		s := resolveImports(importOpts{Source: src, HostHome: home})
		if got := impBoxes(s); !reflect.DeepEqual(got, []string{"/home/agent/.claude/context/x.md"}) {
			t.Fatalf("boxes = %v refused %v", got, s.Refused)
		}
		if s.Files[0].SHA256 != impSum("REAL") {
			t.Errorf("sha = %s", s.Files[0].SHA256)
		}
	})
	t.Run("directory link followed", func(t *testing.T) {
		tmp := t.TempDir()
		home, src := impFixture(t, tmp)
		impWrite(t, filepath.Join(src, "CLAUDE.md"), "@~/.claude/context/a.md\n")
		impWrite(t, filepath.Join(tmp, "dotfiles", "context", "a.md"), "DOT")
		if err := os.Symlink(filepath.Join(tmp, "dotfiles", "context"), filepath.Join(src, "context")); err != nil {
			t.Fatal(err)
		}
		s := resolveImports(importOpts{Source: src, HostHome: home})
		if got := impBoxes(s); !reflect.DeepEqual(got, []string{"/home/agent/.claude/context/a.md"}) {
			t.Fatalf("boxes = %v refused %v", got, s.Refused)
		}
		if s.Files[0].SHA256 != impSum("DOT") {
			t.Errorf("sha = %s", s.Files[0].SHA256)
		}
	})
}

func TestResolveImportsNonRegularRefused(t *testing.T) {
	// A short base: a unix socket path must fit sun_path (~107 bytes), which a
	// long t.TempDir under GOTMPDIR can exceed.
	base, err := os.MkdirTemp("", "imp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	home, src := impFixture(t, base)
	impWrite(t, filepath.Join(src, "CLAUDE.md"), "@fifo.md\n@s.sock\n@dir.md\n")
	if err := syscall.Mkfifo(filepath.Join(src, "fifo.md"), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", filepath.Join(src, "s.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := os.Mkdir(filepath.Join(src, "dir.md"), 0o755); err != nil {
		t.Fatal(err)
	}

	done := make(chan ImportSet, 1)
	go func() { done <- resolveImports(importOpts{Source: src, HostHome: home}) }()
	var s ImportSet
	select {
	case s = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("resolveImports blocked on a non-regular file")
	}
	if len(s.Files) != 0 {
		t.Fatalf("files = %v", impBoxes(s))
	}
	for _, imp := range []string{"fifo.md", "s.sock", "dir.md"} {
		if got := impRefusal(s, imp); got != ReasonImportNotRegular {
			t.Errorf("%s: reason %q", imp, got)
		}
	}
}

func TestResolveImportsCaps(t *testing.T) {
	t.Run("file too large", func(t *testing.T) {
		tmp := t.TempDir()
		home, src := impFixture(t, tmp)
		impWrite(t, filepath.Join(src, "CLAUDE.md"), "@big.md\n@ok.md\n")
		impWrite(t, filepath.Join(src, "big.md"), strings.Repeat("x", maxImportBytes+1))
		impWrite(t, filepath.Join(src, "ok.md"), "ok")
		s := resolveImports(importOpts{Source: src, HostHome: home})
		if impRefusal(s, "big.md") != ReasonImportTooLarge {
			t.Fatalf("refused = %v", s.Refused)
		}
		if got := impBoxes(s); !reflect.DeepEqual(got, []string{"/home/agent/.claude/ok.md"}) {
			t.Errorf("boxes = %v", got)
		}
	})
	t.Run("too many files", func(t *testing.T) {
		tmp := t.TempDir()
		home, src := impFixture(t, tmp)
		var md strings.Builder
		for i := 0; i < maxImportFiles+1; i++ {
			name := fmt.Sprintf("f%02d.md", i)
			fmt.Fprintf(&md, "@%s\n", name)
			impWrite(t, filepath.Join(src, name), name)
		}
		impWrite(t, filepath.Join(src, "CLAUDE.md"), md.String())
		s := resolveImports(importOpts{Source: src, HostHome: home})
		if len(s.Files) != maxImportFiles {
			t.Errorf("files = %d", len(s.Files))
		}
		if len(s.Refused) != 1 || s.Refused[0].Reason != ReasonImportTooMany || s.Refused[0].Import != "f64.md" {
			t.Errorf("refused = %v", s.Refused)
		}
	})
	t.Run("too many bytes", func(t *testing.T) {
		tmp := t.TempDir()
		home, src := impFixture(t, tmp)
		var md strings.Builder
		for i := 0; i < 17; i++ {
			name := fmt.Sprintf("g%02d.md", i)
			fmt.Fprintf(&md, "@%s\n", name)
			impWrite(t, filepath.Join(src, name), strings.Repeat("y", 250<<10))
		}
		impWrite(t, filepath.Join(src, "CLAUDE.md"), md.String())
		s := resolveImports(importOpts{Source: src, HostHome: home})
		if len(s.Files) != 16 {
			t.Errorf("files = %d", len(s.Files))
		}
		if len(s.Refused) != 1 || s.Refused[0].Reason != ReasonImportTooMany {
			t.Errorf("refused = %v", s.Refused)
		}
	})
}

func TestResolveImportsDenied(t *testing.T) {
	tmp := t.TempDir()
	home, src := impFixture(t, tmp)
	deny := filepath.Join(home, ".agent-console")
	impWrite(t, filepath.Join(deny, "state.md"), "S")
	impWrite(t, filepath.Join(home, ".ssh", "config"), "Host x")
	impWrite(t, filepath.Join(src, "CLAUDE.md"), "@~/.agent-console/state.md\n@~/.ssh/config\n")
	s := resolveImports(importOpts{Source: src, HostHome: home, Deny: []string{deny}})
	if len(s.Files) != 0 {
		t.Fatalf("files = %v", impBoxes(s))
	}
	for _, imp := range []string{"~/.agent-console/state.md", "~/.ssh/config"} {
		if got := impRefusal(s, imp); got != ReasonImportDenied {
			t.Errorf("%s: reason %q", imp, got)
		}
	}
}

func TestResolveImportsAbsoluteOutsideHome(t *testing.T) {
	tmp := t.TempDir()
	home, src := impFixture(t, tmp)
	impWrite(t, filepath.Join(src, "CLAUDE.md"), "@/etc/hostname\n@../../etc/x\n@~/../etc/y\n")
	s := resolveImports(importOpts{Source: src, HostHome: home})
	if len(s.Files) != 0 {
		t.Fatalf("files = %v", impBoxes(s))
	}
	if got := impRefusal(s, "/etc/hostname"); got != ReasonImportOutsideHome {
		t.Errorf("/etc/hostname: %q", got)
	}
	if got := impRefusal(s, "../../etc/x"); got != ReasonImportLeavesHome {
		t.Errorf("../../etc/x: %q", got)
	}
	if got := impRefusal(s, "~/../etc/y"); got != ReasonImportLeavesHome {
		t.Errorf("~/../etc/y: %q", got)
	}
}

func TestResolveImportsReserved(t *testing.T) {
	tmp := t.TempDir()
	home, src := impFixture(t, tmp)
	impWrite(t, filepath.Join(src, "CLAUDE.md"), "@~/.claude/CLAUDE.md\n")
	s := resolveImports(importOpts{Source: src, HostHome: home})
	if len(s.Files) != 0 || impRefusal(s, "~/.claude/CLAUDE.md") != ReasonImportReserved {
		t.Fatalf("got %+v", s)
	}
}

func TestResolveImportsMountUnsafe(t *testing.T) {
	tmp := t.TempDir()
	home, src := impFixture(t, tmp)
	impWrite(t, filepath.Join(src, "CLAUDE.md"), "@a,b.md\n")
	impWrite(t, filepath.Join(src, "a,b.md"), "x")
	s := resolveImports(importOpts{Source: src, HostHome: home})
	if want := box.MountUnsafe("/home/agent/.claude/a,b.md"); len(s.Files) != 0 || want == "" || impRefusal(s, "a,b.md") != want {
		t.Fatalf("got %+v", s)
	}
}

func TestResolveImportsConflict(t *testing.T) {
	// A config dir that is not ~/.claude: "@x.md" and "@~/.claude/x.md" land
	// on the same in-box path from two different host files.
	tmp := t.TempDir()
	home, _ := impFixture(t, tmp)
	src := filepath.Join(tmp, "cfg")
	impWrite(t, filepath.Join(src, "CLAUDE.md"), "@x.md\n@~/.claude/x.md\n")
	impWrite(t, filepath.Join(src, "x.md"), "cfg")
	impWrite(t, filepath.Join(home, ".claude", "x.md"), "home")
	s := resolveImports(importOpts{Source: src, HostHome: home})
	if got := impBoxes(s); !reflect.DeepEqual(got, []string{"/home/agent/.claude/x.md"}) || s.Files[0].SHA256 != impSum("cfg") {
		t.Fatalf("boxes = %v", got)
	}
	if got := impRefusal(s, "~/.claude/x.md"); got != ReasonImportConflict {
		t.Errorf("reason %q", got)
	}
}

func TestResolveImportsNoClaudeMD(t *testing.T) {
	tmp := t.TempDir()
	home, src := impFixture(t, tmp)
	s := resolveImports(importOpts{Source: src, HostHome: home})
	if len(s.Files) != 0 || len(s.Refused) != 0 {
		t.Fatalf("got %+v", s)
	}
	s = resolveImports(importOpts{Source: filepath.Join(tmp, "absent"), HostHome: home})
	if len(s.Files) != 0 || len(s.Refused) != 0 {
		t.Fatalf("got %+v", s)
	}
}

func TestReadImportRefusesSymlinkedComponent(t *testing.T) {
	// The read walks the resolved path one component at a time and refuses a
	// component that is (or became) a link, so a directory swapped for a link
	// after EvalSymlinks cannot redirect the open.
	tmp := t.TempDir()
	impWrite(t, filepath.Join(tmp, "real", "f.md"), "F")
	if err := os.Symlink(filepath.Join(tmp, "real"), filepath.Join(tmp, "link")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readImport(filepath.Join(tmp, "real", "f.md"), maxImportBytes); err != nil {
		t.Fatalf("real path: %v", err)
	}
	if _, _, err := readImport(filepath.Join(tmp, "link", "f.md"), maxImportBytes); !errors.Is(err, errImportChanged) {
		t.Fatalf("linked component: err = %v", err)
	}
}

func TestImportSetFingerprint(t *testing.T) {
	a := ImportSet{
		Files:   []ImportFile{{Box: "/b", SHA256: "2"}, {Box: "/a", SHA256: "1"}},
		Refused: []ImportRefusal{{Import: "y", Reason: "r"}, {Import: "x", Reason: "r"}},
	}
	b := ImportSet{
		Files:   []ImportFile{{Box: "/a", SHA256: "1"}, {Box: "/b", SHA256: "2"}},
		Refused: []ImportRefusal{{Import: "x", Reason: "r"}, {Import: "y", Reason: "r"}},
	}
	if a.fingerprint() != b.fingerprint() {
		t.Error("fingerprint depends on order")
	}
	c := b
	c.Files = []ImportFile{{Box: "/a", SHA256: "1"}, {Box: "/b", SHA256: "3"}}
	if c.fingerprint() == b.fingerprint() {
		t.Error("fingerprint ignores content")
	}
	d := b
	d.Refused = nil
	if d.fingerprint() == b.fingerprint() {
		t.Error("fingerprint ignores refusals")
	}
	if (ImportSet{}).fingerprint() == "" {
		t.Error("empty fingerprint")
	}
}

func TestReadImportNeverBlocksOnFIFO(t *testing.T) {
	// resolveImports' Lstat gate refuses a FIFO before readImport runs; this
	// pins readImport on its own (a FIFO swapped in after that gate): its walk
	// re-checks the type before the open, and the open is O_NONBLOCK too.
	p := filepath.Join(t.TempDir(), "f.md")
	if err := syscall.Mkfifo(p, 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, _, err := readImport(p, maxImportBytes); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, errImportChanged) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("readImport blocked on a FIFO")
	}
}
