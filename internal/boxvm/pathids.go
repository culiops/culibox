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
// names the position, never the path. Each path runs in its own subshell, so
// one path's cwd never reaches the next. The path is split into its parent d
// and its leaf l, and the script refuses unless
//   - the path is absolute and clean: d/l rebuilds it exactly, and l is not
//     empty (a trailing slash, or "/" itself), "." or "..";
//   - `cd -P` into d lands in a directory whose real path is d itself
//     (`pwd -P`, which dash answers with the getcwd taken when the directory
//     was entered, so it describes the directory as it was then): no parent
//     component is a symlink, and the directory held now is the one that sits
//     at d. The path is read with a sentinel, `$(pwd -P; echo x)`, because
//     command substitution would otherwise strip a trailing newline from a
//     real directory name and accept a symlink to it;
//   - the leaf, looked up as ./l inside that held directory (so the parents
//     are never looked up again), is a directory or a regular file, by ONE
//     lstat (`stat` without -L) that gives its mode and its identity together:
//     separate -L, -d and -f lookups could each see a different file if the
//     leaf were exchanged for a FIFO or a symlink between them. The same lstat
//     a second time must give the same answer.
//
// Resolving the whole path again for each check is what this replaces: GNU
// stat follows every parent component, so a parent flipped between a real
// directory and a symlink during the script could show a different directory
// to each stat, and the script could print the symlink target's identity.
//
// stat -c %d:%i is what the in-box check prints (see Manager.checkMountIDs).
// The exit status is not the only signal: PathIDs also demands one ctxid line
// per path, so a script mangled on its way here fails closed.
const pathIDsScript = `die() { echo "$1" >&2; exit 1; }
i=0
for e in @LIST@; do
i=$((i+1))
(
p=$(printf %s "$e" | base64 -d; echo x); p=${p%x}
case $p in /*) ;; *) die "path $i: not an absolute path";; esac
d=${p%/*}; l=${p##*/}
[ -n "$d" ] || d=/
case $l in ""|.|..) die "path $i: not a clean path";; esac
if [ "$d" = / ]; then w=/$l; else w=$d/$l; fi
[ "$w" = "$p" ] || die "path $i: not a clean path"
cd -P "$d" 2>/dev/null || die "path $i: not found or unreadable"
c=$(pwd -P; echo x); c=${c%?x}
[ "$c" = "$d" ] || die "path $i: a parent directory is a symlink, or the path is not clean"
s=$(stat -c '%d:%i %f' -- "./$l" 2>/dev/null) || die "path $i: not found or unreadable"
case $((0x${s#* } & 0xF000)) in 16384|32768) ;; *) die "path $i: a symlink, or not a directory or a regular file";; esac
t=$(stat -c '%d:%i %f' -- "./$l" 2>/dev/null) || die "path $i: not found or unreadable"
[ "$s" = "$t" ] || die "path $i: changed during the check"
echo "ctxid ${s% *}"
) || exit 1
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
// It fails closed: an error for any path that is not absolute and clean (or
// holds a NUL), has a symlink as a parent directory, is itself a symlink or not
// a directory or a regular file, or whose leaf answered differently the second
// time it was read; and an error unless the output holds exactly one ctxid line
// per path. paths travel base64-encoded inside a fixed script
// (pathIDsScript), never as argv, so they are inert to a shell.
func (v *VM) PathIDs(ctx context.Context, paths []string) ([]FileID, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	words := make([]string, len(paths))
	for i, p := range paths {
		if !filepath.IsAbs(p) {
			// The position, never the path: the refusal built from this error
			// must not carry a byte of a path into a log or a page.
			return nil, fmt.Errorf("reading file identities in the box VM: path %d is not an absolute path", i+1)
		}
		if strings.IndexByte(p, 0) >= 0 {
			// dash drops a NUL without a word, so the script would read a path
			// other than the one listed.
			return nil, fmt.Errorf("reading file identities in the box VM: path %d contains a NUL byte", i+1)
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
