package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	boximage "github.com/culiops/culibox/docker/culibox"
	"github.com/culiops/culibox/internal/boximagecheck"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/datacompat"
)

const (
	// imageCheckFirst is the wait before the first pass: a daemon restarted in a
	// loop must not ask the registry each time, and boot has enough to do.
	imageCheckFirst = 2 * time.Minute
	// imageCheckTick is how often the pass is offered the chance to run; the
	// check itself runs at most once a day (imageCheckEvery).
	imageCheckTick = time.Hour
	// imageCheckEvery is the most often this process calls the check. The cache
	// file makes it due once a day, but a cache that cannot be saved is due on
	// every call, and that must not become a request per tick.
	imageCheckEvery = 24 * time.Hour
	// imageCheckPassTimeout bounds one pass; the client has the same bound per request.
	imageCheckPassTimeout = 30 * time.Second
	// boxImageFactsTimeout bounds the two docker calls behind GET /api/box-image.
	boxImageFactsTimeout = 5 * time.Second
)

// runImageCheck runs pass in the background, first after first and then on every
// tick of every, and returns the step that cancels it and waits (bounded) for it
// to finish.
func runImageCheck(pass func(context.Context), first, every time.Duration) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTimer(first)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pass(ctx)
				t.Reset(every)
			}
		}
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(retentionStopWait):
		}
	}
}

// imageInputs is what Evaluate and NeedsNetwork decide from, minus the daemon's
// facts and the cache, which each caller adds. It comes from the config the
// server already holds.
func imageInputs(cfg config.Config) boximagecheck.Inputs {
	return boximagecheck.Inputs{
		Configured: cfg.BoxImageOrDefault(),
		Explicit:   cfg.BoxImage != "",
		Published:  publishedBoxImage(),
		CheckOn:    cfg.ImageCheckOn(),
	}
}

// newImageChecker is the anonymous check's client: the default transport honours
// HTTPS_PROXY, and nothing it sends carries a credential.
func newImageChecker() boximagecheck.Checker {
	return boximagecheck.Checker{
		Client:    &http.Client{Timeout: imageCheckPassTimeout},
		Registry:  "https://ghcr.io",
		GitHubAPI: "https://api.github.com",
	}
}

// imageCheckPass is one background pass of the daily check. It never touches
// docker, so its inputs carry no daemon facts. It asks at most once per
// imageCheckEvery whatever the outcome, and logs a line only when what went wrong
// differs from the last time: the class, never an answer body or an error text.
func imageCheckPass(cfg config.Config, dataDir string, k boximagecheck.Checker, logf func(string, ...any)) func(context.Context) {
	now := k.Now
	if now == nil {
		now = time.Now
	}
	var lastAsked time.Time
	var lastClass string
	return func(ctx context.Context) {
		in := imageInputs(cfg)
		if !boximagecheck.NeedsNetwork(in) {
			return
		}
		start := now()
		if !lastAsked.IsZero() && start.Sub(lastAsked) < imageCheckEvery {
			return
		}
		ctx, cancel := context.WithTimeout(ctx, imageCheckPassTimeout)
		defer cancel()
		c, err := boximagecheck.RunIfDue(ctx, k, dataDir, in, false)
		// A fresh answer is one stamped at or after the start of this pass; a cache
		// that was merely not due is older, and must not use up the day.
		asked := c != nil && !c.CheckedAt.Before(start)
		if asked {
			lastAsked = start
		}
		if !asked && err == nil {
			return
		}
		class := imageCheckClass(c, asked, err)
		if class != "" && class != lastClass {
			logf("image check: %s", class)
		}
		lastClass = class
	}
}

// imageCheckClass names what went wrong in a pass, from short fixed strings only.
func imageCheckClass(c *boximagecheck.Cache, asked bool, err error) string {
	var parts []string
	switch {
	case errors.Is(err, datacompat.ErrNewer):
		parts = append(parts, "image-check.json is from a newer CuliBox, left as it is")
	case err != nil:
		parts = append(parts, "could not save image-check.json")
	}
	if asked {
		if c.RegistryErr != "" {
			parts = append(parts, c.RegistryErr)
		}
		if c.ScanErr != "" {
			parts = append(parts, c.ScanErr)
		}
	}
	return strings.Join(parts, "; ")
}

// imageFacts is the part of the docker CLI the box image report reads.
type imageFacts interface {
	ServerArch(ctx context.Context) (string, error)
	Describe(ctx context.Context, image string) (boximage.ImageInfo, error)
}

// boxImageHook builds web.Server.BoxImage: the running image's facts from two
// docker calls under one short deadline, the cache from disk, the verdict from
// both. d is nil without a box layer. A daemon that cannot answer in time leaves
// the facts unknown; the want is still computed. It never touches the network.
func boxImageHook(cfg config.Config, dataDir string, d imageFacts) func(context.Context) boximagecheck.Report {
	return func(ctx context.Context) boximagecheck.Report {
		in := imageInputs(cfg)
		if d != nil {
			fctx, cancel := context.WithTimeout(ctx, boxImageFactsTimeout)
			defer cancel()
			if arch, err := d.ServerArch(fctx); err == nil {
				if info, err := d.Describe(fctx, in.Configured); err == nil {
					in.Arch, in.Info = arch, &info
				}
			}
		}
		if c, err := boximagecheck.LoadCache(dataDir); err == nil {
			in.Cache = c
		}
		return boximagecheck.Evaluate(in)
	}
}

// layerImageFacts is the box layer's docker as imageFacts; nil without a layer
// (a typed nil must not reach the hook as a non-nil interface).
func layerImageFacts(l *boxLayer) imageFacts {
	if l == nil {
		return nil
	}
	return l.img
}
