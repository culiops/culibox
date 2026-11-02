package clone

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
)

// Return is auto-return (spec §6.4 and the work-home design): it asks the clone
// which branches it has, and brings each one the host does not have to clone_of
// as box/<branch> through Fetch, so every guard of the bundle path applies to
// each. What it could not bring is returned, and written to the return state
// the UI reads. Like Fetch, the host reads the clone only through a container
// with no network; host git runs in clone_of alone.

const (
	maxListBytes      = 64 << 10 // the listing is cut here; a torn last line is dropped
	maxListBranches   = 200
	maxReturnAttempts = 20 // fetches one run starts; the rest wait for the next run
	// A listing line is "<sha> refs/heads/<name>": the longest valid one is a
	// sha256 id and a maxBranchLen name. A longer line is not one.
	maxListLine = 64 + len(" refs/heads/") + maxBranchLen

	noBaseDetail = "host repo has neither main nor master"
)

type ReturnMode int

const (
	ReturnAuto   ReturnMode = iota // FastForwardOnly fetches
	ReturnForced                   // today's forced update ("Bring work home now")
	ReturnDry                      // auto-return off: compute what is not home, fetch nothing
)

type ReturnRequest struct {
	Clone                                   config.Clone
	Mode                                    ReturnMode
	Image, Instance, TempDir, Home, DataDir string // as in FetchRequest
	Protected                               []box.ProtectedDir
}

type ReturnResult struct {
	At       time.Time
	Base     string
	Home     []string  // "box/<branch>" refs moved this run
	NotHome  []NotHome // what is not home after this run
	Warnings []string  // FetchResult.Warnings of the branches that came, "box/<b>: " prefixed
	Error    string    // listing or host checks failed; NotHome is then the previous state's
}

// ListArgs is the docker argv (after "docker") of the listing container. It has
// the isolation of BundleArgs and mounts nothing but the clone, read-only:
// clone_of is not needed to list refs. The git config flags are belt and braces
// over a read-only for-each-ref.
func ListArgs(r ReturnRequest, name string) []string {
	dir := r.Clone.Dir
	return []string{
		"run", "--rm", "--init", "--name", name, "--pull=never", "--network", "none", "--cap-drop=ALL", "--security-opt", "no-new-privileges", "--read-only",
		"--tmpfs", boxTmp + ":rw,nosuid,nodev,noexec,size=64m", "--pids-limit", "128", "--memory", "1g",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"--label", LabelCloneList + "=" + r.Instance,
		"-e", "HOME=" + boxTmp, "-e", "GIT_CONFIG_NOSYSTEM=1",
		"--mount", "type=bind,src=" + dir + ",dst=" + dir + ",readonly",
		"--entrypoint", "git", r.Image,
		"-C", dir, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false",
		"for-each-ref", "--format=%(objectname) %(refname)", "refs/heads/",
	}
}

// Return runs one auto-return pass over r.Clone. It is best effort: every
// failure lands in the result, never in an error. When r.DataDir is set the
// outcome is written to the clone's return state.
func Return(ctx context.Context, g Git, d Docker, r ReturnRequest) ReturnResult {
	res := ReturnResult{At: time.Now()}
	var prev ReturnState
	if r.DataDir != "" {
		if st, ok, err := ReadReturnState(r.DataDir, r.Clone.Dir); err == nil && ok {
			prev = st
		}
	}
	returnBranches(ctx, g, d, r, prev, &res)
	if res.Error != "" {
		// Nothing was checked, so nothing is learned: what was not home stays so.
		res.Home, res.NotHome = nil, slices.Clone(prev.NotHome)
		if res.Base == "" {
			res.Base = prev.Base
		}
	} else {
		slices.Sort(res.Home)
		slices.SortFunc(res.NotHome, func(a, b NotHome) int { return strings.Compare(a.Branch, b.Branch) })
	}
	if r.DataDir != "" {
		st := ReturnState{At: res.At, Base: res.Base, NotHome: res.NotHome, Error: res.Error}
		if err := WriteReturnState(r.DataDir, r.Clone.Dir, st); err != nil {
			res.Warnings = append(res.Warnings, "could not record what is not home: "+sanitizeN(err.Error(), maxErrRunes))
		}
	}
	return res
}

type listedBranch struct{ name, sha string }

func returnBranches(ctx context.Context, g Git, d Docker, r ReturnRequest, prev ReturnState, res *ReturnResult) {
	of := r.Clone.Of
	stopped := func() string {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "timed out"
		}
		return "cancelled"
	}
	notHome := func(branch, reason, detail string) {
		res.NotHome = append(res.NotHome, NotHome{Branch: branch, Reason: reason, Detail: detail})
	}

	if r.Image == "" || strings.HasPrefix(r.Image, "-") {
		res.Error = fmt.Sprintf("the box image %q is not usable", sanitize(r.Image))
		return
	}
	if err := checkMounts(r.Clone, r.Home, r.Protected); err != nil {
		res.Error = sanitizeN(err.Error(), maxLongErr)
		return
	}

	// 1. What the clone has, from a container: the host never opens its .git.
	name := containerName("clonelist", r.Instance)
	out := &capBuffer{max: maxListBytes}
	if err := runContainer(ctx, d, name, ListArgs(r, name), out); err != nil {
		if ctx.Err() != nil {
			res.Error = stopped()
		} else {
			res.Error = "listing the clone's branches failed: " + sanitizeN(err.Error(), maxLongErr)
		}
		return
	}
	entries, cut := parseList(out.buf.String(), res)
	if cut || out.over {
		res.Warnings = append(res.Warnings, fmt.Sprintf("the clone's branch list was cut at %d branches or %d bytes; some branches were not looked at",
			maxListBranches, maxListBytes))
	}

	// 2. Which of them the host already has.
	bb, _, err := BaseBranch(ctx, g, of)
	if err != nil {
		if ctx.Err() != nil {
			res.Error = stopped()
		} else {
			res.Error = "could not resolve the host base branch: " + sanitizeN(err.Error(), maxLongErr)
		}
		return
	}
	res.Base = bb
	var todo []string
	for _, e := range entries {
		if ctx.Err() != nil {
			res.Error = stopped()
			return
		}
		in := HeadInHost(ctx, g, of, e.sha)
		switch {
		case in == nil && ctx.Err() != nil:
			res.Error = stopped()
			return
		case in == nil:
			notHome(e.name, ReasonFailed, "could not check")
		case !*in:
			todo = append(todo, e.name)
		}
	}

	switch {
	case r.Mode == ReturnDry:
		for _, b := range todo {
			notHome(b, ReasonOff, "")
		}
		return
	case bb == "":
		for _, b := range todo {
			notHome(b, ReasonRefused, noBaseDetail)
		}
		return
	}

	// 3. Bring them. A branch that was refused for a reason that waits on a
	// person goes last, so it cannot use up the attempts of the others.
	waits := func(b string) bool {
		for _, n := range prev.NotHome {
			if n.Branch == b {
				return n.Reason == ReasonNotFF || n.Reason == ReasonCheckedOut || n.Reason == ReasonRefused
			}
		}
		return false
	}
	slices.SortStableFunc(todo, func(a, b string) int {
		switch wa, wb := waits(a), waits(b); {
		case wa == wb:
			return 0
		case wa:
			return 1
		}
		return -1
	})
	attempts := 0
	for _, b := range todo {
		if ctx.Err() != nil {
			notHome(b, ReasonDeferred, "the run ran out of time")
			continue
		}
		if attempts >= maxReturnAttempts {
			notHome(b, ReasonDeferred, fmt.Sprintf("at most %d branches are brought per run", maxReturnAttempts))
			continue
		}
		attempts++
		fr, err := Fetch(ctx, g, d, FetchRequest{
			Clone: r.Clone, Branch: b, Base: bb, Image: r.Image, Instance: r.Instance, TempDir: r.TempDir,
			Home: r.Home, Protected: r.Protected, DataDir: r.DataDir,
			FastForwardOnly:  r.Mode == ReturnAuto,
			RefuseCheckedOut: true,
		})
		var refusal *Refusal
		switch {
		case err == nil:
		case fr.Ref != "":
			// The ref moved and a later step (listing what came) failed: it is home.
			fr.Warnings = append(fr.Warnings, sanitizeN(err.Error(), maxLongErr))
		case ctx.Err() != nil:
			notHome(b, ReasonDeferred, "the run ran out of time")
			continue
		case errors.As(err, &refusal):
			if refusal.Reason != reasonEmpty { // nothing to fetch: already home
				notHome(b, refusal.Reason, sanitizeN(err.Error(), maxErrRunes))
			}
			continue
		default:
			notHome(b, ReasonFailed, sanitizeN(err.Error(), maxErrRunes))
			continue
		}
		res.Home = append(res.Home, "box/"+b)
		for _, w := range fr.Warnings {
			res.Warnings = append(res.Warnings, "box/"+b+": "+w)
		}
	}
}

// parseList reads the listing: complete lines only, each "<sha> refs/heads/<name>",
// and drops everything else. A name that is not a safe branch name is recorded
// in res as ReasonName and not returned. cut is true when lines were lost to the
// cap or to a torn last line.
func parseList(text string, res *ReturnResult) (entries []listedBranch, cut bool) {
	seen := map[string]bool{}
	for text != "" {
		line, rest, complete := strings.Cut(text, "\n")
		if !complete {
			return entries, true
		}
		text = rest
		if len(line) > maxListLine {
			continue
		}
		sha, ref, ok := strings.Cut(line, " ")
		name, isHead := strings.CutPrefix(ref, "refs/heads/")
		if !ok || !shaRE.MatchString(sha) || !isHead || name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if len(seen) > maxListBranches {
			return entries, true
		}
		if err := ValidBranch(name); err != nil {
			res.NotHome = append(res.NotHome, NotHome{Branch: sanitize(name), Reason: ReasonName, Detail: sanitizeN(err.Error(), maxErrRunes)})
			continue
		}
		entries = append(entries, listedBranch{name: name, sha: sha})
	}
	return entries, false
}
