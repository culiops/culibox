package clone

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/handback"
)

// elfBytes is a runnable-looking ELF head (ET_EXEC, little-endian) padded to n bytes.
func elfBytes(n int, tag byte) []byte {
	b := make([]byte, max(n, 64))
	copy(b, "\x7fELF")
	b[4], b[5], b[16] = 2, 1, 2
	b[63] = tag // makes two blobs differ
	return b
}

// branchFx is a host repo on "main" (one commit) with a branch "box" to fill.
type branchFx struct {
	t    *testing.T
	of   string
	g    ExecGit
	base string
}

func newBranchFx(t *testing.T) *branchFx {
	t.Helper()
	g, err := NewExecGit()
	if err != nil {
		t.Skip("no git on PATH")
	}
	of := filepath.Join(t.TempDir(), "of")
	if err := os.MkdirAll(of, 0o755); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, of, "init", "-q", "-b", "main")
	x := &branchFx{t: t, of: of, g: g}
	x.write("README", "hello", 0o644)
	x.commit("base")
	x.base = x.rev("main")
	fixtureGit(t, of, "checkout", "-q", "-b", "box")
	return x
}

func (x *branchFx) write(name, content string, mode os.FileMode) {
	x.t.Helper()
	p := filepath.Join(x.of, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		x.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		x.t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		x.t.Fatal(err)
	}
}

func (x *branchFx) commit(msg string) {
	x.t.Helper()
	fixtureGit(x.t, x.of, "add", "-A")
	fixtureGit(x.t, x.of, "commit", "-q", "-m", msg)
}

func (x *branchFx) rev(ref string) string {
	x.t.Helper()
	out, err := x.g.Run(context.Background(), x.of, CleanEnv(), "rev-parse", ref)
	if err != nil {
		x.t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func (x *branchFx) inspect(lim handback.Limits) handback.Report {
	x.t.Helper()
	return InspectBranch(context.Background(), x.g, x.of, x.base, "refs/heads/box", lim)
}

func flagFor(rep handback.Report, path string) (handback.Flag, bool) {
	for _, f := range rep.Flags {
		if f.Path == path {
			return f, true
		}
	}
	return handback.Flag{}, false
}

func hasClass(f handback.Flag, c handback.Class) bool { return slices.Contains(f.Classes, c) }

func TestInspectBranchFlagsEveryCommit(t *testing.T) {
	x := newBranchFx(t)
	elf := elfBytes(300, 1)
	x.write("report.pdf", string(elf), 0o644)
	x.write("run.sh", "#!/bin/sh\necho hi\n", 0o755)
	x.write("notes.txt", "plain words", 0o644)
	if err := os.Symlink("/etc/passwd", filepath.Join(x.of, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("notes.txt", filepath.Join(x.of, "inside")); err != nil {
		t.Fatal(err)
	}
	x.commit("c1")
	if err := os.Remove(filepath.Join(x.of, "report.pdf")); err != nil {
		t.Fatal(err)
	}
	x.commit("c2")

	rep := x.inspect(handback.DefaultLimits())
	if rep.Truncated != "" {
		t.Fatalf("Truncated = %q", rep.Truncated)
	}
	pdf, ok := flagFor(rep, "report.pdf")
	if !ok || !hasClass(pdf, handback.ClassMismatch) || !hasClass(pdf, handback.ClassExecutable) {
		t.Fatalf("report.pdf (deleted in c2) not flagged as a mismatch: %+v", rep.Flags)
	}
	sum := sha256.Sum256(elf)
	if pdf.SHA256 != hex.EncodeToString(sum[:]) || pdf.HashNote != "" {
		t.Errorf("report.pdf hash = %q note %q, want %x", pdf.SHA256, pdf.HashNote, sum)
	}
	if sh, ok := flagFor(rep, "run.sh"); !ok || !hasClass(sh, handback.ClassScript) || sh.SHA256 == "" {
		t.Errorf("run.sh = %+v, %v", sh, ok)
	}
	ln, ok := flagFor(rep, "link")
	if !ok || !hasClass(ln, handback.ClassSymlinkOut) || ln.Target != "/etc/passwd" || ln.SHA256 != "" {
		t.Errorf("link = %+v, %v", ln, ok)
	}
	if len(rep.Flags) != 3 {
		t.Errorf("flags = %+v, want exactly report.pdf, run.sh and link", rep.Flags)
	}
	if rep.Flags[0].Path != "report.pdf" {
		t.Errorf("first flag = %q, want the mismatch first", rep.Flags[0].Path)
	}
	if rep.Inspected < 4 {
		t.Errorf("Inspected = %d", rep.Inspected)
	}
}

func TestInspectBranchSameBlobAtTwoPaths(t *testing.T) {
	x := newBranchFx(t)
	// One blob under a harmless name and under a document name: the mismatch
	// must not hide behind whichever path came first.
	elf := string(elfBytes(200, 7))
	x.write("tool.bin", elf, 0o644)
	x.write("docs/invoice.pdf", elf, 0o644)
	x.commit("c1")
	rep := x.inspect(handback.DefaultLimits())
	pdf, ok := flagFor(rep, "docs/invoice.pdf")
	if !ok || !hasClass(pdf, handback.ClassMismatch) {
		t.Fatalf("invoice.pdf mismatch hidden: %+v", rep.Flags)
	}
	if bin, ok := flagFor(rep, "tool.bin"); !ok || hasClass(bin, handback.ClassMismatch) || bin.SHA256 != pdf.SHA256 {
		t.Errorf("tool.bin = %+v, %v", bin, ok)
	}
}

func TestInspectBranchIgnoresHostChanges(t *testing.T) {
	x := newBranchFx(t)
	x.write("mine.pdf", string(elfBytes(100, 2)), 0o644)
	x.commit("box work")
	// The host's main moves on and gains an exec script; the box branch was
	// forked from the older base.
	fixtureGit(t, x.of, "checkout", "-q", "main")
	x.write("host.sh", "#!/bin/sh\n", 0o755)
	x.commit("host work")
	rep := x.inspect(handback.DefaultLimits())
	if _, ok := flagFor(rep, "host.sh"); ok {
		t.Errorf("a host file was flagged: %+v", rep.Flags)
	}
	if _, ok := flagFor(rep, "mine.pdf"); !ok {
		t.Errorf("the box file was not flagged: %+v", rep.Flags)
	}
}

func TestInspectBranchBigBlob(t *testing.T) {
	// Review Focus 5 with lowered caps (ruling R1): the code path is the same
	// as for a 100 MiB blob over the 64 MiB default.
	x := newBranchFx(t)
	x.write("big.bin", string(elfBytes(2<<20, 3)), 0o644)
	x.commit("big")
	lim := handback.DefaultLimits()
	lim.HashPerFile = 1 << 20
	rep := x.inspect(lim)
	f, ok := flagFor(rep, "big.bin")
	if !ok || !hasClass(f, handback.ClassExecutable) {
		t.Fatalf("big.bin not flagged from its head: %+v", rep.Flags)
	}
	if f.SHA256 != "" || f.HashNote != "not hashed: larger than 64 MiB" {
		t.Errorf("big.bin hash = %q note %q", f.SHA256, f.HashNote)
	}
	if rep.Truncated != "" {
		t.Errorf("Truncated = %q", rep.Truncated)
	}
}

func TestInspectBranchHashBudget(t *testing.T) {
	x := newBranchFx(t)
	x.write("a.bin", string(elfBytes(600<<10, 4)), 0o644)
	x.write("b.bin", string(elfBytes(600<<10, 5)), 0o644)
	x.commit("two")
	lim := handback.DefaultLimits()
	lim.HashTotal = 1 << 20
	rep := x.inspect(lim)
	a, aok := flagFor(rep, "a.bin")
	b, bok := flagFor(rep, "b.bin")
	if !aok || !bok {
		t.Fatalf("flags = %+v", rep.Flags)
	}
	hashed, unhashed := a, b
	if a.SHA256 == "" {
		hashed, unhashed = b, a
	}
	if hashed.SHA256 == "" || hashed.HashNote != "" {
		t.Errorf("neither blob was hashed: %+v %+v", a, b)
	}
	if unhashed.SHA256 != "" || unhashed.HashNote != "not hashed: hash budget for this scan used up" {
		t.Errorf("second blob = %+v, want the budget note", unhashed)
	}
	if !hasClass(unhashed, handback.ClassExecutable) {
		t.Errorf("the over-budget blob must still be classified: %+v", unhashed)
	}
}

func TestInspectBranchCaps(t *testing.T) {
	x := newBranchFx(t)
	x.write("one.txt", "1", 0o644)
	x.write("two.txt", "2", 0o644)
	x.write("three.txt", "3", 0o644)
	x.commit("three")
	lim := handback.DefaultLimits()
	lim.Candidates = 1
	rep := x.inspect(lim)
	if !strings.Contains(rep.Truncated, "candidate cap of 1") {
		t.Errorf("Truncated = %q", rep.Truncated)
	}
}

func TestInspectBranchFlagCapKeepsTheStrongest(t *testing.T) {
	x := newBranchFx(t)
	zip := "PK\x03\x04" + strings.Repeat("\x00", 60)
	for i := 0; i < 6; i++ {
		x.write("a"+string(rune('0'+i))+".zip", zip+string(rune('0'+i)), 0o644)
	}
	x.write("zzz.pdf", string(elfBytes(100, 9)), 0o644) // sorts last, but is the strongest
	x.commit("many")
	lim := handback.DefaultLimits()
	lim.MaxFlags = 2
	rep := x.inspect(lim)
	if _, ok := flagFor(rep, "zzz.pdf"); !ok {
		t.Fatalf("the mismatch was pushed out by archives: %+v", rep.Flags)
	}
	if len(rep.Flags) != 2 || !strings.Contains(rep.Truncated, "flag cap of 2") {
		t.Errorf("flags %d, Truncated %q", len(rep.Flags), rep.Truncated)
	}
}

func TestInspectBranchSanitizesPaths(t *testing.T) {
	x := newBranchFx(t)
	// A name is at most 255 bytes on disk, so the long path is a chain of directories.
	name := strings.Repeat(strings.Repeat("d", 200)+"/", 6) + "ev‮il.pdf"
	x.write(name, string(elfBytes(100, 1)), 0o644)
	x.commit("long")
	rep := x.inspect(handback.DefaultLimits())
	if len(rep.Flags) != 1 {
		t.Fatalf("flags = %+v", rep.Flags)
	}
	f := rep.Flags[0]
	if !strings.HasSuffix(f.Path, "ev?il.pdf") || strings.ContainsRune(f.Path, 0x202e) || len([]rune(f.Path)) > 1024 || !hasClass(f, handback.ClassMismatch) {
		t.Errorf("path %q (%d runes), classes %v", f.Path, len([]rune(f.Path)), f.Classes)
	}
}

func TestInspectBranchNoStreamGit(t *testing.T) {
	x := newBranchFx(t)
	rep := InspectBranch(context.Background(), noStreamGit{x.g}, x.of, x.base, "refs/heads/box", handback.DefaultLimits())
	if rep.Truncated != "inspection unavailable" || rep.Flags == nil || len(rep.Flags) != 0 {
		t.Errorf("report = %+v", rep)
	}
}

// noStreamGit hides ExecGit's Stream.
type noStreamGit struct{ g ExecGit }

func (n noStreamGit) Run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	return n.g.Run(ctx, dir, env, args...)
}

func TestInspectBranchBadRefIsTruncatedNotAnError(t *testing.T) {
	x := newBranchFx(t)
	rep := InspectBranch(context.Background(), x.g, x.of, x.base, "refs/heads/nope", handback.DefaultLimits())
	if !strings.HasPrefix(rep.Truncated, "could not inspect: ") || rep.Flags == nil {
		t.Errorf("report = %+v", rep)
	}
}

func TestInspectBranchCancelled(t *testing.T) {
	x := newBranchFx(t)
	x.write("a.pdf", string(elfBytes(100, 1)), 0o644)
	x.commit("c")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rep := InspectBranch(ctx, x.g, x.of, x.base, "refs/heads/box", handback.DefaultLimits())
	if rep.Truncated == "" {
		t.Errorf("a cancelled scan reported nothing: %+v", rep)
	}
}

func TestBatchParserSplitsAnyWay(t *testing.T) {
	stream := []byte("aaaa blob 5\nhello\nbbbb missing\ncccc blob 0\n\ndddd blob 3\nxyz\n")
	for _, step := range []int{1, 2, 3, 7, len(stream)} {
		var got []string
		var cur bytes.Buffer
		p := &batchParser{
			start:   func(id string, size int64) { cur.Reset() },
			data:    func(b []byte) { cur.Write(b) },
			end:     func(id string) { got = append(got, id+"="+cur.String()) },
			missing: func(id string) { got = append(got, id+"=missing") },
		}
		for i := 0; i < len(stream); i += step {
			if _, err := p.Write(stream[i:min(i+step, len(stream))]); err != nil {
				t.Fatal(err)
			}
		}
		if want := "aaaa=hello bbbb=missing cccc= dddd=xyz"; strings.Join(got, " ") != want {
			t.Errorf("step %d: %q, want %q", step, strings.Join(got, " "), want)
		}
	}
	bad := &batchParser{start: func(string, int64) {}, data: func([]byte) {}, end: func(string) {}, missing: func(string) {}}
	if _, err := bad.Write([]byte("aaaa blob 3\nxyzQ")); err == nil {
		t.Error("a missing newline after the body was accepted")
	}
}

func TestFetchCarriesHandback(t *testing.T) {
	x := newFetchFx(t)
	x.dockerCommitELF("tool.pdf")
	res, err := x.fetch()
	if err != nil {
		t.Fatal(err)
	}
	if f, ok := flagFor(res.Handback, "tool.pdf"); !ok || !hasClass(f, handback.ClassMismatch) {
		t.Errorf("Fetch's Handback = %+v", res.Handback)
	}
}

// dockerCommitELF commits an ELF named name on the clone's feat branch.
func (x *fetchFx) dockerCommitELF(name string) {
	x.t.Helper()
	x.commitInClone(name, string(elfBytes(200, 1)), "elf")
}
