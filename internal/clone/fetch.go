package clone

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
)

// The return path (spec §6.4, D9). Code leaves a clone only as a git bundle
// made inside a throwaway container with no network, whose only output is
// stdout into a temp file the host created. The host then verifies the bundle,
// strict-checks its pack with index-pack (git does not fsck a bundle fetch) and
// fetches it into clone_of. The host never runs git against
// the clone's .git: a planted commondir, alternates or symlinked ref there
// would otherwise pull an unrelated private repo into the main repo.

var (
	maxBundleBytes int64 = 512 << 20 // a bigger bundle is refused, not truncated
	fetchTimeout         = 10 * time.Minute
)

// quarantinePrefix is where a fetched branch waits in clone_of, as
// <prefix><clone hash>/<branch>, while its history is checked against the base
// record. Only a branch that passes moves to box/<branch>; the quarantine ref
// is deleted on every path out.
const quarantinePrefix = "refs/agent-box/quarantine/"

// quarantineCleanup bounds the deferred delete of the quarantine ref. It runs
// on a fresh context, so a cancelled or timed-out fetch still removes it.
const quarantineCleanup = 10 * time.Second

// boxTmp is the bundle container's own tmpfs and HOME, not a host path.
const boxTmp = "/tmp"

// maxDockerStderr caps the stderr kept from the docker CLI; the error carries
// the last maxErrRunes of it, sanitized.
const (
	maxDockerStderr = 8 << 10
	maxErrRunes     = 200
	maxLongErr      = 600
	maxWarnPaths    = 10
)

var (
	shaRE  = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	hexRE  = regexp.MustCompile(`^[0-9a-f]{4,64}$`)
	attrRE = regexp.MustCompile(`(?i)co-authored-by:|generated with`)

	errBundleTooBig = errors.New("bundle too large")
)

// Docker runs one docker CLI invocation, streaming its stdout to a writer.
type Docker interface {
	RunStream(ctx context.Context, args []string, stdout io.Writer) error
	// Remove force-removes the named container; one already gone is not an error.
	Remove(ctx context.Context, name string) error
}

// ExecDocker is the real Docker, running the docker binary at Bin (resolve it
// with box.LookDocker). Args are docker's global flags (for example
// "--context", "colima-agent-box"), put before the subcommand of every
// invocation; the zero value adds nothing.
type ExecDocker struct {
	Bin  string
	Args []string
}

// argv is the full argument list for one invocation: the global Args, then args.
func (d ExecDocker) argv(args ...string) []string {
	return append(slices.Clone(d.Args), args...)
}

// RunStream runs docker with args. stdout is copied to the writer as it
// arrives, never held whole in memory. stderr keeps its last 8 KiB (the final
// "fatal:" line is the useful one), and the error carries only its last 200
// runes, sanitized (box-written text). A
// failing writer or a cancelled ctx stops the container (SIGTERM, which the
// docker CLI forwards, then a kill after 10s) and leaves no goroutine behind.
func (d ExecDocker) RunStream(ctx context.Context, args []string, stdout io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, d.Bin, d.argv(args...)...)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	sink := &stopOnError{w: stdout, stop: cancel}
	errb := &tailBuffer{max: maxDockerStderr}
	cmd.Stdout, cmd.Stderr = sink, errb
	err := cmd.Run()
	if sink.err != nil {
		return fmt.Errorf("docker run: writing its output: %w", sink.err)
	}
	if err != nil {
		return fmt.Errorf("docker run: %w: %s", err, sanitize(tailRunes(strings.TrimSpace(errb.String()), maxErrRunes)))
	}
	return nil
}

// stopOnError remembers the first write error and stops the command, so a full
// sink does not leave the child blocked on a pipe nobody reads.
type stopOnError struct {
	w    io.Writer
	stop func()
	err  error
}

func (s *stopOnError) Write(p []byte) (int, error) {
	n, err := s.w.Write(p)
	if err != nil {
		if s.err == nil {
			s.err = err
		}
		s.stop()
	}
	return n, err
}

// tailBuffer keeps the last max bytes written and swallows the rest.
type tailBuffer struct {
	b   []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 2*t.max { // amortised: trim in chunks, not on every write
		t.b = append(t.b[:0], t.b[len(t.b)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	if len(t.b) > t.max {
		return string(t.b[len(t.b)-t.max:])
	}
	return string(t.b)
}

func tailRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

// FetchRequest is one `clone fetch`.
type FetchRequest struct {
	Clone    config.Clone
	Branch   string // branch in the clone to bring home
	Base     string // branch in clone_of the range starts after (usually main)
	Image    string // the box image the bundle container runs
	Instance string // this daemon's instance label
	TempDir  string // where the bundle temp file is made (under the data root)

	Home      string             // the host user's home (CheckOf)
	Protected []box.ProtectedDir // dirs no mount may be, contain or sit in

	DataDir         string // the host data dir that holds the clone's base record
	AllowOldHistory bool   // -allow-old-history: let a known stale or unverified history through, loudly

	// FastForwardOnly is auto-return only: box/<Branch> is created, or moved
	// forward, and never overwritten or moved while checked out. False is the
	// forced update the CLI and "Bring work home now" use (the latter also
	// sets RefuseCheckedOut).
	FastForwardOnly bool

	// RefuseCheckedOut refuses a box/<Branch> that a worktree of clone_of has
	// checked out, even when the update is forced: moving it would leave that
	// work tree at the old commit, so git status shows the new work as reverted
	// changes. Return sets it for every mode that fetches; the CLI leaves it
	// false. FastForwardOnly implies it.
	RefuseCheckedOut bool
}

// Refusal is a Fetch error with a reason Return records. Error() is the
// wrapped error's text, unchanged, so every existing message and CLI test stays.
type Refusal struct {
	Reason string
	Err    error
}

func (r *Refusal) Error() string { return r.Err.Error() }
func (r *Refusal) Unwrap() error { return r.Err }

// reasonEmpty is "nothing to fetch": the branch is already home. Return never records it.
const reasonEmpty = "empty"

func refused(reason string, err error) error { return &Refusal{Reason: reason, Err: err} }

// FetchResult is what came home.
type FetchResult struct {
	Ref      string // refs/heads/box/<Branch> in clone_of
	BaseSHA  string
	Commits  []string // "<hash> <subject>", newest first, sanitized
	Warnings []string
}

// BundleArgs is the docker argv (after "docker") of the bundle container: no
// network, no capabilities, a read-only rootfs, the clone and clone_of's .git
// mounted read-only, and no box label (a box label would make claimCheck read
// it as a box, spec correction 9). --pull=never: a deleted pinned tag must fail,
// never be pulled from a registry by name. GIT_ALTERNATE_OBJECT_DIRECTORIES lets the
// range ^<baseSHA> resolve when clone_of's base moved past what the clone has
// (correction 7). --init puts a reaper at PID 1, so the container ends on a
// stop signal; --name lets a failed run be removed (see runContainer).
func BundleArgs(r FetchRequest, baseSHA, name string) []string {
	of, dir := r.Clone.Of, r.Clone.Dir
	gitDir := of + "/.git"
	return []string{
		"run", "--rm", "--init", "--name", name, "--pull=never", "--network", "none", "--cap-drop=ALL", "--security-opt", "no-new-privileges", "--read-only",
		"--tmpfs", boxTmp + ":rw,nosuid,nodev,noexec,size=64m", "--pids-limit", "128", "--memory", "1g",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"--label", LabelCloneFetch + "=" + r.Instance,
		"-e", "HOME=" + boxTmp, "-e", "GIT_CONFIG_NOSYSTEM=1",
		"-e", "GIT_ALTERNATE_OBJECT_DIRECTORIES=" + gitDir + "/objects",
		"--mount", "type=bind,src=" + dir + ",dst=" + dir + ",readonly",
		"--mount", "type=bind,src=" + gitDir + ",dst=" + gitDir + ",readonly",
		"--entrypoint", "git", r.Image,
		"-C", dir, "bundle", "create", "-", "refs/heads/" + r.Branch, "^" + baseSHA,
	}
}

// capWriter writes to a file and fails once more than max bytes would be
// written. Name is explicit (not an embedded *os.File) so io.Copy cannot reach
// the file's ReadFrom and bypass the cap.
type capWriter struct {
	f   *os.File
	max int64
	n   int64
	err error
}

func (c *capWriter) Name() string { return c.f.Name() }

func (c *capWriter) Write(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	if c.n+int64(len(p)) > c.max {
		c.err = errBundleTooBig
		return 0, c.err
	}
	n, err := c.f.Write(p)
	c.n += int64(n)
	return n, err
}

// checkMounts is the check of every path a throwaway container over c mounts:
// clone_of, the clone dir and the ancestors of the clone dir. Fetch and Return share it.
func checkMounts(c config.Clone, home string, protected []box.ProtectedDir) error {
	if err := CheckOf(c.Of, home, protected); err != nil {
		return err
	}
	unusable := func(why string) error {
		return fmt.Errorf("%w: clone %s %s", box.ErrUnboxableWorkspace, c.Dir, why)
	}
	if !filepath.IsAbs(c.Dir) || filepath.Clean(c.Dir) != c.Dir {
		return unusable("is not a clean absolute path")
	}
	// Every ancestor too, as at spawn: a box that owns one could swap it for a
	// link and make the bundle container read a different repo.
	if err := checkParents(c); err != nil {
		return err
	}
	st, lerr := os.Lstat(c.Dir)
	switch {
	case lerr != nil:
		return unusable("cannot be read: " + sanitize(lerr.Error()))
	case st.Mode()&fs.ModeSymlink != 0:
		return unusable("is a symlink; use the resolved path")
	case !st.IsDir():
		return unusable("is not a directory")
	}
	for _, p := range []string{c.Dir, filepath.Join(c.Of, ".git")} {
		if why := box.MountUnsafe(p); why != "" {
			return fmt.Errorf("%w: %s: %s", box.ErrUnboxableWorkspace, p, why)
		}
	}
	return nil
}

// Fetch brings branch r.Branch of the clone home as refs/heads/box/<Branch> in
// clone_of, through a bundle made in a network-less container. Every host git
// call runs in clone_of with CleanEnv(), never in the clone. Any error leaves
// no ref changed by a half-checked bundle and no temp file behind.
func Fetch(ctx context.Context, g Git, d Docker, r FetchRequest) (res FetchResult, err error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	c := r.Clone

	// 1. Names that reach a ref path, a refspec and an argv.
	if err := ValidBranch(r.Branch); err != nil {
		return res, err
	}
	if err := ValidBranch(r.Base); err != nil {
		return res, err
	}
	if r.Image == "" || strings.HasPrefix(r.Image, "-") {
		return res, fmt.Errorf("clone fetch: the box image %q is not usable", sanitize(r.Image))
	}

	// 2. Paths that are mounted into the container.
	if err := checkMounts(c, r.Home, r.Protected); err != nil {
		return res, err
	}

	hostGit := func(args ...string) ([]byte, error) {
		out, err := g.Run(ctx, c.Of, CleanEnv(), hardened(args...)...)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// git's own text can name refs and subjects a box wrote.
			return nil, errors.New(sanitizeN(err.Error(), maxLongErr))
		}
		return out, nil
	}

	// 3. The base, as a SHA from clone_of.
	out, err := hostGit("rev-parse", "--verify", "--end-of-options", "refs/heads/"+r.Base+"^{commit}")
	if err != nil {
		return res, fmt.Errorf("base branch %s in %s: %w", r.Base, c.Of, err)
	}
	base := strings.TrimSpace(string(out))
	if !shaRE.MatchString(base) {
		return res, fmt.Errorf("base branch %s in %s: unexpected rev-parse output", r.Base, c.Of)
	}

	// The quarantine ref of step 7 goes on every path out from here, success
	// included, and also one an earlier killed fetch left. The delete gets a
	// fresh context: a cancelled ctx must not leave refused history reachable.
	// Scoped to the clone: two profiles may share clone_of, and one's fetch
	// must not overwrite the ref the other is checking.
	quarantine := quarantinePrefix + c.Hash() + "/" + r.Branch
	defer func() {
		cctx, ccancel := context.WithTimeout(context.WithoutCancel(ctx), quarantineCleanup)
		defer ccancel()
		if _, qerr := g.Run(cctx, c.Of, CleanEnv(), hardened("update-ref", "-d", quarantine)...); qerr != nil {
			msg := fmt.Sprintf("could not remove %s from %s (%s); remove it: git -C %s update-ref -d %s",
				quarantine, c.Of, sanitizeN(qerr.Error(), maxLongErr), c.Of, quarantine)
			if err != nil {
				err = fmt.Errorf("%w; and %s", err, msg)
			} else {
				res.Warnings = append(res.Warnings, msg)
			}
		}
	}()

	// 3b. The host's record of the bases it gave this clone (main, else
	// master), with the current host base added first so a later rewrite of it
	// is caught too. Without a trusted record the history cannot be checked:
	// refuse before any container runs, unless -allow-old-history.
	bb, bbSHA, err := BaseBranch(ctx, g, c.Of)
	if err != nil {
		if ctx.Err() != nil {
			return res, fmt.Errorf("clone fetch: %w", ctx.Err())
		}
		return res, fmt.Errorf("clone fetch: cannot check %s against host history: %s", r.Branch, sanitizeN(err.Error(), maxLongErr))
	}
	rebase := bb // the branch the fix rebases on
	if rebase == "" {
		rebase = r.Base
	}
	var overridden []string // what -allow-old-history let through, for the warning
	if bb == "" {
		if !r.AllowOldHistory {
			return res, refused(ReasonRefused, fmt.Errorf("clone fetch: cannot check %s against host history: %s has no main or master branch", r.Branch, c.Of))
		}
		overridden = append(overridden, c.Of+" has no main or master branch to check against")
	}
	var rec BaseRecord
	var recErr error
	if r.DataDir == "" {
		recErr = errors.New("no data dir")
	} else {
		if bb != "" {
			recErr = AppendBase(r.DataDir, c.Dir, bbSHA)
		}
		if recErr == nil {
			rec, recErr = ReadBaseRecord(r.DataDir, c.Dir)
		}
	}
	var unverified string
	switch {
	case recErr != nil:
		rec = BaseRecord{}
		unverified = "the base record cannot be used: " + sanitizeN(recErr.Error(), maxErrRunes)
	case !rec.Exists:
		unverified = "no base record"
	case rec.Unverified:
		unverified = "the base record is unverified"
	}
	if unverified != "" {
		if !r.AllowOldHistory {
			msg := fmt.Sprintf("clone fetch: this clone's history is unverified (created before refresh tracking); "+
				"recreate the clone, or pass -allow-old-history once you have checked %s", r.Branch)
			if recErr != nil {
				msg += " (" + unverified + ")"
			}
			return res, refused(ReasonRefused, errors.New(msg))
		}
		overridden = append(overridden, unverified)
	}

	// 4. A fresh 0600 temp file the host chose; removed on every path out.
	tempDir, err := filepath.Abs(r.TempDir) // git runs with -C <of>: a relative path would mean another place
	if err != nil {
		return res, fmt.Errorf("clone fetch: %w", err)
	}
	if err := os.MkdirAll(tempDir, 0o700); err != nil {
		return res, fmt.Errorf("clone fetch: %w", err)
	}
	f, err := os.CreateTemp(tempDir, "bundle-*")
	if err != nil {
		return res, fmt.Errorf("clone fetch: %w", err)
	}
	tmp := f.Name()
	packOut := tmp + ".pack"
	defer func() {
		f.Close()
		for _, p := range []string{tmp, packOut, tmp + ".idx", tmp + ".rev"} {
			os.Remove(p)
		}
	}()

	// 5. The bundle, made in the container. Success is the exit status alone:
	// an empty range exits 128 but has already written a header to stdout.
	cw := &capWriter{f: f, max: maxBundleBytes}
	name := containerName("clonefetch", r.Instance)
	runErr := runContainer(ctx, d, name, BundleArgs(r, base, name), cw)
	if cw.err != nil {
		return res, refused(ReasonTooLarge, fmt.Errorf("clone fetch: the bundle is larger than %d bytes; refusing it", maxBundleBytes))
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return res, fmt.Errorf("clone fetch: %w", ctx.Err())
		}
		msg := sanitizeN(runErr.Error(), maxLongErr)
		if strings.Contains(runErr.Error(), "empty bundle") {
			return res, refused(reasonEmpty, fmt.Errorf("nothing to fetch: %s has no commits that %s lacks", r.Branch, r.Base))
		}
		return res, fmt.Errorf("clone fetch: bundling %s in the container: %s", r.Branch, msg)
	}
	if err := f.Close(); err != nil {
		return res, fmt.Errorf("clone fetch: %w", err)
	}

	// 6. The bundle must be complete against clone_of.
	if _, err := hostGit("bundle", "verify", tmp); err != nil {
		return res, fmt.Errorf("clone fetch: the bundle does not verify: %w", err)
	}

	// 6a. A bundle with no prerequisite commit shares no history with the
	// base: an orphan branch, or one built on a base the host rewrote from its
	// root. The flag does not override this. It is advisory, for an honest box
	// only: the box writes the header, and can list any commit the host has as
	// a prerequisite (bundle verify accepts that). The real guard is 7b, on the
	// imported history that fetch's connectivity check proved complete.
	n, err := bundlePrereqs(tmp)
	if err != nil {
		return res, fmt.Errorf("clone fetch: reading the bundle header: %w", err)
	}
	if n == 0 {
		return res, refused(ReasonRefused, fmt.Errorf("clone fetch: refusing %s: it shares no history with %s in %s (the bundle has no prerequisite commits). "+
			"In the box: git rebase origin/%s, or recreate the clone", r.Branch, r.Base, c.Of, rebase))
	}

	// 6b. git does not fsck the objects of a bundle fetch (measured, git 2.43:
	// fetch.fsckObjects/transfer.fsckObjects pass a commit with no author
	// email), so the pack goes through index-pack --strict first. A failure
	// leaves no ref, and the check's pack is written beside the bundle in
	// TempDir (never into clone_of), removed with it.
	if err := strictIndexPack(ctx, g, c.Of, tmp, packOut); err != nil {
		if ctx.Err() != nil {
			return res, fmt.Errorf("clone fetch: %w", ctx.Err())
		}
		return res, refused(ReasonRefused, fmt.Errorf("clone fetch: the bundle's objects fail git's strict checks: %s", sanitizeN(err.Error(), maxLongErr)))
	}

	// 6c. Which objects of this pack clone_of lacks, before the import moves any
	// into it: those are the box's own, and the only commits that may be in the
	// tip's history without a host branch or tag reaching them (7d). The import
	// record holds the ones earlier fetches brought; it is seeded once from the
	// box/* branches that already exist. A check that cannot run refuses, flag or not.
	cannot := func(err error) error {
		if ctx.Err() != nil {
			return fmt.Errorf("clone fetch: %w", ctx.Err())
		}
		return fmt.Errorf("clone fetch: cannot check %s against host history: %s", r.Branch, sanitizeN(err.Error(), maxLongErr))
	}
	missing, packCommits, err := missingBefore(ctx, g, c.Of, strings.TrimSuffix(packOut, ".pack")+".idx")
	if err != nil {
		return res, cannot(err)
	}
	// The pack's new commits are the box's whatever happens next, so they are
	// recorded BEFORE the import: objects that land in clone_of while the fetch is
	// cut short (a failed ref update, a timeout, a restart) must not read as host
	// history on the retry. They are absent from clone_of now, so this cannot
	// launder a commit the host had.
	var boxMade map[string]bool
	if r.DataDir != "" {
		err := seedImportRecord(ctx, g, r.DataDir, c.Of)
		if err == nil {
			err = AppendImported(r.DataDir, c.Of, packCommits)
		}
		if err == nil {
			boxMade, _, err = ReadImportRecord(r.DataDir, c.Of)
		}
		if err != nil {
			return res, cannot(err)
		}
	}

	// 7. Into clone_of's quarantine ref, forced. The checks below use the SHA
	// read here, never the ref again, so what moves to box/<branch> is exactly
	// what was checked.
	if _, err := hostGit("-c", "fetch.fsckObjects=true", "-c", "transfer.fsckObjects=true",
		"fetch", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules",
		tmp, "+refs/heads/"+r.Branch+":"+quarantine); err != nil {
		return res, fmt.Errorf("clone fetch: %w", err)
	}
	out, err = hostGit("rev-parse", "--verify", "--end-of-options", quarantine+"^{commit}")
	if err != nil {
		return res, fmt.Errorf("clone fetch: %w", err)
	}
	tip := strings.TrimSpace(string(out))
	if !shaRE.MatchString(tip) {
		return res, fmt.Errorf("clone fetch: %s: unexpected rev-parse output", quarantine)
	}
	// The ref must still hold what this bundle brought: the tip it names.
	out, err = hostGit("bundle", "list-heads", tmp, "refs/heads/"+r.Branch)
	if err != nil {
		return res, fmt.Errorf("clone fetch: %w", err)
	}
	head, name, _ := strings.Cut(strings.TrimSpace(string(out)), " ")
	if !shaRE.MatchString(head) || name != "refs/heads/"+r.Branch {
		return res, fmt.Errorf("clone fetch: refusing %s: the bundle does not name exactly one tip for it", r.Branch)
	}
	if head != tip {
		return res, fmt.Errorf("clone fetch: refusing %s: the imported tip changed while it was checked (a concurrent fetch?); retry", r.Branch)
	}

	// 7b. A recorded base that host history no longer has (a rewrite, e.g. a
	// scrubbed secret) must not be an ancestor of the branch. A check that
	// cannot run refuses even with the flag: it covers a known stale ancestor,
	// not an unknown one.
	if bb != "" {
		stale, err := StaleSHAs(ctx, g, c.Of, bb, rec.SHAs)
		if err != nil {
			return res, cannot(err)
		}
		hit, err := staleAncestor(ctx, g, c.Of, stale, tip)
		if err != nil {
			return res, cannot(err)
		}
		if hit != "" {
			if !r.AllowOldHistory {
				return res, refused(ReasonRefused, fmt.Errorf("clone fetch: refusing %s: it carries history host %s no longer has (recorded tip %s is an ancestor of %s). "+
					"In the box: git rebase origin/%s, or recreate the clone. The refused objects stay unreachable in %s until git gc; "+
					"a push sends only reachable objects, so do not point a ref at them.", r.Branch, bb, hit[:12], r.Branch, bb, c.Of))
			}
			overridden = append(overridden, fmt.Sprintf("recorded tip %s of host %s is an ancestor of %s", hit[:12], bb, r.Branch))
		}
	}

	// 7d. The branch must not bring back a host commit that clone_of still holds
	// but no host branch reaches any more (history the host rewrote away, or a
	// branch the user deleted): 7b only knows the recorded base tips. Same rule:
	// a check that cannot run refuses even with the flag.
	hit, err := rewrittenAway(ctx, g, c.Of, tip, missing, boxMade)
	if err != nil {
		return res, cannot(err)
	}
	if hit != "" {
		if !r.AllowOldHistory {
			return res, refused(ReasonRefused, fmt.Errorf("clone fetch: refusing %s: it carries commit %s that %s had but no branch there reaches any more "+
				"(history rewritten away, or a branch you deleted). In the box: git rebase origin/%s. After review: -allow-old-history",
				r.Branch, hit[:12], c.Of, rebase))
		}
		overridden = append(overridden, fmt.Sprintf("commit %s of %s is no longer reached by any branch there", hit[:12], c.Of))
	}

	// 7c. Checked: box/<branch> takes the tip; the deferred delete drops the
	// quarantine ref. Forced, except for auto-return.
	ref := "refs/heads/box/" + r.Branch
	if r.FastForwardOnly {
		if err := fastForwardRef(ctx, g, c.Of, r.Branch, ref, tip); err != nil {
			return res, err
		}
	} else {
		if r.RefuseCheckedOut {
			if err := refuseCheckedOut(ctx, g, c.Of, r.Branch, ref); err != nil {
				return res, err
			}
		}
		if _, err := hostGit("update-ref", ref, tip); err != nil {
			return res, fmt.Errorf("clone fetch: %w", err)
		}
	}
	res = FetchResult{Ref: ref, BaseSHA: base}
	if len(overridden) > 0 {
		res.Warnings = append([]string{fmt.Sprintf("-allow-old-history: the history check was overridden (%s); review box/%s before any merge or push",
			strings.Join(overridden, "; "), r.Branch)}, res.Warnings...)
	}

	// 8. What came: sanitized lines, and the attribution trailers a push must not carry.
	out, err = hostGit("log", "--no-show-signature", "--format=%h%x09%s%x00%B%x1e", base+".."+ref)
	if err != nil {
		return res, fmt.Errorf("clone fetch: fetched %s but cannot list it: %w", ref, err)
	}
	attributed := 0
	var body strings.Builder
	flush := func() {
		if attrRE.MatchString(body.String()) {
			attributed++
		}
		body.Reset()
	}
	started := false
	for _, rec := range strings.Split(string(out), "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		head, rest, hasBody := strings.Cut(rec, "\x00")
		h, subj, ok := strings.Cut(head, "\t")
		if ok && hasBody && hexRE.MatchString(h) {
			if started {
				flush()
			}
			started = true
			res.Commits = append(res.Commits, sanitize(h+" "+subj))
			body.WriteString(rest)
			continue
		}
		// Not a record start: a box put the separator inside a message. Keep it
		// with the previous commit so a trailer cannot hide behind it.
		body.WriteString(rec)
	}
	if started {
		flush()
	}
	if attributed > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%d commit(s) carry an AI attribution trailer (Co-Authored-By / Generated with); strip them before any push", attributed))
	}

	// 9. Paths at or under a share are the host's local-only files: a box
	// commit touching one (git add -f) would publish it on a push.
	if len(c.Shares) > 0 {
		// log, not a tree diff: a share file added then removed inside the
		// branch is still in its history, and a push would publish it.
		out, err = hostGit("log", "--no-ext-diff", "--no-textconv", "--format=", "--name-only", "-z",
			"--diff-merges=first-parent", base+".."+ref)
		if err != nil {
			return res, fmt.Errorf("clone fetch: fetched %s but cannot list its files: %w", ref, err)
		}
		seen := map[string]bool{}
		var hit []string
		for _, p := range strings.Split(string(out), "\x00") {
			if seen[p] {
				continue
			}
			seen[p] = true
			for _, s := range c.Shares {
				if p == s || strings.HasPrefix(p, s+"/") {
					hit = append(hit, sanitize(p))
					break
				}
			}
		}
		if len(hit) > 0 {
			more := ""
			if len(hit) > maxWarnPaths {
				more = fmt.Sprintf(" (and %d more)", len(hit)-maxWarnPaths)
				hit = hit[:maxWarnPaths]
			}
			res.Warnings = append(res.Warnings, fmt.Sprintf("box/%s changes shared (local-only) paths: %s%s",
				r.Branch, strings.Join(hit, ", "), more))
		}
	}
	return res, nil
}

const (
	maxBundleHeader     = 1 << 20  // bytes of header (prerequisites, refs) before the pack
	maxBundleHeaderLine = 64 << 10 // one header line
)

// packOffset returns where the pack starts in a bundle file: after the header's
// terminating blank line. It accepts the "# v2 git bundle" and "# v3 git
// bundle" signatures and refuses anything else. EVERY header line is checked,
// because git accepts "@" capability lines anywhere in a v3 header: in a v3
// header an "@" line must be @object-format=sha1|sha256 (so a @filter, which
// makes a partial bundle, is refused wherever it sits), and a v2 header has none.
// prereqs counts the "-<sha>" prerequisite lines.
func packOffset(f *os.File) (off int64, prereqs int, err error) {
	br := bufio.NewReaderSize(f, maxBundleHeaderLine)
	readLine := func() (string, error) {
		line, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			return "", errors.New("bundle header line too long")
		}
		if err != nil {
			return "", errors.New("bundle header is truncated")
		}
		off += int64(len(line))
		if off > maxBundleHeader {
			return "", errors.New("bundle header too long")
		}
		return string(line[:len(line)-1]), nil
	}
	sig, err := readLine()
	if err != nil {
		return 0, 0, err
	}
	var v3 bool
	switch sig {
	case "# v2 git bundle":
	case "# v3 git bundle":
		v3 = true
	default:
		return 0, 0, fmt.Errorf("not a git bundle (signature %q)", sanitize(sig))
	}
	for {
		line, err := readLine()
		if err != nil {
			return 0, 0, err
		}
		if line == "" {
			return off, prereqs, nil
		}
		if line[0] == '-' {
			prereqs++
		}
		if line[0] == '@' && !(v3 && (line == "@object-format=sha1" || line == "@object-format=sha256")) {
			return 0, 0, fmt.Errorf("bundle capability %q is not supported", sanitize(line))
		}
	}
}

// bundlePrereqs is the number of prerequisite commits in the bundle at path.
// A bundle of a branch that shares history with the base has at least one.
func bundlePrereqs(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	_, n, err := packOffset(f)
	return n, err
}

// staleAncestor returns the first of stale (StaleSHAs output) that is an
// ancestor of tip, or "". A stale SHA that is missing from of, or not a
// commit, cannot be one: the fetch that made tip checked that all its history
// is present. Any git failure other than merge-base's "no" (exit 1) is an
// error, never a pass.
func staleAncestor(ctx context.Context, g Git, of string, stale []string, tip string) (string, error) {
	if len(stale) == 0 {
		return "", nil
	}
	sg, ok := g.(StdinGit)
	if !ok {
		return "", errors.New("this git runner cannot take stdin")
	}
	out, err := sg.RunStdin(ctx, of, CleanEnv(), strings.NewReader(strings.Join(stale, "\n")+"\n"), hardened("cat-file", "--batch-check")...)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) != len(stale) {
		return "", fmt.Errorf("cat-file --batch-check: %d answers for %d SHAs", len(lines), len(stale))
	}
	for i, line := range lines {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != stale[i] {
			return "", fmt.Errorf("cat-file --batch-check: unexpected line %q", sanitize(line))
		}
		if f[1] != "commit" {
			continue
		}
		_, err := g.Run(ctx, of, CleanEnv(), hardened("merge-base", "--is-ancestor", stale[i], tip)...)
		if err == nil {
			return stale[i], nil
		}
		if code, ok := exitCode(err); ok && code == 1 && ctx.Err() == nil {
			continue
		}
		return "", err
	}
	return "", nil
}

// strictIndexPack feeds the pack inside the bundle file to `git index-pack
// --strict --fix-thin --stdin <packOut>` in of, which fscks every object
// (missingEmail, badTree, a .gitmodules trick...) and completes a thin pack from
// of's objects. packOut (and its .idx/.rev) is outside of: without a path
// index-pack writes into of's objects/pack, and a refused pack would stay there
// as tmp_pack_*. The caller removes packOut's files.
func strictIndexPack(ctx context.Context, g Git, of, bundle, packOut string) error {
	sg, ok := g.(StdinGit)
	if !ok {
		return errors.New("this git runner cannot take stdin")
	}
	f, err := os.Open(bundle)
	if err != nil {
		return err
	}
	defer f.Close()
	off, _, err := packOffset(f)
	if err != nil {
		return err
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return err
	}
	_, err = sg.RunStdin(ctx, of, CleanEnv(), f, hardened("index-pack", "--strict", "--fix-thin", "--stdin", packOut)...)
	return err
}

// gitFail words a failed host git call: the context's error when it ran out,
// else the sanitized git output.
func gitFail(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("clone fetch: %w", ctx.Err())
	}
	return fmt.Errorf("clone fetch: %s", sanitizeN(err.Error(), maxLongErr))
}

// refuseCheckedOut returns a ReasonCheckedOut refusal when a worktree of of has
// ref checked out. Every update of box/<branch> that must not leave a host work
// tree behind its ref goes through it.
func refuseCheckedOut(ctx context.Context, g Git, of, branch, ref string) error {
	out, err := g.Run(ctx, of, CleanEnv(), hardened("worktree", "list", "--porcelain")...)
	if err != nil {
		return gitFail(ctx, err)
	}
	var where string
	for _, line := range strings.Split(string(out), "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			where = p
		} else if line == "branch "+ref {
			return refused(ReasonCheckedOut, fmt.Errorf("box/%s is checked out in %s; not moved", branch, sanitizeN(where, maxErrRunes)))
		}
	}
	return nil
}

// fastForwardRef moves ref to tip only when it is new or tip descends from it,
// and never while a worktree of of has it checked out. update-ref carries the
// value read here as its old value, so a ref that moved since is refused, not
// overwritten.
func fastForwardRef(ctx context.Context, g Git, of, branch, ref, tip string) error {
	run := func(args ...string) ([]byte, error) {
		return g.Run(ctx, of, CleanEnv(), hardened(args...)...)
	}
	fail := func(err error) error { return gitFail(ctx, err) }
	// read is the commit ref names; "" when there is no such ref (exit 1 of --quiet).
	read := func() (string, error) {
		out, err := run("rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
		if code, ok := exitCode(err); ok && code == 1 {
			return "", nil
		}
		if err != nil {
			return "", fail(err)
		}
		sha := strings.TrimSpace(string(out))
		if !shaRE.MatchString(sha) {
			return "", fmt.Errorf("clone fetch: %s: unexpected rev-parse output", ref)
		}
		return sha, nil
	}

	old, err := read()
	if err != nil {
		return err
	}

	if err := refuseCheckedOut(ctx, g, of, branch, ref); err != nil {
		return err
	}

	notFF := refused(ReasonNotFF, fmt.Errorf("box/%s on the host has commits %s lacks; not overwritten (culibox clone fetch overwrites it)", branch, branch))
	if old != "" {
		if _, err := run("merge-base", "--is-ancestor", old, tip); err != nil {
			if code, ok := exitCode(err); ok && code == 1 {
				return notFF
			}
			return fail(err)
		}
	}
	// old == "" tells update-ref the ref must not exist yet.
	if _, err := run("update-ref", ref, tip, old); err != nil {
		if ctx.Err() != nil {
			return fail(err)
		}
		// If the ref no longer holds what was read, another writer won.
		if now, rerr := read(); rerr == nil && now != old {
			return notFF
		}
		return fail(err)
	}
	return nil
}
