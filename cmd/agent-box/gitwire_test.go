package main

import (
	"slices"
	"testing"

	"github.com/culiops/agent-box/internal/clone"
	"github.com/culiops/agent-box/internal/gitview"
)

func TestGitViewForNilLayer(t *testing.T) {
	if g := gitViewFor(nil, 1, 2); g != nil {
		t.Fatalf("gitViewFor(nil) = %#v, want a nil interface", g)
	}
}

func TestGitViewForLayer(t *testing.T) {
	layer := &boxLayer{dockerBin: "/usr/bin/docker", dockerArgs: []string{"--context", "colima-agent-box"}, image: "ghcr.io/x/box:1"}
	r, ok := gitViewFor(layer, 1001, 1002).(*gitview.Runner)
	if !ok {
		t.Fatal("not a *gitview.Runner")
	}
	d, ok := r.Docker.(clone.ExecDocker)
	if !ok || d.Bin != "/usr/bin/docker" || !slices.Equal(d.Args, layer.dockerArgs) {
		t.Fatalf("docker = %#v", r.Docker)
	}
	if r.Image != "ghcr.io/x/box:1" || r.UID != 1001 || r.GID != 1002 {
		t.Fatalf("runner = %+v", r)
	}
}
