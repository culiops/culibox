package main

import (
	"github.com/culiops/culibox/internal/clone"
	"github.com/culiops/culibox/internal/gitview"
	"github.com/culiops/culibox/internal/web"
)

// gitViewFor is the file tree's git runner: git in throwaway containers on the
// box layer's docker (with its darwin colima flags) and fallback box image.
// Without a layer there is no docker, and it returns a nil interface (never a
// typed nil) so the git endpoints answer "unavailable".
func gitViewFor(layer *boxLayer, uid, gid int) web.GitView {
	if layer == nil {
		return nil
	}
	return &gitview.Runner{
		Docker: clone.ExecDocker{Bin: layer.dockerBin, Args: layer.dockerArgs},
		Image:  layer.image,
		UID:    uid,
		GID:    gid,
	}
}

// sweepGitLeftovers removes, in the background, git containers an earlier run
// left behind: CuliBox stopping between docker's create and start leaves one in
// Created, pinning its box image (#254). It waits SweepDelay, so one the
// restart itself left is old enough by then. No-op without a git runner.
func sweepGitLeftovers(g web.GitView) {
	if r, ok := g.(*gitview.Runner); ok {
		r.SweepLater(gitview.SweepDelay)
	}
}
