package box

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The workspace is resolved once. A symlink in the path that is re-pointed after
// that resolution (here at the data directory, which a workspace must never
// be) changes nothing: the protected check, the box name, the label, the mount
// and the exec's -w all carry the directory that was resolved and checked.
func TestWrapUsesOneResolutionForCheckNameAndMount(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	root := ctxTmp(t)
	a := filepath.Join(root, "a")
	if err := os.Mkdir(a, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}
	want := mustBoxName(t, m, link)

	var got workspace
	hooked := false
	wrapResolvedHook = func(ws workspace) {
		hooked, got = true, ws
		if err := os.Remove(link); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(m.opts.StateRoot, link); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { wrapResolvedHook = nil })

	argv, _, name, err := m.Wrap(context.Background(), link, "s1", BoxKey{}, []string{"claude"}, nil, nil)
	if err != nil {
		t.Fatalf("Wrap after the flip: %v", err)
	}
	if !hooked {
		t.Fatal("wrapResolvedHook never ran")
	}
	if got.Given != link || got.Path != a {
		t.Fatalf("hook saw workspace %+v, want Given %q and Path %q", got, link, a)
	}
	if name != want {
		t.Fatalf("name = %s, want %s (the name before the flip)", name, want)
	}
	if lbl := f.labels[name][LabelWorkspace]; lbl != a {
		t.Fatalf("workspace label = %q, want %q", lbl, a)
	}
	if len(f.runArgs) != 1 {
		t.Fatalf("docker run called %d times, want 1", len(f.runArgs))
	}
	if !slices.Contains(f.runArgs[0], "type=bind,src="+a+",dst="+a) {
		t.Fatalf("no workspace mount of %s onto itself in %v", a, f.runArgs[0])
	}
	if bad := "type=bind,src=" + m.opts.StateRoot + ",dst=" + m.opts.StateRoot; slices.Contains(f.runArgs[0], bad) {
		t.Fatalf("the re-pointed link target was mounted: %s", bad)
	}
	i := slices.Index(argv, "-w")
	if i < 0 || i+1 >= len(argv) || argv[i+1] != a {
		t.Fatalf("argv = %v, want -w %s", argv, a)
	}
}

// An unresolvable workspace fails with the text BoxName always gave, and before
// anything is created.
func TestWrapRefusesAnUnresolvableWorkspaceWithTheResolveText(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	gone := filepath.Join(ctxTmp(t), "gone")
	_, _, _, err := m.Wrap(context.Background(), gone, "s1", BoxKey{}, []string{"claude"}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), `box: resolving workspace "`+gone+`"`) {
		t.Fatalf("err = %v, want the resolving-workspace text for %q", err, gone)
	}
	if _, nerr := m.BoxName(gone, BoxKey{}); nerr == nil || nerr.Error() != err.Error() {
		t.Fatalf("BoxName err = %v, want the same as Wrap's: %v", nerr, err)
	}
	if len(f.runArgs) != 0 {
		t.Fatalf("docker run called %d times for a workspace that does not resolve", len(f.runArgs))
	}
}

// A relative cwd is made absolute: Path is what is checked, named, mounted.
func TestResolveWorkspaceMakesARelativePathAbsolute(t *testing.T) {
	d := ctxTmp(t)
	if err := os.Mkdir(filepath.Join(d, "rel"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(d)
	ws, err := resolveWorkspace("rel")
	if err != nil {
		t.Fatal(err)
	}
	if ws.Given != "rel" || ws.Path != filepath.Join(d, "rel") {
		t.Fatalf("ws = %+v, want Given rel and Path %s", ws, filepath.Join(d, "rel"))
	}
}

// A `..` after a symlink is resolved the way the kernel does it (the link's
// target's parent), not lexically: the workspace, and so the box name, is the
// directory the path opens. Made absolute first, filepath.Abs would have cleaned
// `link/..` to the folder holding the link.
func TestResolvePathResolvesDotDotAfterASymlinkLikeTheKernel(t *testing.T) {
	d := ctxTmp(t)
	target := filepath.Join(d, "elsewhere", "deep")
	mkdirs(t, target, filepath.Join(d, "here"))
	link := filepath.Join(d, "here", "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(d, "elsewhere") // link/.. is deep's parent
	got, err := resolvePath(link + "/..")
	if err != nil || got != want {
		t.Fatalf("resolvePath(link/..) = %q, %v; want %q", got, err, want)
	}
	// Relative to a working directory that is itself reached through a link.
	t.Chdir(link)
	got, err = resolvePath("..")
	if err != nil || got != want {
		t.Fatalf("resolvePath(..) from the link = %q, %v; want %q", got, err, want)
	}
	if got, err = resolvePath("."); err != nil || got != target {
		t.Fatalf("resolvePath(.) = %q, %v; want %q", got, err, target)
	}
}
