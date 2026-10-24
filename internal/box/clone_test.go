package box

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cloneKey is a clone-mode key whose mounts follow the spec's table: the source
// repo's .git, one share, both read-only.
func cloneKey(ws, hash string) BoxKey {
	return BoxKey{Clone: &CloneMounts{Hash: hash, RO: []Mount{
		{Host: "/r/of/.git", Box: "/r/of/.git"},
		{Host: "/r/of/docs/sp", Box: filepath.Join(ws, "docs/sp")},
	}}}
}

func TestCloneKeyChangesBoxName(t *testing.T) {
	m := newTestManager(t) // Instance "TEST"
	const dir = "/usr"
	plain, err := m.boxName(dir, BoxKey{})
	if err != nil {
		t.Fatal(err)
	}
	// Computed by the code as it was before clone mode existed: a non-clone
	// spawn's box name must never change, or live boxes and boxstate are orphaned.
	if want := "ab-box-94ab17bf2b8a"; plain != want {
		t.Fatalf("a non-clone name changed: got %s, want %s", plain, want)
	}
	a, err := m.boxName(dir, BoxKey{Clone: &CloneMounts{Hash: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.boxName(dir, BoxKey{Clone: &CloneMounts{Hash: "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if plain == a || plain == b || a == b {
		t.Fatalf("want three distinct names, got %s %s %s", plain, a, b)
	}
}

func TestCloneBoxNameStable(t *testing.T) {
	m := newTestManager(t)
	x, err := m.boxName("/usr", BoxKey{Clone: &CloneMounts{Hash: "h", RO: []Mount{{Host: "/a", Box: "/a"}}}})
	if err != nil {
		t.Fatal(err)
	}
	y, err := m.boxName("/usr", BoxKey{Clone: &CloneMounts{Hash: "h", RO: []Mount{{Host: "/a", Box: "/a"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if x != y {
		t.Fatalf("equal clone specs must name one box: %s vs %s", x, y)
	}
}

// D12, asserted on the rendered args: a clone adds no writable bind.
func TestCloneWrapMountsAreReadOnly(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	ws := realTemp(t)
	if _, _, _, err := m.Wrap(context.Background(), ws, "s1", cloneKey(ws, "h"), []string{"bash"}, nil, []Mount{bundleRoot}); err != nil {
		t.Fatal(err)
	}
	args := f.runArgs[0]
	for _, want := range []string{
		"type=bind,src=/r/of/docs/sp,dst=" + filepath.Join(ws, "docs/sp") + ",readonly",
		"type=bind,src=/r/of/.git,dst=/r/of/.git,readonly",
		"type=bind,src=" + bundleRoot.Host + ",dst=" + bundleRoot.Box + ",readonly",
	} {
		if !containsPair(args, "--mount", want) {
			t.Errorf("missing %q in %q", want, args)
		}
	}
	var writable []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--mount" && !strings.HasSuffix(args[i+1], ",readonly") {
			writable = append(writable, args[i+1])
		}
	}
	if len(writable) != 3 {
		t.Fatalf("want exactly the workspace, the box home and the out dir writable, got %q", writable)
	}
	if !strings.Contains(writable[0], "dst="+ws) || !strings.Contains(writable[1], "dst="+boxHome) ||
		!strings.HasSuffix(writable[2], "dst="+PlaywrightOutBox) {
		t.Fatalf("the writable mounts must be the workspace, the home and the out dir: %q", writable)
	}
}

func TestCloneWorkspaceSkipsGuard(t *testing.T) {
	m, f := guardMgr(t, true)
	ws := realTemp(t)
	gitRepo(t, ws, "")
	if _, _, _, err := m.Wrap(context.Background(), ws, "s1", BoxKey{Clone: &CloneMounts{Hash: "h"}}, []string{"bash"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	args := f.runArgs[0]
	if hasDst(args, filepath.Join(ws, ".claude")) || hasDst(args, filepath.Join(ws, ".git")) {
		t.Fatalf("a clone workspace is not guarded: %q", args)
	}
	if _, err := os.Lstat(filepath.Join(ws, ".claude")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("no .claude may be created in a clone workspace: %v", err)
	}

	// The same manager, same kind of directory, without a Clone key: guarded.
	ws2 := realTemp(t)
	gitRepo(t, ws2, "")
	if _, _, _, err := m.Wrap(context.Background(), ws2, "s2", BoxKey{}, []string{"bash"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !hasDst(f.runArgs[1], filepath.Join(ws2, ".git")) {
		t.Fatalf("a plain boxed spawn is still guarded: %q", f.runArgs[1])
	}
}

func TestLiveCloneBoxMissingShareIsRefused(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	ws := realTemp(t)
	key := cloneKey(ws, "h")
	name, err := m.boxName(ws, key)
	if err != nil {
		t.Fatal(err)
	}
	f.running[name] = true
	f.mounts[name] = []string{ws, boxHome, "/r/of/.git"} // lacks the share destination
	m.AdoptBox(name)
	_, _, _, err = m.Wrap(context.Background(), ws, "s2", key, []string{"bash"}, nil, nil)
	if !errors.Is(err, ErrBoxUnavailable) {
		t.Fatalf("want ErrBoxUnavailable, got %v", err)
	}
	if !strings.Contains(err.Error(), "was started without") || !strings.Contains(err.Error(), filepath.Join(ws, "docs/sp")) {
		t.Fatalf("the refusal must name the missing share: %v", err)
	}
	if f.stops() != 0 || f.runs() != 0 {
		t.Fatalf("a box with a live session must not be touched: stops=%d runs=%d", f.stops(), f.runs())
	}
}

func TestCloneUnsafeShareSourceRefused(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	ws := realTemp(t)
	key := BoxKey{Clone: &CloneMounts{Hash: "h", RO: []Mount{{Host: "/r/of/do,cs", Box: filepath.Join(ws, "docs")}}}}
	_, _, _, err := m.Wrap(context.Background(), ws, "s1", key, []string{"bash"}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "comma") {
		t.Fatalf("want a refusal naming the comma, got %v", err)
	}
	if f.runs() != 0 {
		t.Fatalf("nothing may be started: runs=%d", f.runs())
	}
}

func TestCloneKeyWithoutHashRefused(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	ws := realTemp(t)
	if _, _, _, err := m.Wrap(context.Background(), ws, "s1", BoxKey{Clone: &CloneMounts{}}, []string{"bash"}, nil, nil); err == nil {
		t.Fatal("a clone key without a hash must be refused")
	}
	if f.runs() != 0 {
		t.Fatalf("nothing may be started: runs=%d", f.runs())
	}
}
