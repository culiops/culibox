package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
)

// listEntries lists dir through handleFsList and returns its entries by name.
func listEntries(t *testing.T, s *Server, dir string) map[string]fsEntry {
	t.Helper()
	rr := fsGet(s.handleFsList, "/api/fs/list", dir)
	if rr.Code != http.StatusOK {
		t.Fatalf("list %s: code = %d (%s)", dir, rr.Code, rr.Body)
	}
	var got struct {
		Entries []fsEntry `json:"entries"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	out := map[string]fsEntry{}
	for _, e := range got.Entries {
		out[e.Name] = e
	}
	return out
}

// A symlink is listed as a link; it is marked a folder (target_dir) only when its target resolves, under the same fs
// policy as a typed path, to a directory. is_dir stays lstat's answer, so a client that does not know the new fields
// (the phone's tree) lists it exactly as before.
func TestFsListSymlinkDir(t *testing.T) {
	s, home, _ := boxedServer(t)
	mustMkdirAll(t, filepath.Join(home, "real"))
	mustWrite(t, filepath.Join(home, "real", "f.txt"), "x")
	mustSymlink(t, "real", filepath.Join(home, "lnk"))
	mustSymlink(t, t.TempDir(), filepath.Join(home, "out")) // a folder outside every served root
	mustSymlink(t, "loop", filepath.Join(home, "loop"))     // a link to itself
	mustSymlink(t, "real/f.txt", filepath.Join(home, "flink"))
	mustSymlink(t, "data", filepath.Join(home, "dl")) // the denied tree
	mustSymlink(t, "missing", filepath.Join(home, "dangling"))

	got := listEntries(t, s, home)
	if e := got["lnk"]; e.IsDir || !e.Link || !e.TargetDir {
		t.Errorf("lnk = %+v, want link to a folder (is_dir false, link, target_dir)", e)
	}
	for _, n := range []string{"out", "loop", "dl", "flink", "dangling"} {
		if e := got[n]; e.IsDir || !e.Link || e.TargetDir {
			t.Errorf("%s = %+v, want a link that is not a folder the policy serves", n, e)
		}
	}
	if e := got["real"]; !e.IsDir || e.Link || e.TargetDir {
		t.Errorf("real = %+v, want a plain folder", e)
	}
	// The folder a link names is listed as a typed path would be.
	if sub := listEntries(t, s, filepath.Join(home, "lnk")); len(sub) != 1 {
		t.Errorf("lnk lists %+v, want [f.txt]", sub)
	}
}

// In a boxed workspace the link is judged through os.Root: a folder inside the workspace is a folder, one outside it
// is not, whatever the host would allow.
func TestFsListSymlinkDirStrict(t *testing.T) {
	s, home, ws := boxedServer(t)
	mustMkdirAll(t, filepath.Join(ws, "in"))
	mustMkdirAll(t, filepath.Join(home, "hostdir"))
	mustSymlink(t, "in", filepath.Join(ws, "toin"))
	mustSymlink(t, "../hostdir", filepath.Join(ws, "tohost"))
	got := listEntries(t, s, ws)
	if e := got["toin"]; !e.Link || !e.TargetDir {
		t.Errorf("toin = %+v, want a link to a folder", e)
	}
	if e := got["tohost"]; !e.Link || e.TargetDir {
		t.Errorf("tohost = %+v, want a link that is not a folder (it leaves the workspace)", e)
	}
}

// Only the first maxLinkDirChecks links of one listing are resolved: a folder of many links costs a bounded number of
// walks. The rest still list, as links.
func TestFsListSymlinkCap(t *testing.T) {
	s, home, _ := boxedServer(t)
	mustMkdirAll(t, filepath.Join(home, "real"))
	dir := filepath.Join(home, "many")
	mustMkdirAll(t, dir)
	n := maxLinkDirChecks + 44
	for i := 0; i < n; i++ {
		mustSymlink(t, "../real", filepath.Join(dir, fmt.Sprintf("l%03d", i)))
	}
	got := listEntries(t, s, dir)
	links, dirs := 0, 0
	for _, e := range got {
		if e.Link {
			links++
		}
		if e.TargetDir {
			dirs++
		}
	}
	if links != n || dirs != maxLinkDirChecks {
		t.Errorf("links = %d, target_dir = %d; want %d and %d", links, dirs, n, maxLinkDirChecks)
	}
}

// Review round 1 (Important 3): a link whose own target text lies, by whole components, outside every served root and
// scratch root (and is no ancestor of one) is judged without walking it, so a link to a hung network mount
// (~/share -> /mnt/nfs/share) cannot hang the listing of the folder that holds it. A relative target is read from the
// listed folder. Only host listings walk targets at all (a boxed workspace's walk stops at the workspace).
func TestLinkMayLead(t *testing.T) {
	p := fsPolicy{roots: []string{"/home/u", "/srv/work"}, scratch: map[fileKey]string{{dev: 1, ino: 2}: "/tmp/claude-1000"}}
	for _, c := range []struct {
		dir, target string
		want        bool
	}{
		{"/home/u", "/mnt/nfs/share", false},
		{"/home/u", "/home/u/real", true},
		{"/home/u", "/home/u", true},
		{"/home/u", "/home", true}, // an ancestor of a root: the walk refuses it itself, cheaply
		{"/home/u", "/srv/work/x", true},
		{"/home/u", "/srv/workshop", false}, // whole components
		{"/home/u", "/tmp/claude-1000/a", true},
		{"/home/u/a", "../b", true},
		{"/home/u/a", "../../../mnt/nfs", false},
		{"/home/u/a", "real", true},
	} {
		if got := p.linkMayLead(c.dir, c.target); got != c.want {
			t.Errorf("linkMayLead(%q, %q) = %v, want %v", c.dir, c.target, got, c.want)
		}
	}
}

// Review round 2 (Minor 1): a link whose absolute target is written with the configured, unresolved spelling of a root
// ($HOME through a symlink, macOS's /tmp for /private/tmp) may lead in: it is walked, as a typed path would be.
func TestLinkMayLeadRawSpelling(t *testing.T) {
	p := fsPolicy{roots: []string{"/data/u"}, spellings: []string{"/home/u"}}
	if !p.linkMayLead("/data/u", "/home/u/proj") {
		t.Error("a target under home's configured spelling must be walked")
	}
	if p.linkMayLead("/data/u", "/home/someone") {
		t.Error("whole components still")
	}
}

func TestFsListSymlinkDirViaHomeSpelling(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(base, "var", "home", "u")
	mustMkdirAll(t, filepath.Join(real, "proj"))
	mustSymlink(t, filepath.Join(base, "var"), filepath.Join(base, "alias"))
	home := filepath.Join(base, "alias", "home", "u") // home as configured: through a link
	mustSymlink(t, filepath.Join(home, "proj"), filepath.Join(real, "abs_spelled"))
	s := &Server{Home: home}
	if e := listEntries(t, s, home)["abs_spelled"]; !e.Link || !e.TargetDir {
		t.Errorf("abs_spelled = %+v, want a link to a folder", e)
	}
}

// #364, Mac row MP4-1: an extra root is stored resolved (/private/tmp/labs on macOS), and a folder link whose absolute
// target is written through a link at the top of the filesystem (/tmp/labs/...) must still expand, as it does for home.
// On Linux a link base/tmp -> private/tmp stands in for macOS's /tmp, with base standing in for "/".
func TestFsListSymlinkDirViaTopLinkToExtraRoot(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(base, "private", "tmp", "labs")
	mustMkdirAll(t, filepath.Join(extra, "sub"))
	mustMkdirAll(t, filepath.Join(base, "private", "tmp", "other"))
	mustSymlink(t, "private/tmp", filepath.Join(base, "tmp"))
	home := filepath.Join(base, "Users", "u")
	mustMkdirAll(t, home)
	mustSymlink(t, filepath.Join(base, "tmp", "labs"), filepath.Join(home, "labs"))
	mustSymlink(t, filepath.Join(base, "tmp", "other"), filepath.Join(home, "other"))
	old := topLinkAliases
	topLinkAliases = func() []linkAlias { return readTopLinks(base) }
	t.Cleanup(func() { topLinkAliases = old })

	s := &Server{Home: home, Roots: fakeRoots{extra}}
	got := listEntries(t, s, home)
	if e := got["labs"]; !e.Link || !e.TargetDir {
		t.Errorf("labs = %+v, want a link to a folder (the extra root, through the top-level link)", e)
	}
	if e := got["other"]; !e.Link || e.TargetDir {
		t.Errorf("other = %+v, want a link that is not a folder the policy serves", e)
	}
}
