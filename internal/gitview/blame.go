package gitview

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// BlameCommit is one commit as the editor's blame shows it. Every string has
// passed cleanText: git prints what the repository holds, and an agent (or
// whoever wrote the history) chose it. Ref is a pull or merge request number
// the summary names, "#123" or "!123", or "".
type BlameCommit struct {
	SHA      string `json:"sha"`
	Author   string `json:"author"`
	Email    string `json:"email"`
	Time     int64  `json:"time"`
	Summary  string `json:"summary"`
	Ref      string `json:"ref,omitempty"`
	Boundary bool   `json:"boundary,omitempty"`
}

// The bounds of a blame field, in runes.
const (
	maxAuthorRunes  = 128
	maxEmailRunes   = 254
	maxSummaryRunes = 256
	// maxBlameLines is more lines than a 2 MiB file of empty lines holds once
	// the output cap (maxBlameBytes) is applied; anything past it is not git.
	maxBlameLines = 2_000_000
)

var errBadBlame = errors.New("unexpected blame output")

// ZeroSHA reports whether sha is git's id for "not committed yet".
func ZeroSHA(sha string) bool {
	return sha != "" && strings.Trim(sha, "0") == ""
}

// cleanText makes s safe to show as one line of plain text: valid UTF-8, with
// no control characters and no bidirectional controls (which would let a name
// or summary reorder the text around it on screen), cut to max runes with an
// ellipsis.
func cleanText(s string, max int) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r):
			return -1
		case r == 0x061C, r == 0x200E, r == 0x200F, r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069:
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max-1]) + "…"
	}
	return s
}

var (
	refParenRE = regexp.MustCompile(`\(([#!])([1-9][0-9]{0,8})\)\s*$`)
	refMergeRE = regexp.MustCompile(`^Merge pull request #([1-9][0-9]{0,8})(\s|$)`)
)

// refOf is the pull or merge request a summary names: "Title (#12)" (GitHub's
// squash), "Merge pull request #12 from …" (GitHub's merge), "Title (!12)"
// (GitLab), as "#12" or "!12"; "" otherwise. Which host gets a link for which
// form is the client's choice (GitLab's "#12" is an issue).
func refOf(summary string) string {
	if m := refParenRE.FindStringSubmatch(summary); m != nil {
		return m[1] + m[2]
	}
	if m := refMergeRE.FindStringSubmatch(summary); m != nil {
		return "#" + m[1]
	}
	return ""
}

// validSHA reports whether s is a full lowercase object id (SHA-1 or SHA-256).
func validSHA(s string) bool { return oidRE.MatchString(s) }

func positive(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n > 0 && n <= maxBlameLines
}

// ParseBlame reads git blame --porcelain into the commits it names, in the
// order first seen, and the file's lines as runs of [commit index, line count]
// in line order. Output that does not follow the format exactly (final lines
// not 1, 2, 3…, a group whose header count and content lines differ, a commit
// without its metadata, an id that is not one) is refused whole: the caller
// shows no blame rather than a wrong one.
func ParseBlame(b []byte) ([]BlameCommit, [][2]int, error) {
	commits := []BlameCommit{}
	lines := [][2]int{}
	index := map[string]int{}
	seenTime := map[int]bool{}
	cur := -1        // the commit of the current line
	pending := false // a header was read, its content line not yet
	fresh := false   // the current group is the first to name its commit
	left := 0        // lines left in the current group, counting the pending one
	next := 1        // the final line number the next header must carry
	for len(b) > 0 {
		var line []byte
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			line, b = b[:i], b[i+1:]
		} else {
			line, b = b, nil
		}
		if len(line) > 0 && line[0] == '\t' {
			if !pending {
				return nil, nil, errBadBlame
			}
			pending = false
			left--
			if n := len(lines); n > 0 && lines[n-1][0] == cur {
				lines[n-1][1]++
			} else {
				lines = append(lines, [2]int{cur, 1})
			}
			continue
		}
		if pending {
			// Metadata of the commit just named, before its content line.
			key, val, _ := strings.Cut(string(line), " ")
			if validSHA(key) {
				// A header where metadata or a content line must be: a name or summary holding a newline.
				return nil, nil, errBadBlame
			}
			if !fresh {
				continue // git prints a commit's metadata once, in the group that first names it
			}
			c := &commits[cur]
			switch key {
			case "author":
				c.Author = cleanText(val, maxAuthorRunes)
			case "author-mail":
				c.Email = cleanText(strings.TrimSuffix(strings.TrimPrefix(val, "<"), ">"), maxEmailRunes)
			case "author-time":
				t, err := strconv.ParseInt(val, 10, 64)
				if err != nil {
					return nil, nil, errBadBlame
				}
				c.Time = t
				seenTime[cur] = true
			case "summary":
				c.Summary = cleanText(val, maxSummaryRunes)
				c.Ref = refOf(c.Summary)
			case "boundary":
				c.Boundary = true
			}
			continue
		}
		// A header: "<sha> <orig> <final> <count>" to open a group, "<sha> <orig> <final>" inside one.
		f := strings.Fields(string(line))
		if len(f) != 3 && len(f) != 4 || !validSHA(f[0]) {
			return nil, nil, errBadBlame
		}
		if _, ok := positive(f[1]); !ok {
			return nil, nil, errBadBlame
		}
		if final, ok := positive(f[2]); !ok || final != next {
			return nil, nil, errBadBlame
		}
		if len(f) == 4 {
			n, ok := positive(f[3])
			if !ok || left != 0 {
				return nil, nil, errBadBlame
			}
			left = n
		} else if left == 0 {
			return nil, nil, errBadBlame
		}
		i, seen := index[f[0]]
		if !seen {
			i = len(commits)
			index[f[0]] = i
			commits = append(commits, BlameCommit{SHA: f[0]})
		} else if len(f) == 3 && i != cur {
			return nil, nil, errBadBlame
		}
		fresh = !seen
		cur = i
		pending = true
		next++
		if next > maxBlameLines+1 {
			return nil, nil, errBadBlame
		}
	}
	if pending || left != 0 {
		return nil, nil, errBadBlame
	}
	for i := range commits {
		if !seenTime[i] {
			return nil, nil, errBadBlame
		}
	}
	return commits, lines, nil
}

// ParseLogLast reads LogLastArgs' record: five NUL-terminated fields and git's
// newline. Empty output (no commit touched the path) is nil, nil; anything else
// that is not exactly that record is refused.
func ParseLogLast(b []byte) (*BlameCommit, error) {
	b = bytes.TrimSuffix(b, []byte("\n"))
	if len(b) == 0 {
		return nil, nil
	}
	f := strings.Split(string(b), "\x00")
	if len(f) != 6 || f[5] != "" || !validSHA(f[0]) {
		return nil, errBadBlame
	}
	t, err := strconv.ParseInt(f[3], 10, 64)
	if err != nil {
		return nil, errBadBlame
	}
	c := &BlameCommit{SHA: f[0], Author: cleanText(f[1], maxAuthorRunes), Email: cleanText(f[2], maxEmailRunes), Time: t,
		Summary: cleanText(f[4], maxSummaryRunes)}
	c.Ref = refOf(c.Summary)
	return c, nil
}

// BlameKey is what a blame answer is kept under besides the target and path:
// the HEAD it was read at, the sha256 of the file it describes (both from the
// caller, which reads them before asking), and whether IgnoreRevsName was
// passed.
type BlameKey struct {
	Head, Version string
	IgnoreRevs    bool
}

// Blame is one file's line blame and its last commit. State is one of ok,
// untracked (git has no history for the path, or the repository none at all),
// slow (a run hit the Timeout), toobig (the output passed maxBlameBytes),
// changed (the file changed while git read it), notrepo, unavailable. Lines are
// runs of [index into Commits, line count] over the working-tree file, whose
// uncommitted lines name the all-zero commit. Last is the last commit that
// touched the path, nil when none did. IgnoredRevs reports that IgnoreRevsName
// was applied. Reason carries git's stderr only for unavailable, so a web
// handler must not forward it as it is.
type Blame struct {
	State       string        `json:"state"`
	Reason      string        `json:"reason,omitempty"`
	Last        *BlameCommit  `json:"last,omitempty"`
	Commits     []BlameCommit `json:"commits,omitempty"`
	Lines       [][2]int      `json:"lines,omitempty"`
	IgnoredRevs bool          `json:"ignoredRevs,omitempty"`
}

const (
	// maxBlameBytes bounds the porcelain buffer: a 2 MiB file of ~40-byte lines
	// prints some 6 MiB (each line once, plus a ~45-byte header per line and the
	// metadata of each commit once). Past it the answer is toobig.
	maxBlameBytes = 16 << 20
	// maxLogBytes bounds the last-commit record (one id, two idents, a summary).
	maxLogBytes = 64 << 10
	// maxBlameCache is how many answers are kept, the least recently used going first.
	maxBlameCache = 32
	// blameWait bounds the wait for the one blame turn (see Blame).
	blameWait = 30 * time.Second
)

var (
	// noHistoryRE is git saying the path or the repository has no commit to blame against.
	noHistoryRE  = regexp.MustCompile(`(no such path '.*' in HEAD|no such ref: HEAD|does not have any commits yet)\s*$`)
	errBlameTurn = errors.New("timed out waiting for the blame turn")
)

type blameCached struct {
	b        Blame
	lastUsed time.Time
}

// Blame returns the line blame and the last commit of rel (relative to t.Top):
// a log run, then a blame run, each in one slot at prioBlame, the lowest. Only
// one Blame runs at a time per Runner, so at most one of the MaxParallel slots
// ever holds a blame and the tree's status always has the other (anti-goal A2).
// The wait for that turn ends with ctx (a client that went away) or after
// blameWait. ok and untracked answers are kept under (t, rel, k), the
// maxBlameCache most recently used, but only when still() holds after the runs:
// the caller re-reads the file there, and a file that changed meanwhile is
// "changed", not kept. With k.IgnoreRevs, a blame that fails for any reason but
// a timeout or the cap is run once more without the file (IgnoredRevs false).
func (r *Runner) Blame(ctx context.Context, t Target, rel string, k BlameKey, still func() bool) Blame {
	if why := t.Unsafe(); why != "" {
		return Blame{State: "unavailable", Reason: why}
	}
	if !safePath(rel) {
		return Blame{State: "unavailable", Reason: "invalid path"}
	}
	key := t.key() + "\x00" + rel + "\x00" + k.Head + "\x00" + k.Version + "\x00" + strconv.FormatBool(k.IgnoreRevs)
	if b, ok := r.blameCachedGet(key); ok {
		return b
	}
	release, err := r.blameTurn(ctx)
	if err != nil {
		return Blame{State: "unavailable", Reason: errBlameTurn.Error()}
	}
	defer release()
	// Another request for the same file may have run while this one waited.
	if b, ok := r.blameCachedGet(key); ok {
		return b
	}
	b := r.doBlame(ctx, t, rel, k.IgnoreRevs)
	if b.State != "ok" && b.State != "untracked" {
		return b
	}
	if !still() {
		return Blame{State: "changed"}
	}
	r.mu.Lock()
	if r.blameCache == nil {
		r.blameCache = map[string]*blameCached{}
	}
	r.blameCache[key] = &blameCached{b: cloneBlame(b), lastUsed: r.now()}
	for len(r.blameCache) > maxBlameCache {
		oldest := ""
		for k, c := range r.blameCache {
			if oldest == "" || c.lastUsed.Before(r.blameCache[oldest].lastUsed) {
				oldest = k
			}
		}
		delete(r.blameCache, oldest)
	}
	r.mu.Unlock()
	return b
}

func (r *Runner) blameCachedGet(key string) (Blame, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.blameCache[key]
	if !ok {
		return Blame{}, false
	}
	c.lastUsed = r.now()
	return cloneBlame(c.b), true
}

// blameTurn waits for the one blame turn of the Runner and returns its release.
func (r *Runner) blameTurn(ctx context.Context) (func(), error) {
	r.mu.Lock()
	if r.blameSem == nil {
		r.blameSem = make(chan struct{}, 1)
	}
	sem := r.blameSem
	r.mu.Unlock()
	wctx, cancel := context.WithTimeout(ctx, blameWait)
	defer cancel()
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, nil
	case <-wctx.Done():
		return nil, wctx.Err()
	}
}

func (r *Runner) doBlame(ctx context.Context, t Target, rel string, ignoreRevs bool) Blame {
	out, err := r.runCapped(ctx, prioBlame, func(name string) []string {
		return LogLastArgs(t, r.Image, name, r.UID, r.GID, rel)
	}, maxLogBytes)
	if err != nil {
		return blameFailed(err)
	}
	last, err := ParseLogLast(out)
	if err != nil {
		return Blame{State: "unavailable", Reason: "unexpected log output"}
	}
	run := func(ignore bool) ([]byte, error) {
		return r.runCapped(ctx, prioBlame, func(name string) []string {
			return BlameArgs(t, r.Image, name, r.UID, r.GID, rel, ignore)
		}, maxBlameBytes)
	}
	out, err = run(ignoreRevs)
	if err != nil && ignoreRevs && !errors.Is(err, errTimedOut) && !errors.Is(err, errCapped) && !errors.Is(err, errNoSlot) && ctx.Err() == nil {
		ignoreRevs = false
		out, err = run(false)
	}
	if err != nil {
		return blameFailed(err)
	}
	commits, lines, err := ParseBlame(out)
	if err != nil {
		return Blame{State: "unavailable", Reason: "unexpected blame output"}
	}
	return Blame{State: "ok", Last: last, Commits: commits, Lines: lines, IgnoredRevs: ignoreRevs}
}

// blameFailed is the answer for a failed run.
func blameFailed(err error) Blame {
	msg := strings.TrimSpace(err.Error())
	switch {
	case errors.Is(err, errTimedOut):
		return Blame{State: "slow"}
	case errors.Is(err, errCapped):
		return Blame{State: "toobig"}
	case exitCode(err) == 128 && noHistoryRE.MatchString(msg):
		return Blame{State: "untracked"}
	case strings.Contains(msg, "not a git repository"):
		return Blame{State: "notrepo"}
	}
	return Blame{State: "unavailable", Reason: failReasonText(msg)}
}

func cloneBlame(b Blame) Blame {
	if b.Last != nil {
		c := *b.Last
		b.Last = &c
	}
	b.Commits = slices.Clone(b.Commits)
	b.Lines = slices.Clone(b.Lines)
	return b
}
