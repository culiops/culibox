package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/culiops/agent-box/internal/config"
)

func TestNewCloneHooksLinuxOnly(t *testing.T) {
	none := func() []config.Clone { return nil }
	home := t.TempDir()

	if h, err := newCloneHooks("darwin", &boxLayer{}, home, "", nil, none); h != nil || err != nil {
		t.Fatalf("darwin: got %v, %v; want nil, nil", h, err)
	}
	if h, err := newCloneHooks("linux", nil, home, "", nil, none); h != nil || err != nil {
		t.Fatalf("linux without a layer: got %v, %v; want nil, nil", h, err)
	}
	h, err := newCloneHooks("linux", &boxLayer{}, home, "", nil, none)
	if err != nil {
		t.Fatal(err)
	}
	if h == nil {
		t.Fatal("linux with a layer: want hooks, got nil")
	}
}

func TestCloneHooksBoxOwnedReadsCurrentProfiles(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "work", "clone")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cur := []config.Clone(nil)
	h, err := newCloneHooks("linux", &boxLayer{}, home, "", nil, func() []config.Clone { return cur })
	if err != nil {
		t.Fatal(err)
	}
	if why, owned := h.BoxOwned(dir); owned {
		t.Fatalf("no clone profile yet, but %s is owned: %s", dir, why)
	}
	// A profile saved after boot: the next call must see it.
	cur = []config.Clone{{Of: filepath.Join(home, "repo"), Dir: dir}}
	if _, owned := h.BoxOwned(dir); !owned {
		t.Fatalf("%s should be box-owned once a profile clones into it", dir)
	}
	cur = nil
	if _, owned := h.BoxOwned(dir); owned {
		t.Fatalf("%s should be free again after the profile is gone", dir)
	}
}
