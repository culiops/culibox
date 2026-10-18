package bundle

import (
	"testing"
)

func providerForTest(t *testing.T) *Provider {
	t.Helper()
	src := realisticSource(t)
	m, err := New(Options{
		Source:  src,
		Root:    t.TempDir(),
		MCPFile: src + "/../.claude.json",
		Prober:  fakeProber{"npx": true, "node": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return NewProvider(m)
}

// brain #131: the box key does not include the agent binary, so a bash session
// and a claude session in one workspace share a box, and a box's mounts are
// fixed when it starts. Every bin therefore gets the SAME mounts; only the
// flags are the bin's own, so a shell is still never handed agent flags.
func TestEveryBinGetsTheSameBoxMounts(t *testing.T) {
	p := providerForTest(t)
	cm, cf, ch, err := p.For("claude")
	if err != nil {
		t.Fatal(err)
	}
	for _, bin := range []string{"bash", "/usr/bin/zsh", ""} {
		bm, bf, bh, err := p.For(bin)
		if err != nil {
			t.Fatal(err)
		}
		if len(bf) != 0 {
			t.Fatalf("%q must not get agent flags: %v", bin, bf)
		}
		if bh != ch {
			t.Fatalf("%q bundle hash %q, want %q", bin, bh, ch)
		}
		if len(bm) != len(cm) {
			t.Fatalf("%q mounts %+v, want claude's %+v", bin, bm, cm)
		}
		for i := range bm {
			if bm[i].Host != cm[i].Host || bm[i].Box != cm[i].Box {
				t.Fatalf("%q mount %d = %+v, want %+v", bin, i, bm[i], cm[i])
			}
		}
	}
	if len(cf) == 0 {
		t.Fatal("claude must still get its flags")
	}
}

// The flags point into the bundle root, so a box without it cannot run claude
// at all; the ~/.claude overlays are config the agent runs without. That is
// what lets the box layer refuse the first and tolerate the second on a live box.
// A shell reads none of it, so for a shell even the root is optional: it must
// never be refused a box over a mount it does not use (review round 1).
func TestOnlyTheBundleRootIsARequiredMount(t *testing.T) {
	ms, _, _, err := providerForTest(t).For("claude")
	if err != nil {
		t.Fatal(err)
	}
	root := 0
	for _, mt := range ms {
		if mt.Box == BoxBundleDir {
			root++
			if mt.Optional {
				t.Fatalf("the bundle root must be required: %+v", mt)
			}
		} else if !mt.Optional {
			t.Fatalf("a config overlay must be optional: %+v", mt)
		}
	}
	if root != 1 || len(ms) < 2 {
		t.Fatalf("want the root once plus the overlays, got %+v", ms)
	}
}

func TestNothingIsRequiredForABinWithoutALayout(t *testing.T) {
	p := providerForTest(t)
	for _, bin := range []string{"bash", "/usr/bin/zsh", ""} {
		ms, _, _, err := p.For(bin)
		if err != nil {
			t.Fatal(err)
		}
		if len(ms) == 0 {
			t.Fatalf("%q must still get the box mounts", bin)
		}
		for _, mt := range ms {
			if !mt.Optional {
				t.Fatalf("%q: %+v must be optional for a bin that never reads it", bin, mt)
			}
		}
	}
}
