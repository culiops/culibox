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
	"strings"
	"syscall"
	"time"

	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/config"
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
// with box.LookDocker).
type ExecDocker struct{ Bin string }

// RunStream runs docker with args. stdout is copied to the writer as it
// arrives, never held whole in memory. stderr keeps its last 8 KiB (the final
// "fatal:" line is the useful one), and the error carries only its last 200
// runes, sanitized (box-written text). A
// failing writer or a cancelled ctx stops the container (SIGTERM, which the
// docker CLI forwards, then a kill after 10s) and leaves no goroutine behind.
func (d ExecDocker) RunStream(ctx context.Context, args []string, stdout io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, d.Bin, args...)
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
}

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
	if err := CheckOf(c.Of, r.Home, r.Protected); err != nil {
		return res, err
	}
	unusable := func(why string) error {
		return fmt.Errorf("%w: clone %s %s", box.ErrUnboxableWorkspace, c.Dir, why)
	}
	if !filepath.IsAbs(c.Dir) || filepath.Clean(c.Dir) != c.Dir {
		return res, unusable("is not a clean absolute path")
	}
	// Every ancestor too, as at spawn: a box that owns one could swap it for a
	// link and make the bundle container read a different repo.
	if err := checkParents(c); err != nil {
		return res, err
	}
	st, lerr := os.Lstat(c.Dir)
	switch {
	case lerr != nil:
		return res, unusable("cannot be read: " + sanitize(lerr.Error()))
	case st.Mode()&fs.ModeSymlink != 0:
		return res, unusable("is a symlink; use the resolved path")
	case !st.IsDir():
		return res, unusable("is not a directory")
	}
	for _, p := range []string{c.Dir, filepath.Join(c.Of, ".git")} {
		if why := box.MountUnsafe(p); why != "" {
			return res, fmt.Errorf("%w: %s: %s", box.ErrUnboxableWorkspace, p, why)
		}
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
		return res, fmt.Errorf("clone fetch: the bundle is larger than %d bytes; refusing it", maxBundleBytes)
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return res, fmt.Errorf("clone fetch: %w", ctx.Err())
		}
		msg := sanitizeN(runErr.Error(), maxLongErr)
		if strings.Contains(runErr.Error(), "empty bundle") {
			return res, fmt.Errorf("nothing to fetch: %s has no commits that %s lacks", r.Branch, r.Base)
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

	// 6b. git does not fsck the objects of a bundle fetch (measured, git 2.43:
	// fetch.fsckObjects/transfer.fsckObjects pass a commit with no author
	// email), so the pack goes through index-pack --strict first. A failure
	// leaves no ref, and the check's pack is written beside the bundle in
	// TempDir (never into clone_of), removed with it.
	if err := strictIndexPack(ctx, g, c.Of, tmp, packOut); err != nil {
		if ctx.Err() != nil {
			return res, fmt.Errorf("clone fetch: %w", ctx.Err())
		}
		return res, fmt.Errorf("clone fetch: the bundle's objects fail git's strict checks: %s", sanitizeN(err.Error(), maxLongErr))
	}

	// 7. Into clone_of as box/<branch>, forced.
	ref := "refs/heads/box/" + r.Branch
	if _, err := hostGit("-c", "fetch.fsckObjects=true", "-c", "transfer.fsckObjects=true",
		"fetch", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules",
		tmp, "+refs/heads/"+r.Branch+":"+ref); err != nil {
		return res, fmt.Errorf("clone fetch: %w", err)
	}
	res = FetchResult{Ref: ref, BaseSHA: base}

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
func packOffset(f *os.File) (int64, error) {
	br := bufio.NewReaderSize(f, maxBundleHeaderLine)
	var off int64
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
		return 0, err
	}
	var v3 bool
	switch sig {
	case "# v2 git bundle":
	case "# v3 git bundle":
		v3 = true
	default:
		return 0, fmt.Errorf("not a git bundle (signature %q)", sanitize(sig))
	}
	for {
		line, err := readLine()
		if err != nil {
			return 0, err
		}
		if line == "" {
			return off, nil
		}
		if line[0] == '@' && !(v3 && (line == "@object-format=sha1" || line == "@object-format=sha256")) {
			return 0, fmt.Errorf("bundle capability %q is not supported", sanitize(line))
		}
	}
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
	off, err := packOffset(f)
	if err != nil {
		return err
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return err
	}
	_, err = sg.RunStdin(ctx, of, CleanEnv(), f, hardened("index-pack", "--strict", "--fix-thin", "--stdin", packOut)...)
	return err
}
