package bundle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/agent-box/internal/box"
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
	cg, err := p.For("claude", true)
	if err != nil {
		t.Fatal(err)
	}
	cm, cf, ch := cg.Mounts, cg.Flags, cg.Hash
	for _, bin := range []string{"bash", "/usr/bin/zsh", ""} {
		bg, err := p.For(bin, true)
		if err != nil {
			t.Fatal(err)
		}
		bm, bf, bh := bg.Mounts, bg.Flags, bg.Hash
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
	g, err := providerForTest(t).For("claude", true)
	if err != nil {
		t.Fatal(err)
	}
	ms := g.Mounts
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
		g, err := p.For(bin, true)
		if err != nil {
			t.Fatal(err)
		}
		ms := g.Mounts
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

// Plugins are mounted live from the host, so their drops cannot live in the
// bundle lock; the Grant carries the lock's drops first, then the plugin ones.
func TestProviderGrantCarriesPluginsAndLockDrops(t *testing.T) {
	src := realisticSource(t)
	pluginTree(t, src)
	snapRoot := t.TempDir()
	mk := func() *Provider {
		m, err := New(Options{Source: src, Root: snapRoot, MCPFile: src + "/../.claude.json", Prober: fakeProber{"npx": true, "node": true}})
		if err != nil {
			t.Fatal(err)
		}
		return NewProvider(m)
	}
	p := mk()
	g, err := p.For("claude", true)
	if err != nil {
		t.Fatal(err)
	}
	has := map[string]box.Mount{}
	for _, mt := range g.Mounts {
		has[mt.Box] = mt
	}
	for _, boxPath := range pluginBoxPaths {
		got, ok := has[boxPath]
		if !ok || !got.Optional {
			t.Fatalf("grant lacks optional plugin mount %s (have %+v)", boxPath, g.Mounts)
		}
		if !strings.HasPrefix(got.Host, filepath.Join(snapRoot, "plugins-")) {
			t.Fatalf("%s must be served from the snapshot, not the host dir: %s", boxPath, got.Host)
		}
	}
	if _, ok := has[BoxBundleDir]; !ok || len(g.Mounts) < 4+2 {
		t.Fatalf("the bundle mounts must still be there: %+v", g.Mounts)
	}
	built, err := p.m.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if len(built.Lock.Dropped) == 0 {
		t.Fatal("fixture must drop something (hooks, env) for this test to mean anything")
	}
	if len(g.Drops) != len(built.Lock.Dropped) {
		t.Fatalf("complete set: drops are the lock's alone: %+v", g.Drops)
	}
	for i, d := range built.Lock.Dropped {
		if g.Drops[i] != d {
			t.Fatalf("drop %d = %+v, want the lock's %+v", i, g.Drops[i], d)
		}
	}

	// A broken set: lock drops first, then exactly one plugin drop.
	if err := os.RemoveAll(filepath.Join(src, "plugins", "cache")); err != nil {
		t.Fatal(err)
	}
	g2, err := p.For("claude", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, mt := range g2.Mounts {
		if strings.HasPrefix(mt.Box, "/home/agent/.claude/plugins") {
			t.Fatalf("an incomplete set must mount nothing: %+v", mt)
		}
	}
	b2, err := p.m.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	n := len(b2.Lock.Dropped)
	if len(g2.Drops) != n+1 || g2.Drops[n].Kind != KindPlugins || g2.Drops[n].Name != "*" {
		t.Fatalf("want lock drops then one plugins drop, got %+v", g2.Drops)
	}
	if g2.Drops[n].Reason != ReasonPluginsIncomplete {
		t.Fatalf("reason %q", g2.Drops[n].Reason)
	}

	// A bin without a layout gets the same mounts, all optional, and no flags.
	pluginTree(t, src)
	gc, _ := p.For("claude", true)
	gb, err := p.For("bash", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(gb.Flags) != 0 || len(gb.Mounts) != len(gc.Mounts) {
		t.Fatalf("bash: flags %v mounts %d, want none / %d", gb.Flags, len(gb.Mounts), len(gc.Mounts))
	}
	for _, mt := range gb.Mounts {
		if !mt.Optional {
			t.Fatalf("a shell must never be refused a box over %+v", mt)
		}
	}
}
