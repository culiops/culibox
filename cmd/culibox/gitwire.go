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
