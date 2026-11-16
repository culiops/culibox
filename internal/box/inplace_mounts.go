package box

import (
	"errors"
	"fmt"
	"os"
	"path"
	"runtime"
	"strconv"
	"strings"
)

// A host folder can be a folder of the workspace under another path: a bind
// mount of <ws>/tools at /mnt/tools makes both one directory, so hooks git runs
// from /mnt/tools/hooks are files the box writes as <ws>/tools/hooks (#308).
// The walk asks the mount table which filesystem folder each directory it
// enters is, and whether that folder lies under the workspace's own folder on
// the same filesystem (or under a mount below the workspace). That costs one
// read of the table per walk and a scan of it per directory entered, whatever
// the workspace holds: nothing below the workspace is listed.

// hostMountInfo reads the daemon's mount table (/proc/self/mountinfo), the
// mounts host git sees when it runs in the same mount namespace. nil where
// there is none: macOS has no bind mounts by default (no nullfs), and a FUSE
// mirror such as bindfs is its own device, so no folder there is the
// workspace's by inode; the walk stays as it was before #308 (the limit is in
// docs/SECURITY.md). A variable so a test can stand in for a host bind mount,
// which needs privileges.
var hostMountInfo = func() func() ([]byte, error) {
	if runtime.GOOS != "linux" {
		return nil
	}
	return func() ([]byte, error) { return os.ReadFile("/proc/self/mountinfo") }
}()

// mountEntry is one line of a mountinfo table: root is the folder of the
// filesystem dev mounted at point (both unescaped).
type mountEntry struct {
	id, parent  int
	dev         string // major:minor
	root, point string
}

// parseMountInfo parses a mountinfo table (proc(5)). A line it cannot read is
// an error: a mount it skipped could be the one that leads into the workspace.
func parseMountInfo(b []byte) ([]mountEntry, error) {
	var out []mountEntry
	for i, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		f := strings.Fields(line)
		sep := -1
		for j := 6; j < len(f); j++ {
			if f[j] == "-" {
				sep = j
				break
			}
		}
		if len(f) < 7 || sep < 0 {
			return nil, fmt.Errorf("line %d is not a mountinfo line", i+1)
		}
		id, err1 := strconv.Atoi(f[0])
		parent, err2 := strconv.Atoi(f[1])
		root, err3 := unescapeMount(f[3])
		point, err4 := unescapeMount(f[4])
		if err := errors.Join(err1, err2, err3, err4); err != nil || !strings.Contains(f[2], ":") {
			return nil, fmt.Errorf("line %d is not a mountinfo line", i+1)
		}
		out = append(out, mountEntry{id: id, parent: parent, dev: f[2], root: root, point: point})
	}
	return out, nil
}

// unescapeMount undoes the kernel's octal escapes (\040 space, \011 tab, \012
// newline, \134 backslash).
func unescapeMount(s string) (string, error) {
	if !strings.Contains(s, `\`) {
		return s, nil
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		if i+3 >= len(s) {
			return "", fmt.Errorf("bad escape in %q", s)
		}
		n, err := strconv.ParseUint(s[i+1:i+4], 8, 8)
		if err != nil {
			return "", fmt.Errorf("bad escape in %q", s)
		}
		b.WriteByte(byte(n))
		i += 3
	}
	return b.String(), nil
}

// relUnder is p relative to dir ("." for dir itself) when p is at or below
// dir, both clean absolute paths, compared as written.
func relUnder(p, dir string) (string, bool) {
	switch {
	case p == dir:
		return ".", true
	case dir == "/":
		return strings.TrimPrefix(p, "/"), strings.HasPrefix(p, "/")
	case strings.HasPrefix(p, dir+"/"):
		return p[len(dir)+1:], true
	}
	return "", false
}

// mountOf is the mount p (a physical path) lives on, found as the kernel finds
// it: from the top of the stack at /, repeatedly into the child mount (one
// mounted on the current one) whose mount point is the nearest at or above p,
// to the top of that point's stack. A mount whose parent was itself covered
// later (/a/b on a first /a, then a second /a over it) is never reached, so it
// cannot stand in for what is visible there. The longest mount point alone is
// not enough. nil when the table has no / or none holds p.
func mountOf(ms []mountEntry, p string) *mountEntry {
	ids := make(map[int]bool, len(ms))
	for i := range ms {
		ids[ms[i].id] = true
	}
	var cur *mountEntry
	for i := range ms {
		// The root: mounted at /, on nothing the table shows (outside a chroot)
		// or on something that is not at /.
		if ms[i].point == "/" && (ms[i].parent == ms[i].id || !ids[ms[i].parent] || !onSlash(ms, ms[i].parent)) {
			cur = &ms[i]
			break
		}
	}
	if cur == nil {
		return nil
	}
	if cur = stackTop(ms, cur); cur == nil {
		return nil
	}
	for steps := 0; steps < len(ms); steps++ {
		var next *mountEntry
		for i := range ms {
			c := &ms[i]
			if c.parent != cur.id || c.point == cur.point {
				continue
			}
			if _, ok := relUnder(p, c.point); ok && (next == nil || len(c.point) < len(next.point)) {
				next = c
			}
		}
		if next == nil {
			return cur
		}
		if cur = stackTop(ms, next); cur == nil {
			return nil
		}
	}
	return nil // a loop in the parent ids: not a table to trust
}

// onSlash reports whether the mount id is one mounted at /.
func onSlash(ms []mountEntry, id int) bool {
	for i := range ms {
		if ms[i].id == id {
			return ms[i].point == "/"
		}
	}
	return false
}

// stackTop is the mount on top of m's mount point: one mounted later at the
// same point has m as its parent. Of several (not seen in practice), the last
// listed. nil when the parents loop at that point.
func stackTop(ms []mountEntry, m *mountEntry) *mountEntry {
	for steps := 0; steps < len(ms); steps++ {
		var up *mountEntry
		for i := range ms {
			if ms[i].parent == m.id && ms[i].point == m.point && &ms[i] != m {
				up = &ms[i]
			}
		}
		if up == nil {
			return m
		}
		m = up
	}
	return nil // a loop at one mount point: not a table to trust
}

// visibleIn reports whether m is the mount found at its own mount point.
func (m mountEntry) visibleIn(ms []mountEntry) bool {
	v := mountOf(ms, m.point)
	return v != nil && v.id == m.id
}

// fsPath is where p (on mount m) is inside m's filesystem.
func fsPath(m *mountEntry, p string) string {
	rel, _ := relUnder(p, m.point)
	return path.Join(m.root, rel)
}

// errNotSameDir is a folder the mount table places in the workspace while the
// disk says it is another directory: something the table does not show (a
// mount in another namespace, a race) is in the way, so it is not guessed at.
var errNotSameDir = errors.New("not the same folder on disk")

// mountTableError is a mount table that cannot be read, parsed or followed to
// the mount a path lives on. It is about CuliBox's view of the host, not about
// the path the walk was given, so its refusal says so (mountTableRefusal).
type mountTableError struct{ err error }

func (e *mountTableError) Error() string { return "the mount table cannot be read: " + e.err.Error() }
func (e *mountTableError) Unwrap() error { return e.err }

// mountTableRefusal is the refusal for a walk that failed on the mount table,
// nil for any other error.
func mountTableRefusal(err error) error {
	var mt *mountTableError
	if !errors.As(err, &mt) {
		return nil
	}
	return refuseInPlace("CuliBox cannot read or follow its mount table (/proc/self/mountinfo: %v), so it cannot tell whether a "+
		"folder git uses is a folder of the workspace mounted elsewhere. %s", mt.err, inPlaceTrustedHost)
}

// wsAlias reports where in the workspace ws the host path x (a physical path,
// already found not to be ws itself) is: its path relative to ws, or "" when it
// is not in the workspace. x is in it when its filesystem folder lies under
// the workspace's (same device), or under the root of a mount below ws, and
// the folder at that path in ws is x on disk (sameHostDir). A candidate the
// disk does not confirm is an error, as is a table that does not hold x or ws.
func wsAlias(ms []mountEntry, x, ws string) (string, error) {
	mx, mws := mountOf(ms, x), mountOf(ms, ws)
	if mx == nil {
		return "", &mountTableError{fmt.Errorf("it holds no mount for %s", x)}
	}
	if mws == nil {
		return "", &mountTableError{fmt.Errorf("it holds no mount for %s", ws)}
	}
	fx := fsPath(mx, x)
	var cand string
	for i := range ms {
		m := &ms[i]
		if m.dev != mx.dev {
			continue
		}
		var base, fbase string
		switch {
		case m == mws:
			base, fbase = ws, fsPath(mws, ws)
		case m.point != ws && strings.HasPrefix(m.point, ws+"/") && m.visibleIn(ms):
			base, fbase = m.point, m.root
		default:
			continue
		}
		rel, ok := relUnder(fx, fbase)
		if !ok {
			continue
		}
		p := path.Join(base, rel)
		if sameHostDir(x, p) {
			r, _ := relUnder(p, ws)
			return r, nil
		}
		cand = p
	}
	if cand != "" {
		return "", fmt.Errorf("%s is mounted from %s in the workspace, but is %w", x, cand, errNotSameDir)
	}
	return "", nil
}
