package gitview

import (
	"context"
	"log"
	"regexp"
	"strings"
	"time"
)

const (
	// sweepGrace is how old a stopped git container must be before a sweep
	// removes it. A live run sits in Created only between docker's create and
	// start, and every run ends within its 10s timeout, so an older one has
	// nobody left to start or remove it.
	sweepGrace = 2 * time.Minute
	// sweepDelay is how long after a failed run its sweep runs: past sweepGrace,
	// so the container a killed CLI left half-made is old enough by then.
	sweepDelay = 3 * time.Minute
	// sweepTimeout bounds the docker ps of one sweep.
	sweepTimeout = 30 * time.Second
	// maxSweepBytes bounds the listing; rows past it wait for the next sweep.
	maxSweepBytes = 1 << 20

	namePrefix = "agent-box-git-"
)

// gitName is exactly what newName makes, so a sweep never touches a container
// someone else named with the same prefix.
var gitName = regexp.MustCompile(`^` + namePrefix + `[0-9a-f]{16}$`)

// psFormat is the sweep's docker ps --format; Sweep reads it back.
const psFormat = "{{.Names}}\t{{.State}}\t{{.CreatedAt}}"

// psTime is the layout of docker ps {{.CreatedAt}}.
const psTime = "2006-01-02 15:04:05 -0700 MST"

// Sweep removes git containers that runs left behind: stopped (created, exited
// or dead) and older than sweepGrace. The run's own rm -f misses a container
// whose docker CLI was killed while the daemon was still creating it, and no rm
// runs at all when CuliBox stops between create and start; --rm never fires for
// a container that never started, and each one pins the box image it was made
// from. Running containers and young ones are kept, so a sweep is safe beside
// live runs, including another CuliBox's on the same docker. It returns how
// many it removed.
func (r *Runner) Sweep(ctx context.Context) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, sweepTimeout)
	defer cancel()
	out := &capBuffer{max: maxSweepBytes}
	err := r.Docker.RunStream(ctx, []string{"ps", "-a",
		"--filter", "name=^" + namePrefix,
		"--filter", "status=created", "--filter", "status=exited", "--filter", "status=dead",
		"--format", psFormat}, out)
	if err != nil && !out.hit {
		return 0, err
	}
	now := r.now()
	n := 0
	// Only whole lines: a capped listing ends mid-row.
	text := string(out.b)
	if i := strings.LastIndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	} else {
		text = ""
	}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 || !gitName.MatchString(f[0]) {
			continue
		}
		switch f[1] {
		case "created", "exited", "dead":
		default:
			continue
		}
		created, err := time.Parse(psTime, f[2])
		if err != nil || now.Sub(created) < sweepGrace {
			continue
		}
		r.remove(f[0])
		n++
	}
	return n, nil
}

// SweepLater runs one Sweep after d in the background. Calls while one is
// pending add nothing: that sweep lists every leftover anyway.
func (r *Runner) SweepLater(d time.Duration) {
	r.mu.Lock()
	if r.sweepPending {
		r.mu.Unlock()
		return
	}
	r.sweepPending = true
	r.mu.Unlock()
	after := r.AfterFunc
	if after == nil {
		after = func(d time.Duration, fn func()) { time.AfterFunc(d, fn) }
	}
	after(d, func() {
		r.mu.Lock()
		r.sweepPending = false
		r.mu.Unlock()
		if n, err := r.Sweep(context.Background()); err != nil {
			log.Printf("gitview: sweeping leftover git containers: %s", failReasonText(err.Error()))
		} else if n > 0 {
			log.Printf("gitview: removed %d leftover git container(s)", n)
		}
	})
}
