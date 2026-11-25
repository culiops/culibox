package main

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/boxedws"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
	"github.com/culiops/culibox/internal/session"
)

func TestBoxTreesFunc(t *testing.T) {
	data := t.TempDir()
	reg, _, err := boxedws.Open(data, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	recorded, live, clone, host := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	if err := reg.Add(recorded); err != nil {
		t.Fatal(err)
	}
	// What Manager.BoxedCwds hands over: boxed sessions' cwds only. Duplicates
	// of a recorded entry and empty cwds still arrive and must be dropped here.
	cwds := []string{live, recorded, ""}
	clones := []config.Clone{{Of: host, Dir: clone}, {Of: host, Dir: live}}
	fn := boxTreesFunc([]string{data}, reg, func() []string { return cwds }, func() []config.Clone { return clones })

	got := fn()
	if !slices.Equal(got.Denied, []string{data}) {
		t.Fatalf("Denied = %v, want [%s]", got.Denied, data)
	}
	want := []string{recorded, live, clone}
	slices.Sort(want)
	slices.Sort(got.Contained)
	if !slices.Equal(got.Contained, want) {
		t.Fatalf("Contained = %v, want %v", got.Contained, want)
	}

	// Evaluated per call: a session spawned later is covered at once.
	later := t.TempDir()
	cwds = append(cwds, later)
	if !slices.Contains(fn().Contained, later) {
		t.Fatalf("a later boxed session's cwd is missing from %v", fn().Contained)
	}
}

func TestSeedDirs(t *testing.T) {
	sessions := []session.Info{
		{Cwd: "/w/boxed", Runtime: config.RuntimeBox},
		{Cwd: "/w/host", Runtime: config.RuntimeHost},
	}
	clones := []config.Clone{{Of: "/r", Dir: "/w/clone"}}
	profiles := []string{"/w/profile", "/w/boxed"}
	boxes := []box.BoxInfo{
		{Name: "a", Workspace: "/w/fromdocker"},
		{Name: "b", Workspace: "/w/garbled", Unreadable: true},
		{Name: "c", Workspace: ""},
	}
	got := seedDirs(sessions, clones, profiles, boxes)
	want := []string{"/w/boxed", "/w/clone", "/w/profile", "/w/fromdocker"}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("seedDirs = %v, want %v", got, want)
	}
}

func TestBoxedRecorder(t *testing.T) {
	data := t.TempDir()
	reg, _, err := boxedws.Open(data, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	rec := boxedRecorder(reg, nil)
	if err := rec(""); err != nil {
		t.Fatal(err)
	}
	if got := reg.List(); len(got) != 0 {
		t.Fatalf("empty cwd recorded %v", got)
	}
	// A relative cwd is recorded as the absolute path it names from the daemon's cwd.
	dir := t.TempDir()
	t.Chdir(dir)
	if err := rec("."); err != nil {
		t.Fatal(err)
	}
	if got := reg.List(); len(got) != 1 || got[0] != dir {
		t.Fatalf("relative cwd recorded %v, want [%s]", got, dir)
	}
}

// seedCase runs seedBoxedRegistry against a fresh registry and returns what a
// later boot's Open would see.
func seedCase(t *testing.T, list func(context.Context) ([]box.BoxInfo, error)) (seeded bool, dirs []string) {
	t.Helper()
	data := t.TempDir()
	none := func(string) (string, bool) { return "", false }
	reg, _, err := boxedws.Open(data, none)
	if err != nil {
		t.Fatal(err)
	}
	profile := t.TempDir()
	seedBoxedRegistry(reg, nil, nil, []string{profile}, list)
	reg2, seeded, err := boxedws.Open(data, none)
	if err != nil {
		t.Fatal(err)
	}
	return seeded, reg2.List()
}

func TestSeedBoxedRegistryCompleteness(t *testing.T) {
	fromDocker := t.TempDir()
	t.Run("listing ok marks seeded and records docker's workspaces", func(t *testing.T) {
		seeded, dirs := seedCase(t, func(context.Context) ([]box.BoxInfo, error) {
			return []box.BoxInfo{{Name: "a", Workspace: fromDocker}}, nil
		})
		if !seeded || !slices.Contains(dirs, fromDocker) {
			t.Fatalf("seeded=%v dirs=%v", seeded, dirs)
		}
	})
	t.Run("listing failure stays unseeded so the next boot retries, but still records the rest", func(t *testing.T) {
		seeded, dirs := seedCase(t, func(context.Context) ([]box.BoxInfo, error) {
			return nil, errors.New("docker unreachable")
		})
		if seeded {
			t.Fatal("a failed docker listing must not mark the registry seeded")
		}
		if len(dirs) != 1 {
			t.Fatalf("the profile dir should still be recorded: %v", dirs)
		}
	})
	t.Run("no containment layer: nothing to list, complete", func(t *testing.T) {
		if seeded, _ := seedCase(t, nil); !seeded {
			t.Fatal("without a containment layer the seed is complete")
		}
	})
}

// Profiles from profiles.json reach the launcher only when the profile manager
// is built, after the first-boot seed ran. recordBoxedProfiles runs after that
// on every boot, so a boxed profile's directory is recorded even in a registry
// an earlier build already marked seeded with nothing in it.
func TestRecordBoxedProfiles(t *testing.T) {
	data := t.TempDir()
	none := func(string) (string, bool) { return "", false }
	reg, _, err := boxedws.Open(data, none)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Seed(nil, true); err != nil { // what the buggy boot left behind
		t.Fatal(err)
	}
	inPlace, clone, host, repo := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	l := launcher.New([]config.AgentConfig{{Name: "bash", Bin: "bash"}}, nil, nil)
	l.SetDefaultRuntime("") // unset: profiles without a runtime spawn boxed
	l.SetAgents([]config.AgentConfig{
		{Name: "bash", Bin: "bash"},
		{Name: "inplace", Bin: "claude", DefaultCwd: inPlace},
		{Name: "clone", Bin: "claude", Runtime: config.RuntimeBox, CloneOf: repo, DefaultCwd: clone},
		{Name: "host", Bin: "claude", Runtime: config.RuntimeHost, DefaultCwd: host},
	})

	recordBoxedProfiles(reg, l.Clones(), l.BoxedDefaultCwds())

	reg2, seeded, err := boxedws.Open(data, none)
	if err != nil {
		t.Fatal(err)
	}
	got := reg2.List()
	want := []string{clone, inPlace}
	for i, d := range want { // the registry stores resolved paths (/var -> /private/var on macOS)
		if want[i], err = filepath.EvalSymlinks(d); err != nil {
			t.Fatal(err)
		}
	}
	slices.Sort(got)
	slices.Sort(want)
	if !seeded || !slices.Equal(got, want) {
		t.Fatalf("seeded=%v recorded=%v, want seeded=true %v", seeded, got, want)
	}
}

// Removing a VM folder never drops a recorded boxed workspace: the registry
// feeds the fs containment trees, so its filter ignores the share rule.
func TestBoxedRegistryIgnoresShareRule(t *testing.T) {
	l, _, _ := darwinLayer(t, false)
	l.vm.SetShares(nil)
	ws := resolvedTemp(t)
	if _, bad := l.boxes.Unboxable(ws); !bad {
		t.Fatal("setup: the spawn rule must refuse a workspace outside every share")
	}
	data := t.TempDir()
	reg, _, err := boxedws.Open(data, boxedFilter(l))
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(ws); err != nil {
		t.Fatal(err)
	}
	reg2, _, err := boxedws.Open(data, boxedFilter(l))
	if err != nil {
		t.Fatal(err)
	}
	if got := reg2.List(); !slices.Contains(got, ws) {
		t.Fatalf("a recorded workspace outside every share was dropped: %v", got)
	}
	if boxedFilter(nil) == nil {
		t.Fatal("no layer must still give a filter")
	}
}
