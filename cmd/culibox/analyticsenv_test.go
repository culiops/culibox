package main

import (
	"errors"
	"reflect"
	"testing"

	"github.com/culiops/culibox/internal/analytics"
	"github.com/culiops/culibox/internal/config"
)

// The env is rebuilt from the live sources on every call, maps each profile's
// own fields, and tolerates a missing key function and an unreadable record.
func TestAnalyticsResolveEnv(t *testing.T) {
	list := []config.AgentConfig{{Name: "a", DefaultCwd: "/c/a", CloneOf: "/r/a", Account: "work"}}
	dirs := []string{"/c/a"}
	var dirsErr error
	f := analyticsResolveEnv(func() []config.AgentConfig { return list },
		func() ([]string, error) { return dirs, dirsErr }, nil, true)

	env := f()
	want := []analytics.ProfileRef{{Name: "a", DefaultCwd: "/c/a", CloneOf: "/r/a", Account: "work"}}
	if !reflect.DeepEqual(env.Profiles, want) || !reflect.DeepEqual(env.CloneDirs, dirs) {
		t.Fatalf("env = %+v, want profiles %+v and clone dirs %v", env, want, dirs)
	}
	if env.ConvKey != nil || env.RepoRoot == nil || !env.FoldCase {
		t.Fatalf("env = %+v, want nil ConvKey, a RepoRoot and FoldCase", env)
	}

	list = append(list, config.AgentConfig{Name: "b"})
	dirsErr = errors.New("malformed line")
	env = f()
	if len(env.Profiles) != 2 || len(env.CloneDirs) != 0 {
		t.Fatalf("second call = %+v, want the new profile and an empty clone-dirs record", env)
	}
}
