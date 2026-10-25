package clone

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/culiops/agent-box/internal/config"
)

// The spawn-time refresh (spec: clone refresh). A throwaway container with no
// network fetches host <base> into the clone as origin/<base> and fast-forwards
// local <base> when it can. It runs the box's own git against the box's own
// clone, so what it prints is box output: display only, never an input to a
// security decision. What the host trusts is computed on the host: the base
// record, the stale SHAs and whether HEAD reached a host branch.

// Why a refresh did not run a container.
const (
	SkipCreated       = "created"        // the clone was just made from host base
	SkipInUse         = "in-use"         // a live session of this instance uses the clone
	SkipOtherInstance = "other-instance" // a box of another instance holds the clone
	SkipNoBase        = "no-base"        // clone_of has neither main nor master
)

// maxReportBytes caps the container's stdout; the rest is dropped unread.
const maxReportBytes = 8 << 10

// maxStale caps the stale branch names kept from the report.
const maxStale = 50

// RefreshRequest is one refresh of a clone.
type RefreshRequest struct {
	Clone    config.Clone
	Image    string // the box image the refresh container runs
	Instance string // this daemon's instance label
	DataDir  string // "" = no base record (tests only)
	Skip     string // "" | SkipCreated | SkipInUse | SkipOtherInstance: run no container, still do the host checks
}

// Report is what the refresh container printed. Box output: display only.
type Report struct {
	Head, HeadSHA string
	Ahead, Behind int
	FFFrom, FFTo  string
	FFCount       int
	FFRefused     bool
	Stale         []string // branch names
}

// RefreshResult is what a spawn shows about its clone.
type RefreshResult struct {
	Refreshed  bool
	Skipped    string
	Error      string // sanitized
	Base       string // "main" | "master" | ""
	Branch     string // HEAD branch as ReadHead saw it; "" = detached or unknown
	Report     Report
	HeadInHost *bool // nil = unknown
	Rewritten  bool  // host-computed: StaleSHAs non-empty
	Unverified bool  // record missing, marked unverified, or unreadable
}

// refreshScript runs in the refresh container as `sh -c refreshScript
// agent-box-refresh <dir> <of> <base> <stale SHA>...`. Every value arrives as a
// positional argument and every expansion is quoted; no host or box value is
// ever part of the script text. Each git call turns hooks and fsmonitor off,
// and the host repo is named by its path, never by the `origin` remote, so a
// box-written remote.origin.url or uploadpack is not consulted. A box-written
// url.*.insteadOf still applies, but it can only redirect to paths inside the
// container's two mounts. Branch names
// come only from for-each-ref and symbolic-ref, as full refnames, so a name
// like "-x" or "$(x)" is never an option or code. Only the report goes to
// stdout; git's own output goes to stderr.
//
// It is a var only so the dockerit timeout test can swap it.
var refreshScript = `dir=$1 of=$2 base=$3
shift 3
g() { git -C "$dir" -c core.hooksPath=/dev/null -c core.fsmonitor=false "$@"; }
lref=refs/heads/$base
rref=refs/remotes/origin/$base

g fetch --no-tags --no-write-fetch-head --upload-pack=git-upload-pack --end-of-options "$of" "+$lref:$rref" >&2 || exit 1

ff=none count=
if old=$(g rev-parse --verify --quiet "$lref^{commit}"); then
	new=$(g rev-parse --verify --quiet "$rref^{commit}")
	if [ "$old" != "$new" ]; then
		ff=refused
		if g merge-base --is-ancestor "$lref" "$rref"; then
			if [ "$(g symbolic-ref --quiet HEAD)" = "$lref" ]; then
				g merge --ff-only "$rref" >&2
			else
				g fetch --no-tags --no-write-fetch-head --upload-pack=git-upload-pack --end-of-options "$of" "$lref:$lref" >&2
			fi
			if [ $? -eq 0 ] && now=$(g rev-parse --verify --quiet "$lref^{commit}") && [ "$now" != "$old" ]; then
				ff=$old..$now
				count=$(g rev-list --count "$old..$now")
			fi
		fi
	fi
fi
printf 'ff=%s\n' "$ff"
[ -n "$count" ] && printf 'ff_count=%s\n' "$count"

h=$(g symbolic-ref --quiet HEAD)
case $h in
refs/heads/*) h=${h#refs/heads/} ;;
*) h= ;;
esac
printf 'head=%s\n' "$h"
printf 'head_sha=%s\n' "$(g rev-parse --verify --quiet 'HEAD^{commit}')"
if c=$(g rev-list --left-right --count "HEAD...$rref"); then
	printf '%s\n' "$c" | { read -r a b; printf 'ahead=%s\nbehind=%s\n' "$a" "$b"; }
fi

if [ $# -gt 0 ]; then
	have=$(printf '%s\n' "$@" | g cat-file --batch-check='%(objecttype) %(objectname)')
	set --
	while read -r typ sha; do
		[ "$typ" = commit ] && set -- "$@" "--contains=$sha"
	done <<EOF
$have
EOF
	if [ $# -gt 0 ]; then
		g for-each-ref --format='%(refname)' "$@" refs/heads/ | while IFS= read -r ref; do
			printf 'stale=%s\n' "${ref#refs/heads/}"
		done
	fi
fi
exit 0
`

// RefreshArgs is the docker argv (after "docker") of the refresh container. It
// has the shape of BundleArgs, but the clone is mounted read-write (the
// fast-forward writes it), clone_of's .git read-only, and nothing else. No box
// label (claimCheck must not read it as a box), --pull=never, --init and
// --name for the kill path (runContainer). GIT_NO_REPLACE_OBJECTS: a box-written
// refs/replace must not change what the script compares.
func RefreshArgs(r RefreshRequest, base string, stale []string, name string) []string {
	of, dir := r.Clone.Of, r.Clone.Dir
	gitDir := of + "/.git"
	args := []string{
		"run", "--rm", "--init", "--name", name, "--pull=never", "--network", "none", "--cap-drop=ALL", "--security-opt", "no-new-privileges", "--read-only",
		"--tmpfs", boxTmp + ":rw,nosuid,nodev,noexec,size=64m", "--pids-limit", "128", "--memory", "1g",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"--label", LabelCloneRefresh + "=" + r.Instance,
		"-e", "HOME=" + boxTmp, "-e", "GIT_CONFIG_NOSYSTEM=1", "-e", "GIT_NO_REPLACE_OBJECTS=1",
		"--mount", "type=bind,src=" + dir + ",dst=" + dir,
		"--mount", "type=bind,src=" + gitDir + ",dst=" + gitDir + ",readonly",
		"--entrypoint", "sh", r.Image,
		"-c", refreshScript, "agent-box-refresh", dir, of, base,
	}
	return append(args, stale...)
}

// hostCheckTimeout bounds the host-side check run after the refresh container,
// which must work even when the refresh's own deadline has passed.
const hostCheckTimeout = 10 * time.Second

var countRE = regexp.MustCompile(`^[0-9]{1,9}$`)

// parseReport reads the container's report: complete lines only, known keys
// only, strict value shapes; anything else is dropped. A later valid line for
// a key replaces an earlier one, except stale, which collects (deduped, at
// most maxStale).
func parseReport(out []byte) Report {
	var r Report
	lines := strings.Split(string(out), "\n")
	lines = lines[:len(lines)-1] // after the last newline: a partial line, or nothing
	seen := map[string]bool{}
	num := func(v string) (int, bool) {
		if !countRE.MatchString(v) {
			return 0, false
		}
		n, err := strconv.Atoi(v)
		return n, err == nil
	}
	for _, line := range lines {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "head":
			if v == "" || ValidBranch(v) == nil {
				r.Head = sanitize(v)
			}
		case "head_sha":
			if shaRE.MatchString(v) {
				r.HeadSHA = sanitize(v)
			}
		case "ahead":
			if n, ok := num(v); ok {
				r.Ahead = n
			}
		case "behind":
			if n, ok := num(v); ok {
				r.Behind = n
			}
		case "ff_count":
			if n, ok := num(v); ok {
				r.FFCount = n
			}
		case "ff":
			switch from, to, _ := strings.Cut(v, ".."); {
			case v == "none":
				r.FFFrom, r.FFTo, r.FFRefused = "", "", false
			case v == "refused":
				r.FFFrom, r.FFTo, r.FFRefused = "", "", true
			case shaRE.MatchString(from) && shaRE.MatchString(to):
				r.FFFrom, r.FFTo, r.FFRefused = sanitize(from), sanitize(to), false
			}
		case "stale":
			if ValidBranch(v) == nil && !seen[v] && len(r.Stale) < maxStale {
				seen[v] = true
				r.Stale = append(r.Stale, sanitize(v))
			}
		}
	}
	return r
}

// Refresh brings host <base> into the clone through the refresh container,
// unless r.Skip says not to, and always runs the host-side checks. It is best
// effort: every failure lands in the result, never in an error. Host git runs
// only in clone_of (hardened, CleanEnv); the clone is read without git.
func Refresh(ctx context.Context, g Git, d Docker, r RefreshRequest) (res RefreshResult) {
	res.Skipped = r.Skip
	of, dir := r.Clone.Of, r.Clone.Dir
	fail := func(msg string) {
		if res.Error != "" {
			res.Error += "; "
		}
		res.Error += msg
	}
	defer func() {
		var sha string
		res.Branch, sha, _ = ReadHead(dir) // read after the container exited
		// A timed-out refresh still reports what the host can say about the
		// clone, so this check gets its own short deadline.
		hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), hostCheckTimeout)
		defer cancel()
		res.HeadInHost = HeadInHost(hctx, g, of, sha)
	}()

	base, baseSHA, err := BaseBranch(ctx, g, of)
	switch {
	case err != nil:
		fail("could not resolve the host base branch: " + sanitizeN(err.Error(), maxLongErr))
		return res
	case base == "":
		if res.Skipped == "" {
			res.Skipped = SkipNoBase
		}
		return res
	}
	res.Base = base

	var stale []string
	recorded := true
	if r.DataDir == "" {
		res.Unverified = true
	} else {
		if r.Skip == "" {
			// The record must hold every base the clone can get: no record, no refresh.
			if err := AppendBase(r.DataDir, dir, baseSHA); err != nil {
				recorded = false
				fail("could not record host " + base + ": " + sanitizeN(err.Error(), maxLongErr))
			}
		}
		rec, err := ReadBaseRecord(r.DataDir, dir)
		res.Unverified = !rec.Exists || rec.Unverified || err != nil
		if err == nil {
			stale, err = StaleSHAs(ctx, g, of, base, rec.SHAs)
			if err != nil {
				stale = nil
				fail("could not check host history: " + sanitizeN(err.Error(), maxLongErr))
			}
			res.Rewritten = len(stale) > 0
		}
	}

	if r.Skip != "" || !recorded {
		return res
	}
	name := containerName("clonerefresh", r.Instance)
	out := &capBuffer{max: maxReportBytes}
	if err := runContainer(ctx, d, name, RefreshArgs(r, base, stale, name), out); err != nil {
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			fail("timed out")
		case ctx.Err() != nil:
			fail("cancelled")
		default:
			fail("fetching host " + base + " in the box failed: " + sanitizeN(err.Error(), maxLongErr))
		}
		return res
	}
	res.Refreshed = true
	res.Report = parseReport(out.buf.Bytes())
	return res
}
