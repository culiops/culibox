package boxvm

import (
	"context"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// FileID is a file's (device, inode) as the box VM's kernel reports it.
type FileID struct{ Dev, Ino uint64 }

// pathIDsScript runs in the VM, under `sh -c`: Ubuntu's dash and GNU coreutils.
// @LIST@ is the space-separated base64 of the paths, nothing else: base64's
// alphabet (A-Z a-z 0-9 + / =) is inert to every shell, so no byte of a path can
// ever be read as syntax, whether or not colima quotes the argv it forwards.
// Each word is decoded by the loop below, with an "x" appended and stripped so
// that `$(...)` cannot eat a newline the path ends in.
//
// Per path it prints `ctxid <dev>:<ino>` or stops with a one-line reason that
// names the position, never the path. It refuses unless
//   - realpath -e prints the path back unchanged: the path is absolute and
//     clean and no component of it, the leaf included, is a symlink right now
//     (a plain stat follows every symlink but the leaf's, so it alone would not
//     do; this is the check CheckContextPathID's fd walk makes on Linux);
//   - the leaf is a directory or a regular file (so no socket, FIFO or device);
//   - stat before and after those checks gives one identity, so the path is the
//     same file throughout them.
//
// stat -c %d:%i is what the in-box check prints (see Manager.checkMountIDs).
// The exit status is not the only signal: PathIDs also demands one ctxid line
// per path, so a script mangled on its way here fails closed.
const pathIDsScript = `die() { echo "$1" >&2; exit 1; }
i=0
for e in @LIST@; do
i=$((i+1))
p=$(printf %s "$e" | base64 -d; echo x); p=${p%x}
a=$(stat -c %d:%i -- "$p" 2>/dev/null) || die "path $i: not found or unreadable"
r=$(realpath -e -- "$p" 2>/dev/null) || die "path $i: not found or unreadable"
[ "$r" = "$p" ] || die "path $i: a component is a symlink, or the path is not clean"
[ -d "$p" ] || [ -f "$p" ] || die "path $i: not a directory or a regular file"
b=$(stat -c %d:%i -- "$p" 2>/dev/null) || die "path $i: not found or unreadable"
[ "$a" = "$b" ] || die "path $i: changed during the check"
echo "ctxid $a"
done
`

var ctxIDLine = regexp.MustCompile(`^ctxid ([0-9]+):([0-9]+)$`)

// PathIDs returns the identity of each path as the box VM sees it, in order,
// from ONE `colima ssh` for all of them. On macOS an inode number from the Mac
// is not the one virtiofs shows the VM and the box, but the VM's is the box's
// (spiked on colima 0.8.1: the VM and the container bind view agree), so this
// stands in for the host's identity. Like InputRules it only asks: a stopped VM
// is an error, never a start.
//
// It fails closed: an error for any path that is not absolute, is or is under a
// symlink, is not a directory or a regular file, or whose identity changed
// while it was read; and an error unless the output holds exactly one ctxid
// line per path. paths travel base64-encoded inside a fixed script
// (pathIDsScript), never as argv, so they are inert to a shell.
func (v *VM) PathIDs(ctx context.Context, paths []string) ([]FileID, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	words := make([]string, len(paths))
	for i, p := range paths {
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("reading file identities in the box VM: %q is not an absolute path", p)
		}
		words[i] = base64.StdEncoding.EncodeToString([]byte(p))
	}
	if err := v.preflight(); err != nil {
		return nil, err
	}
	script := strings.Replace(pathIDsScript, "@LIST@", strings.Join(words, " "), 1)
	out, err := v.r.Run(ctx, v.env(), v.cfg.ColimaBin, "ssh", "--profile", Profile, "--", "sh", "-c", script)
	if err != nil {
		return nil, fmt.Errorf("reading file identities in the box VM: %w", err)
	}
	var ids []FileID
	for _, line := range strings.Split(out, "\n") {
		m := ctxIDLine.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue // colima or lima chatter around the answer
		}
		dev, derr := strconv.ParseUint(m[1], 10, 64)
		ino, ierr := strconv.ParseUint(m[2], 10, 64)
		if derr != nil || ierr != nil {
			return nil, fmt.Errorf("reading file identities in the box VM: unreadable identity %q", line)
		}
		ids = append(ids, FileID{Dev: dev, Ino: ino})
	}
	if len(ids) != len(paths) {
		return nil, fmt.Errorf("reading file identities in the box VM: got %d identities for %d paths", len(ids), len(paths))
	}
	return ids, nil
}
