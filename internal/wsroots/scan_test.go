package wsroots

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// mk creates the files (a trailing "/" makes a folder) under root.
func mk(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, n := range names {
		p := filepath.Join(root, n)
		if n[len(n)-1] == '/' {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// resolved is a temp dir with its symlinks resolved, as Scan reports paths.
func resolved(t *testing.T) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return real
}

func paths(ps []Place) []string {
	out := []string{}
	for _, p := range ps {
		out = append(out, p.Path)
	}
	return out
}

func TestScanFindsProjectsUnderBases(t *testing.T) {
	b1, b2 := resolved(t), resolved(t)
	mk(t, b1, "a/.git/", "b/c/go.mod", "plain/x/")
	mk(t, b2, "p/package.json", "q/Cargo.toml", "r/pyproject.toml", "s/.git") // .git as a file (a worktree)
	got := Scan(context.Background(), []string{b1, b2}, Env{}, ScanOpts{})
	want := []string{
		filepath.Join(b1, "a"), filepath.Join(b1, "b", "c"),
		filepath.Join(b2, "p"), filepath.Join(b2, "q"), filepath.Join(b2, "r"), filepath.Join(b2, "s"),
	}
	if !reflect.DeepEqual(paths(got), want) {
		t.Fatalf("got %v, want %v", paths(got), want)
	}
	if got[1].Label != "c" {
		t.Fatalf("label = %q, want the base name c", got[1].Label)
	}
}

func TestScanStopsAtAProjectFolder(t *testing.T) {
	b := resolved(t)
	mk(t, b, "app/.git/", "app/sub/go.mod", "app/vendor/x/package.json")
	got := Scan(context.Background(), []string{b}, Env{}, ScanOpts{})
	if want := []string{filepath.Join(b, "app")}; !reflect.DeepEqual(paths(got), want) {
		t.Fatalf("got %v, want %v", paths(got), want)
	}
}

func TestScanDepthLimit(t *testing.T) {
	b := resolved(t)
	mk(t, b, "a/b/c/.git/", "x/y/z/w/.git/") // c is 3 below the base; w is 4
	got := Scan(context.Background(), []string{b}, Env{}, ScanOpts{})
	if want := []string{filepath.Join(b, "a", "b", "c")}; !reflect.DeepEqual(paths(got), want) {
		t.Fatalf("got %v, want %v", paths(got), want)
	}
}

func TestScanMax(t *testing.T) {
	b := resolved(t)
	for i := 0; i < 60; i++ {
		mk(t, b, fmt.Sprintf("p%02d/.git/", i))
	}
	got := Scan(context.Background(), []string{b}, Env{}, ScanOpts{})
	if len(got) != 50 {
		t.Fatalf("default Max: got %d places, want 50", len(got))
	}
	got = Scan(context.Background(), []string{b}, Env{}, ScanOpts{Max: 3})
	if want := []string{filepath.Join(b, "p00"), filepath.Join(b, "p01"), filepath.Join(b, "p02")}; !reflect.DeepEqual(paths(got), want) {
		t.Fatalf("Max 3: got %v, want %v", paths(got), want)
	}
}

func TestScanBudget(t *testing.T) {
	b := resolved(t)
	for i := 0; i < 200; i++ {
		mk(t, b, fmt.Sprintf("d%03d/e/f/.git/", i))
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond) // the deadline has passed
	start := time.Now()
	got := Scan(ctx, []string{b}, Env{}, ScanOpts{})
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %v with an expired context", d)
	}
	if len(got) >= 200 {
		t.Fatalf("an expired context still found all %d places", len(got))
	}
	// The scan's own budget bounds it too, with no deadline on ctx.
	start = time.Now()
	Scan(context.Background(), []string{b}, Env{}, ScanOpts{Budget: time.Nanosecond})
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %v with a 1ns budget", d)
	}
}

func TestScanSkipsForbiddenAndSymlinks(t *testing.T) {
	b, out := resolved(t), resolved(t)
	mk(t, b, "ok/.git/", "ssh-ish/.git/")
	mk(t, out, "elsewhere/.git/")
	if err := os.Symlink(filepath.Join(out, "elsewhere"), filepath.Join(b, "link")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	if err := os.Symlink(out, filepath.Join(b, "dirlink")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	got := Scan(context.Background(), []string{b}, Env{Home: filepath.Join(out, "home"), DataDir: filepath.Join(out, "data")}, ScanOpts{})
	want := []string{filepath.Join(b, "ok"), filepath.Join(b, "ssh-ish")}
	if !reflect.DeepEqual(paths(got), want) {
		t.Fatalf("got %v, want %v (links are not followed)", paths(got), want)
	}
	// A folder Refusal rejects is skipped with everything under it: a base that
	// holds CuliBox's data folder or the credential folders, and one inside it.
	for name, env := range map[string]Env{
		"data folder inside the base": {DataDir: filepath.Join(b, "ok", "data")},
		"credentials inside the base": {Home: filepath.Join(b, "ok")},
		"the data folder itself":      {DataDir: b},
		"the data folder above":       {DataDir: filepath.Dir(b)},
	} {
		if got := Scan(context.Background(), []string{b}, env, ScanOpts{}); len(got) != 0 {
			t.Errorf("%s: the base was scanned: %v", name, got)
		}
	}
	// A link given as a base is resolved, and the results are under the real path.
	got = Scan(context.Background(), []string{filepath.Join(b, "dirlink")}, Env{}, ScanOpts{})
	if want := []string{filepath.Join(out, "elsewhere")}; !reflect.DeepEqual(paths(got), want) {
		t.Fatalf("a linked base: got %v, want %v", paths(got), want)
	}
}

func TestScanSkipsUnreadableAndDotFolders(t *testing.T) {
	b := resolved(t)
	mk(t, b, "open/.git/", "shut/inner/.git/", ".hidden/x/.git/")
	if err := os.Chmod(filepath.Join(b, "shut"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(b, "shut"), 0o755) })
	got := Scan(context.Background(), []string{b, filepath.Join(b, "missing"), "relative"}, Env{}, ScanOpts{})
	if want := []string{filepath.Join(b, "open")}; !reflect.DeepEqual(paths(got), want) {
		t.Fatalf("got %v, want %v", paths(got), want)
	}
}

func TestDefaultBasesLinux(t *testing.T) {
	old := globFn
	defer func() { globFn = old }()
	var asked []string
	globFn = func(p string) []string {
		asked = append(asked, p)
		return []string{p[:len(p)-1] + "disk"}
	}
	got := DefaultBases("linux", "ann")
	want := []string{"/srv", "/opt", "/data", "/mnt/disk", "/media/ann/disk", "/run/media/ann/disk"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for _, u := range []string{"", "a/b", "..", "x*"} {
		asked = nil
		if g := DefaultBases("linux", u); len(g) != 4 {
			t.Fatalf("user %q: got %v, want the fixed three and /mnt/*", u, g)
		}
		if len(asked) != 1 {
			t.Fatalf("user %q asked %v", u, asked)
		}
	}
	if g := DefaultBases("plan9", "ann"); g != nil {
		t.Fatalf("unknown OS: got %v", g)
	}
}

func TestDefaultBasesDarwinSkipsBootVolume(t *testing.T) {
	vols := resolved(t)
	mk(t, vols, "Data/")
	if err := os.Symlink("/", filepath.Join(vols, "Macintosh HD")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	if err := os.Symlink(filepath.Join(vols, "Data"), filepath.Join(vols, "Alias")); err != nil {
		t.Fatal(err)
	}
	old := globFn
	defer func() { globFn = old }()
	globFn = func(p string) []string {
		if p != "/Volumes/*" {
			t.Fatalf("darwin globbed %q", p)
		}
		return []string{filepath.Join(vols, "Alias"), filepath.Join(vols, "Data"), filepath.Join(vols, "Macintosh HD"), filepath.Join(vols, "gone")}
	}
	got := DefaultBases("darwin", "ann")
	want := []string{filepath.Join(vols, "Alias"), filepath.Join(vols, "Data"), "/opt", "/srv"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestScanSkipsTheSkipSet(t *testing.T) {
	b := resolved(t)
	mk(t, b, "homebrew/.git/", "homebrew/Library/x/.git/", "keep/.git/", "grp/brew/.git/")
	got := Scan(context.Background(), []string{b}, Env{}, ScanOpts{Skip: []string{filepath.Join(b, "homebrew"), filepath.Join(b, "grp", "brew")}})
	if want := []string{filepath.Join(b, "keep")}; !reflect.DeepEqual(paths(got), want) {
		t.Fatalf("got %v, want %v", paths(got), want)
	}
	// With no skip set both are listed.
	if got := Scan(context.Background(), []string{b}, Env{}, ScanOpts{}); len(got) != 3 {
		t.Fatalf("without Skip: %v", paths(got))
	}
}
