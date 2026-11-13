package box

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// A context path is a host directory or file a boxed agent can read, mounted
// read-only at the same path. These checks run on the host before that mount
// is made, at profile save and again at spawn.

// ctxPathError is a context_paths refusal: it wraps ErrUnboxableWorkspace, so a
// caller can classify it, and reads as "context_paths <p>: <why>".
type ctxPathError struct{ msg string }

func (e *ctxPathError) Error() string { return e.msg }
func (e *ctxPathError) Unwrap() error { return ErrUnboxableWorkspace }

func ctxPathRefuse(p, format string, a ...any) error {
	return &ctxPathError{"context_paths " + p + ": " + fmt.Sprintf(format, a...)}
}

// ErrContextMountChanged marks a box refused because a context mount, seen from
// inside the box, is not the path that was checked on the host.
var ErrContextMountChanged = errors.New("context mount is not the checked path")

// ctxMountError is the ErrContextMountChanged refusal: a ctxPathError (so it is
// ErrUnboxableWorkspace too) that also reports ErrContextMountChanged.
type ctxMountError struct{ ctxPathError }

func (e *ctxMountError) Is(target error) bool { return target == ErrContextMountChanged }

// ctxMountChanged is the refusal for context path p, with why saying what the
// in-box check found. Its text is "context_paths <p>: <why>; refusing".
func ctxMountChanged(p, why string) error {
	return &ctxMountError{ctxPathError{"context_paths " + p + ": " + why + "; refusing"}}
}

// FileID is a file's identity on its filesystem. A bind mount shares its
// source's, so the identity of a mount's destination inside the box says
// whether docker mounted the path that was checked.
type FileID struct{ Dev, Ino uint64 }

// checkMountIDs compares, inside the running box name, the identity of every
// mount in ms that carries an ID with the one recorded on the host (Linux) or
// read in the box VM (macOS, see fillVMIDs). ms is the workspace first, then the
// extra mounts. Docker follows symlinks in a bind source, so a path flipped to a
// link between that read and `docker run` would mount the link's target; this
// catches it before the agent starts. One `docker exec` for all of them, paths
// as argv (no shell). Mounts without an ID cost nothing. The guards of the
// workspace are compared only when guards is set (see skipGuard); created says
// the box is this spawn's, for the refusal's wording. Caller holds opMu.
func (m *Manager) checkMountIDs(ctx context.Context, name string, ms []Mount, created, guards bool) error {
	var checked []Mount
	for _, mt := range ms {
		if mt.ID != nil && !skipGuard(mt, guards) {
			checked = append(checked, mt)
		}
	}
	if len(checked) == 0 {
		return nil
	}
	// /usr/bin/stat by absolute path: a bare "stat" resolves through the box PATH,
	// which has the agent-writable ~/go/bin ahead of /usr/bin, so the agent could
	// plant a stat that prints whatever identity the check expects.
	args := []string{"exec", name, "/usr/bin/stat", "-c", "%d:%i", "--"}
	for _, mt := range checked {
		args = append(args, mt.Box)
	}
	// Stdout only: stderr noise on a successful exec would shift the lines below.
	out, err := m.opts.Client.Stdout(ctx, args)
	lines := strings.Split(out, "\n")
	for i, mt := range checked {
		// A check that did not run (the exec failed, or its answer is missing or
		// no identity) is not a path that changed: mountChanged says so.
		switch {
		case err != nil:
			return m.mountChanged(mt, created, err)
		case i >= len(lines):
			return m.mountChanged(mt, created, errors.New("it printed no identity for this path"))
		}
		got := strings.TrimSpace(lines[i])
		if got == fmt.Sprintf("%d:%d", mt.ID.Dev, mt.ID.Ino) {
			continue
		}
		if !isIDLine(got) {
			return m.mountChanged(mt, created, errors.New("it printed something that is not an identity"))
		}
		return m.mountChanged(mt, created, nil)
	}
	return nil
}

// isIDLine reports whether s has the shape of one line of the in-box stat,
// "<dev>:<ino>" in decimal.
func isIDLine(s string) bool {
	dev, ino, ok := strings.Cut(s, ":")
	if !ok {
		return false
	}
	for _, n := range []string{dev, ino} {
		if _, err := strconv.ParseUint(n, 10, 64); err != nil {
			return false
		}
	}
	return true
}

// skipGuard is true for a guard mount when guards is not set, which ensure does
// for a box that was checked when it started: one this spawn created, and one
// created by a build that checks (LabelIdentity) is compared only at creation. A
// file bind mount pins the inode it was made with, and an editor that saves
// CLAUDE.md by rename leaves the host's file with another identity while the
// running box keeps the old one: comparing a guard on reuse would refuse the next
// session after any such save. The workspace directory is not replaced by normal
// work, so it is compared every time. A box never checked (an older build's, or
// one whose refusal could not remove it) has its guards compared once.
func skipGuard(mt Mount, guards bool) bool {
	return mt.kind == mountGuard && !guards
}

// wsHostFilesHint ends the refusal of a workspace that a box created by this
// spawn does not show as the checked folder: one such refusal is a race, but one
// for every folder means docker does not see the host's files as they are, which
// the README lists as a requirement. Only where the boxes run in the host's own
// docker (no VM hook, Options.VMIDs): with a hook they run in CuliBox's own VM
// and that requirement does not apply.
const wsHostFilesHint = "If this repeats for every folder, CuliBox's docker does not see host files as they are " +
	"(for example Docker Desktop for Linux or a remote docker host); see README, Requirements."

// mountChanged is the refusal for a mount the in-box check did not confirm: the
// workspace (and a guard of it) is a mount-changed refusal of the workspace,
// anything else is a context-path one. The text matches what was found.
// checkErr is why the check could not run (the exec failed, or its answer is
// missing or is no identity), and then nothing is claimed to have changed. With
// no checkErr the identity differs: created says the box is this spawn's, so the
// mount changed while it started, and otherwise the box was reused and its mount
// was replaced since it started. A workspace that differs in a box this spawn
// created, with no VM hook, also carries wsHostFilesHint. A guard is named as one, and as a file or
// folder, because it is not the workspace.
func (m *Manager) mountChanged(mt Mount, created bool, checkErr error) error {
	var what, verdict string
	switch mt.kind {
	case mountWorkspace:
		what, verdict = "workspace", "the mounted folder is not the one that was checked"
	case mountGuard:
		what, verdict = "guard", "the mounted file or folder is not the one that was checked"
	default:
		what, verdict = "context_paths", "the mounted path is not the checked one"
	}
	var why string
	switch {
	case checkErr != nil:
		why = fmt.Sprintf("the in-box check could not run (%v)", checkErr)
	case created:
		why = verdict + " (it changed while the box started)"
	default:
		why = verdict + " (it was replaced since the box started)"
	}
	if mt.kind == mountWorkspace || mt.kind == mountGuard {
		msg := what + " " + mt.Host + ": " + why + "; refusing"
		if mt.kind == mountWorkspace && created && checkErr == nil && m.opts.VMIDs == nil {
			msg += ". " + wsHostFilesHint
		}
		return &wsMountError{msg: msg}
	}
	return ctxMountChanged(mt.Host, why)
}

// ctxIdentityFailed is the refusal when the box VM could not give the identity
// of the context mounts at paths: the same ErrContextMountChanged family, so
// the session and web layers show it like the other context refusals. The
// reason is the VM read's own error: fixed messages that name a position, never
// a path, saying whether a path is a symlink, is missing or the VM did not
// answer (plus, when the exec itself failed, the first line colima printed,
// passed through as it came). The refusal names every path of the batch: the
// VM read them together.
func ctxIdentityFailed(paths []string, err error) error {
	return &ctxMountError{ctxPathError{"context_paths " + strings.Join(paths, ", ") +
		": the box VM could not confirm the path (" + err.Error() + "); refusing"}}
}

// identityFailed is the refusal when the box VM could not give the identity of
// the mounts ms (the batch it was asked about). A batch of context mounts alone
// is ctxIdentityFailed. One with the workspace (or a guard of it) in it names the
// workspace, is ErrWorkspaceMountChanged and ErrBoxUnavailable, and, when
// context mounts were read with it, names them too and is also the context
// refusal (ErrContextMountChanged, ErrUnboxableWorkspace): the VM read them
// together, so the failure cannot be told apart.
func identityFailed(ms []Mount, err error) error {
	var wsPath string
	var ctx []string
	for _, mt := range ms {
		switch {
		case mt.kind == mountWorkspace:
			wsPath = mt.Host
		case mt.kind == mountGuard:
			if wsPath == "" {
				wsPath = mt.Host
			}
		default:
			ctx = append(ctx, mt.Host)
		}
	}
	if wsPath == "" {
		return ctxIdentityFailed(ctx, err)
	}
	msg := "the box VM could not confirm workspace " + wsPath
	var also []error
	if len(ctx) > 0 {
		msg += " and context_paths " + strings.Join(ctx, ", ")
		also = []error{ErrContextMountChanged, ErrUnboxableWorkspace}
	}
	return &wsMountError{msg: msg + " (" + err.Error() + "); refusing", also: also}
}

// fillVMIDs gives every CheckID mount that has no ID its identity from
// Options.VMIDs, and returns ms with those IDs set (a copy; the caller's slice
// is never written to). Mounts that already carry an ID, mounts that are not
// CheckID, and the guards when guards is not set (skipGuard), are
// not asked about. It is one VMIDs call for all of them, and none when there is
// nothing to ask or no hook (Linux records the identity on the host), so it can
// run again for the same spawn at no cost. Fail closed: an error or a wrong
// count refuses the spawn. Caller holds opMu.
func (m *Manager) fillVMIDs(ctx context.Context, ms []Mount, guards bool) ([]Mount, error) {
	if m.opts.VMIDs == nil {
		return ms, nil
	}
	var at []int
	var asked []Mount
	var paths []string
	for i, mt := range ms {
		if mt.CheckID && mt.ID == nil && !skipGuard(mt, guards) {
			at = append(at, i)
			asked = append(asked, mt)
			paths = append(paths, mt.Host)
		}
	}
	if len(at) == 0 {
		return ms, nil
	}
	ids, err := m.opts.VMIDs(ctx, paths)
	if err == nil && len(ids) != len(paths) {
		err = fmt.Errorf("got %d identities for %d paths", len(ids), len(paths))
	}
	if err != nil {
		return nil, identityFailed(asked, err)
	}
	out := slices.Clone(ms)
	for k, i := range at {
		id := ids[k]
		out[i].ID = &id
	}
	return out, nil
}

// vmView is ms as the box's identity check sees it. With a VM hook (macOS) the
// identity of a guard is the VM's to read, never the host's inode, which is not
// the one the box shows (Options.VMIDs): it returns a copy with the ID cleared on
// every guard. Without the hook it is ms itself.
func (m *Manager) vmView(ms []Mount) []Mount {
	if m.opts.VMIDs == nil {
		return ms
	}
	out := slices.Clone(ms)
	for i := range out {
		if out[i].kind == mountGuard {
			out[i].ID = nil
		}
	}
	return out
}

// withWorkspace is the batch of the workspace mount and the extra mounts, the
// workspace first. A new slice: the caller's is never written to.
func withWorkspace(wsm Mount, extraRO []Mount) []Mount {
	return append([]Mount{wsm}, extraRO...)
}

// fillMountIDs is fillVMIDs over the workspace mount and extraRO as one batch,
// so the VM is asked once, the workspace first, and gives both back.
func (m *Manager) fillMountIDs(ctx context.Context, wsm Mount, extraRO []Mount, guards bool) (Mount, []Mount, error) {
	all, err := m.fillVMIDs(ctx, withWorkspace(wsm, extraRO), guards)
	if err != nil {
		return wsm, extraRO, err
	}
	return all[0], all[1:], nil
}

// refuseMountChanged handles a box that failed checkMountIDs. A box this spawn
// created, or one no session of this process uses, is removed: nothing runs in
// it, and it must not be reused with the wrong mount. A box other sessions run
// in is kept (removing it would kill them) and the refusal says how to get it
// recreated; every later spawn into it is checked, and refused, again. A box
// that could not be removed is remembered (mismatched): it is never reused as it
// is, whatever the next spawn's checks would skip for a box that started well
// (see removeMismatched). Caller holds opMu.
func (m *Manager) refuseMountChanged(ctx context.Context, name string, created bool, cerr error) error {
	m.mu.Lock()
	live := m.active[name]
	m.mu.Unlock()
	if !created && live > 0 {
		return fmt.Errorf("%w (box %s still runs %d session(s) and is kept; end them so the box is recreated, then spawn again)",
			cerr, name, live)
	}
	m.unverify(name)
	if err := m.opts.Client.Stop(ctx, name); err != nil {
		cerr = fmt.Errorf("%w (box %s could not be removed: %v; every spawn into it is checked again)", cerr, name, err)
		m.markMismatched(name)
	} else {
		m.boxGone(name)
		if !created {
			// A reused idle box: the user did not just create it, so say it is gone.
			cerr = fmt.Errorf("%w; the box was removed, spawn again", cerr)
		}
	}
	m.rearmIfIdle(name)
	return cerr
}

// CheckContextPath is CheckContextPathID without the identity, for the callers
// that only need the verdict.
func CheckContextPath(p, home string, protected []ProtectedDir, view *ShareView) error {
	_, err := CheckContextPathID(p, home, protected, view)
	return err
}

// CheckContextPathID refuses a context path the box could not mount safely or at
// all, and returns the identity of the leaf it checked, for Mount.ID (nil off
// Linux: a Mac inode number is not the one the box sees, so there the box layer
// reads the identity inside the VM instead, see Options.VMIDs). The identity
// comes from a walk that follows no symlink in any component (leafID), so a
// component swapped after the check makes docker mount something with another
// identity. p is absolute and clean (config validation has seen it). No component of
// p may be a symlink: the mount follows links on the host, so a link swapped
// after this check would expose a different path than the one the user chose.
// The leaf must exist and be a directory or a regular file. home is the user's
// home directory, protected the directories a box must not touch, and view
// the box VM's share list (nil when there is no VM).
func CheckContextPathID(p, home string, protected []ProtectedDir, view *ShareView) (*FileID, error) {
	if !filepath.IsAbs(p) {
		return nil, ctxPathRefuse(p, "must be an absolute path")
	}
	if filepath.Clean(p) != p {
		return nil, ctxPathRefuse(p, "must be a clean path (no . or .. components, no repeated or trailing slash)")
	}
	cur := string(filepath.Separator)
	var info fs.FileInfo
	for _, part := range strings.Split(strings.Trim(p, string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		var err error
		if info, err = os.Lstat(cur); err != nil {
			if os.IsNotExist(err) {
				return nil, ctxPathRefuse(p, "does not exist")
			}
			return nil, ctxPathRefuse(p, "cannot be read (%v)", err)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return nil, ctxPathRefuse(p, "%s is a symlink; list the real path", cur)
		}
	}
	if info == nil { // p is the filesystem root
		var err error
		if info, err = os.Lstat(p); err != nil {
			return nil, ctxPathRefuse(p, "cannot be read (%v)", err)
		}
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return nil, ctxPathRefuse(p, "must be a directory or a regular file")
	}
	// The identity, straight after the walk above and before the slower checks
	// below: a second, fd-based walk, because the Lstat walk is a series of
	// separate lookups a parent swap can slip between.
	id, err := leafID(p)
	if err != nil {
		return nil, err
	}
	if reason := MountUnsafe(p); reason != "" {
		return nil, ctxPathRefuse(p, "%s", reason)
	}
	if home != "" {
		if real, err := filepath.EvalSymlinks(home); err == nil {
			home = real
		}
		if reason := homeReason(p, home); reason != "" {
			return nil, ctxPathRefuse(p, "%s", reason)
		}
		for _, name := range hostSocketDirs {
			d := filepath.Join(home, name)
			if pathWithin(p, d, runtime.GOOS == "darwin") {
				return nil, ctxPathRefuse(p, "inside %s, which holds host sockets or credentials", d)
			}
		}
	}
	if reason, hint := ProtectedOverlapHint(p, ReadOnlyUse(protected)); reason != "" {
		if hint != "" {
			reason += "; " + hint
		}
		return nil, ctxPathRefuse(p, "%s", reason)
	}
	if view != nil {
		// A context path is mounted read-only: a read-only share suffices.
		if err := view.Check(p, false); err != nil {
			return nil, ctxPathRefuse(p, "%s", err.Error())
		}
	}
	if info.IsDir() {
		ipc, err := findSocketOrPipe(p)
		if err != nil {
			return nil, err
		}
		if ipc != "" {
			return nil, ctxPathRefuse(p, "contains a unix socket or named pipe (%s); a read-only mount does not stop the box "+
				"from connecting to it or writing into it", ipc)
		}
	}
	return id, nil
}

// hostSocketDirs are directories under home that hold the sockets or keys of
// host services (the docker, colima and lima daemons, ssh keys and agent).
var hostSocketDirs = []string{".colima", ".lima", ".docker", ".ssh"}

// HostSocketDirs returns hostSocketDirs joined to home: the host directories a
// bundled file must never be read from.
func HostSocketDirs(home string) []string {
	out := make([]string, 0, len(hostSocketDirs))
	for _, d := range hostSocketDirs {
		out = append(out, filepath.Join(home, d))
	}
	return out
}

// socketScanMaxEntries bounds findSocketOrPipe's walk. A tree bigger than this
// is refused, not allowed unscanned: a socket anywhere in it would go unseen. A
// var so a test can lower it.
var socketScanMaxEntries = 20000

// findSocketOrPipe returns the first unix socket or named pipe under dir (any
// depth, symlinks not followed), or "". The kernel's read-only check covers
// regular files, directories and symlinks only: a read-only bind mount stops
// neither connect() on a socket nor a write into a FIFO. A socket in a context
// path lets the box drive the host service behind it (a docker socket is root
// on the host), a FIFO lets it feed whatever host process reads it. It fails
// closed: a tree over the entry cap or a directory it cannot read is an error,
// because either could hide one. err is a ready context_paths refusal for p.
func findSocketOrPipe(p string) (found string, err error) {
	visited := 0
	_ = filepath.WalkDir(p, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			sub := path
			if rel, rerr := filepath.Rel(p, path); rerr == nil && rel != "." {
				sub = rel
			}
			err = ctxPathRefuse(p, "cannot read %s to scan for unix sockets and named pipes", sub)
			return fs.SkipAll
		}
		if visited >= socketScanMaxEntries {
			err = ctxPathRefuse(p, "too large to scan for unix sockets and named pipes (over %d entries); list narrower subdirectories", socketScanMaxEntries)
			return fs.SkipAll
		}
		visited++
		if d.Type()&(fs.ModeSocket|fs.ModeNamedPipe) != 0 {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	return found, err
}

const (
	secretScanMaxEntries = 5000 // files and directories visited
	secretScanMaxDepth   = 2    // a file directly in dir is depth 1
	secretScanMaxResults = 20
)

// SecretFilePatterns are base-name globs that look like a secret or config
// file. internal/config (which imports this package) warns on the same set.
var SecretFilePatterns = []string{
	".env*", "*.pem", "*.key", "config*.json", "id_*", "*.tfstate", "*.tfstate.*",
	"credentials*", "*.tfvars", ".netrc", ".npmrc", ".git-credentials", "auth.json",
	"*.p12", "*.pfx", "kubeconfig",
}

// SecretFiles lists, relative to dir, regular files whose names look like a
// secret, so the user is told before a box can read them. The scan is bounded:
// two levels deep, 5000 entries, 20 results. It skips .git directories and
// symlinks, and an unreadable entry is skipped, not reported.
func SecretFiles(dir string) []string {
	var found []string
	visited := 0
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil || rel == "." {
			return nil
		}
		if visited >= secretScanMaxEntries {
			return fs.SkipAll
		}
		visited++
		depth := strings.Count(rel, string(filepath.Separator)) + 1
		switch {
		case d.Type()&fs.ModeSymlink != 0:
		case d.IsDir():
			if d.Name() == ".git" || depth >= secretScanMaxDepth {
				return fs.SkipDir
			}
		case d.Type().IsRegular() && looksSecret(d.Name()):
			found = append(found, rel)
			if len(found) >= secretScanMaxResults {
				return fs.SkipAll
			}
		}
		return nil
	})
	return found
}

// LooksSecret reports whether a base name matches SecretFilePatterns.
func LooksSecret(name string) bool { return looksSecret(name) }

func looksSecret(name string) bool {
	for _, pat := range SecretFilePatterns {
		if ok, _ := filepath.Match(pat, name); ok {
			return true
		}
	}
	return false
}
