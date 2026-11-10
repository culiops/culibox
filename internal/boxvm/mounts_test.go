package boxvm

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/vmshares"
)

func TestMountsAreDataSubfoldersAndShares(t *testing.T) {
	c := testCfg()
	c.Root = "/r"
	c.Shares = []vmshares.Share{{Path: "/Users/a/b", Mode: vmshares.RW}, {Path: "/Users/a/a", Mode: vmshares.RO}}
	want := []Mount{{"/r/boxstate", true}, {"/r/bundle", false}, {"/r/broker", false}, {"/r/ca", false},
		{"/Users/a/a", false}, {"/Users/a/b", true}}
	if got := c.Mounts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Mounts = %v, want %v", got, want)
	}
	for _, m := range c.Mounts() {
		if m.Location == c.Home {
			t.Fatalf("home must not be mounted: %v", m)
		}
	}
}

func TestMountsNeverExposeDataRootControlFiles(t *testing.T) {
	c := testCfg()
	c.Shares = []vmshares.Share{{Path: "/Users/a/work", Mode: vmshares.RW}}
	for _, ctl := range []string{c.Root + "/vm", c.Root + "/docker", c.Root + "/profiles.json"} {
		for _, m := range c.Mounts() {
			if vmshares.Within(ctl, m.Location, false) || vmshares.Within(m.Location, ctl, false) {
				t.Fatalf("mount %q overlaps control path %q", m.Location, ctl)
			}
		}
	}
	// The root itself is the parent of the four subfolders by design; what must
	// hold is that no mount contains it (that would expose everything in it).
	for _, m := range c.Mounts() {
		if vmshares.Within(c.Root, m.Location, false) {
			t.Fatalf("mount %q contains the data root", m.Location)
		}
	}
}

func v032Config() Config {
	c := testCfg()
	c.Root = "/Volumes/fast/ab"
	return c
}

func TestYAMLAllRWIsByteIdenticalToV032(t *testing.T) {
	want, err := os.ReadFile("testdata/v032-colima.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c := v032Config()
	got, err := renderYAML(c, []Mount{{c.Home, true}, {c.Root, true}})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("all-rw YAML drifted from v0.32:\n%s", got)
	}
}

func TestYAMLWritesWritableFalse(t *testing.T) {
	c := testCfg()
	c.Shares = []vmshares.Share{{Path: "/Users/a/ro", Mode: vmshares.RO}}
	y, err := c.YAML()
	if err != nil {
		t.Fatal(err)
	}
	s := string(y)
	for _, w := range []string{"  - location: \"" + c.Root + "/bundle\"\n    writable: false\n",
		"  - location: \"" + c.Root + "/boxstate\"\n    writable: true\n",
		"  - location: \"/Users/a/ro\"\n    writable: false\n"} {
		if !strings.Contains(s, w) {
			t.Fatalf("missing %q in\n%s", w, s)
		}
	}
}

func TestReadMountsRoundTrip(t *testing.T) {
	c := testCfg()
	c.Shares = []vmshares.Share{{Path: "/Users/a/ro", Mode: vmshares.RO}, {Path: "/Users/a/my work", Mode: vmshares.RW}}
	y, err := c.YAML()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "colima.yaml")
	if err := os.WriteFile(p, y, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMounts(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, c.Mounts()) {
		t.Fatalf("ReadMounts = %v, want %v", got, c.Mounts())
	}
	// the v0.32 file reads back as all-rw
	old, err := ReadMounts("testdata/v032-colima.yaml")
	if err != nil || !reflect.DeepEqual(old, []Mount{{"/Users/a", true}, {"/Volumes/fast/ab", true}}) {
		t.Fatalf("v032 = %v, %v", old, err)
	}
}

func TestReadMountsRejectsOtherShapes(t *testing.T) {
	for name, body := range map[string]string{
		"block-style":   "mounts:\n  - {location: \"/a\", writable: true}\nprovision:\n",
		"no writable":   "mounts:\n  - location: \"/a\"\nprovision:\n",
		"unquoted":      "mounts:\n  - location: /a\n    writable: true\nprovision:\n",
		"bad bool":      "mounts:\n  - location: \"/a\"\n    writable: yes\nprovision:\n",
		"no mounts key": "cpu: 4\nprovision:\n",
		"stray line":    "mounts:\n  - location: \"/a\"\n    writable: true\n    extra: 1\nprovision:\n",
		"never ends":    "mounts:\n  - location: \"/a\"\n    writable: true\n",
	} {
		p := filepath.Join(t.TempDir(), "c.yaml")
		os.WriteFile(p, []byte(body), 0o600)
		if m, err := ReadMounts(p); err == nil {
			t.Errorf("%s: accepted, got %v", name, m)
		}
	}
	if _, err := ReadMounts(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("missing file accepted")
	}
}

func TestNewVMResolvesShares(t *testing.T) {
	real := shortRoot(t)
	link := real + "-link"
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(link) })
	c := testCfg()
	c.Shares = []vmshares.Share{{Path: filepath.Join(link, "proj"), Mode: vmshares.RW}}
	got := NewVM(c, &fakeRunner{}).Config().Shares
	resolved, _ := filepath.EvalSymlinks(real)
	if len(got) != 1 || got[0].Path != filepath.Join(resolved, "proj") || got[0].Mode != vmshares.RW {
		t.Fatalf("shares not resolved: %v", got)
	}
	if c.Shares[0].Path != filepath.Join(link, "proj") {
		t.Fatal("the caller's slice was modified")
	}
}

func TestEnsureDataMountDirs(t *testing.T) {
	root := t.TempDir()
	if err := EnsureDataMountDirs(root); err != nil {
		t.Fatal(err)
	}
	for _, m := range DataMounts(root) {
		fi, err := os.Stat(m.Location)
		if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v %v", m.Location, fi, err)
		}
	}
}
