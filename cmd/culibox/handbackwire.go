package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/handback"
	"github.com/culiops/culibox/internal/session"
)

// The in-place scan trusts a workspace only if the box layer vouches for it
// (session.spawnWorkspace asks through an optional interface, so a signature
// drift would silently turn the check off): this fails the build instead.
var _ interface {
	NamedFor(real string, key box.BoxKey, name string) bool
} = (*box.Manager)(nil)

// handbackDir is the record store under the data dir: one file per in-place
// workspace or returned clone branch, until the user marks it reviewed. The
// daemon and `culibox clone fetch` write the same files, under a file lock.
func handbackDir(dataDir string) string { return filepath.Join(dataDir, "handback") }

// handbackStopTimeout bounds one scan the box-stop hook runs: the walk's own
// budget plus the hashing of what it flagged.
const handbackStopTimeout = 45 * time.Second

// handbackHooks is the daemon's session.HandbackHooks (the in-place scan at a
// session's exit) and the box layer's stop hook (the scan when a box stops),
// plus the recording of a clone return's flags. It works on what a box wrote,
// so it never runs, opens or parses any of it beyond what internal/handback
// does.
type handbackHooks struct {
	store handback.Store
	// owned reports whether a directory is box-owned (a clone's dir): a clone's
	// flags come from its returned branches, never from walking the clone. nil
	// = nothing is known to be owned.
	owned func(dir string) bool

	// Swappable for tests.
	inspect func(ctx context.Context, root *os.Root, since time.Time, lim handback.Limits) (handback.Report, error)
	now     func() time.Time
	limits  handback.Limits // zero fields fall back to the defaults
}

func newHandbackHooks(dataDir string, owned func(string) bool) *handbackHooks {
	return &handbackHooks{
		store:   handback.Store{Dir: handbackDir(dataDir)},
		owned:   owned,
		inspect: handback.InspectRoot,
		now:     time.Now,
	}
}

// unknownStartNote is the report's own sentence when a scan has no usable
// start: the session's start time was lost (a restart adopted it) and no
// earlier scan of the workspace is on record.
func unknownStartNote(since time.Time) string {
	return fmt.Sprintf("start time unknown after a restart; files written before %s were not checked", since.UTC().Format(time.RFC3339))
}

const (
	pathChangedNote  = "the workspace path changed since the session started; it was not scanned"
	scanPanickedNote = "the scan stopped with an internal error"
	// notReachedNote is added when a walk stopped early: the window still moves
	// on, so what it did not reach is left to the user.
	notReachedNote = "files this scan did not reach are not checked by a later scan"
)

// errWorkspaceMoved: the path no longer leads to the directory the box was made over.
var errWorkspaceMoved = errors.New("workspace path changed")

// openWorkspace opens the workspace for walking, and only if it still is the
// directory the box was made over. By the time a scan runs, another box with an
// overlapping folder may have renamed the workspace and put a symlink to
// somewhere else (~/.aws) in its place: the host walk must never follow it.
// path must already be the real path; it is checked to still resolve to itself
// before and after the open, and the opened directory must be the one Lstat
// sees at path (a directory, not a symlink).
func openWorkspace(path string) (*os.Root, error) {
	stable := func() error {
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
		if real != path {
			return errWorkspaceMoved
		}
		return nil
	}
	if err := stable(); err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	opened, err := r.Stat(".")
	lst, lerr := os.Lstat(path)
	if err != nil || lerr != nil || !lst.IsDir() || !os.SameFile(opened, lst) || stable() != nil {
		r.Close()
		return nil, errWorkspaceMoved
	}
	return r, nil
}

// ScanInPlace inspects what a box wrote into workspace (the real path the box
// was made over) since `since` and records it. An exit scan's window starts at
// `since` or at the last scan on record, whichever is earlier, when that last
// scan was not a complete box-stop scan, so a gap between scans (a process that
// kept writing after a session, a box reaped at boot) is never left unchecked;
// after a complete stop scan the gap was already checked and the window starts
// at `since`. With sinceKnown false the start is a guess: the last scan on
// record replaces it, and with none the record says files before the guess
// were not checked. The scan's start is taken before the walk, so a file
// written during it is seen by the next scan. A walk that stopped early
// (Report.Incomplete: its time budget, the entry or candidate cap, a cancel)
// moves the window on too, or a tree over a cap would be walked to the same
// cap at full cost on every exit and stop: the record says what it did not
// reach (notReachedNote) until the user marks it reviewed. Only a scan that
// could not walk at all (the workspace could not be opened or read) leaves the
// window at its own start, so the next scan covers it. A scan with nothing flagged still writes
// a record: the next scan (a box stop) starts from it. A workspace that cannot
// be opened, whose path changed, or whose walk panicked is recorded with a
// note saying it was not (fully) scanned. The note's Count is what this scan
// flagged, not what the record holds.
func (h *handbackHooks) ScanInPlace(ctx context.Context, workspace string, since time.Time, sinceKnown bool) (session.HandbackNote, error) {
	return h.scanInPlace(ctx, workspace, since, sinceKnown, handback.ScanExit)
}

// scanInPlace is ScanInPlace for either caller: kind says whether it is a
// session's exit scan or the box-stop scan, and is written into the record.
func (h *handbackHooks) scanInPlace(ctx context.Context, workspace string, since time.Time, sinceKnown bool, kind string) (session.HandbackNote, error) {
	key := handback.InPlaceKey(workspace)
	scanAt := h.now()
	note := ""
	info, haveLast, err := h.store.LastScanInfo(key)
	last := info.At
	if err != nil {
		// A record that cannot be read does not stop the scan: Merge decides
		// whether it can be replaced, and says so if it cannot.
		log.Printf("handback: last scan of %q unreadable (%v); using the session's start", workspace, err)
	}
	switch {
	case haveLast && !sinceKnown:
		since = last
	case haveLast && kind == handback.ScanExit && !(info.Kind == handback.ScanStop && info.Complete) && last.Before(since):
		// The last check did not cover the time up to now (an exit scan only
		// saw its own session, or the stop scan was cut short): reach back.
		since = last
	case !haveLast && !sinceKnown:
		note = unknownStartNote(since)
	}

	var rep handback.Report
	walked := false
	r, err := openWorkspace(workspace)
	switch {
	case err != nil:
		rep.Truncated = "the workspace could not be opened: " + handback.SanitizePath(err.Error())
		if errors.Is(err, errWorkspaceMoved) {
			rep.Truncated = pathChangedNote
		}
		rep.Incomplete = true
	default:
		rep, walked = h.safeInspect(ctx, r, since)
		r.Close()
	}
	switch {
	case rep.Incomplete && !walked:
		scanAt = since // nothing was looked at, at no cost: the next scan covers this window
	case rep.Incomplete:
		// A walk cut short by a cap would stop at the same cap again if the
		// window stayed put, at its full cost on every exit and stop. It moves
		// on, and the record says what was left unchecked until it is reviewed.
		rep.Truncated = joinNotes(rep.Truncated, notReachedNote)
	}
	if note != "" {
		rep.Truncated = joinNotes(rep.Truncated, note)
	}
	base := handback.Record{Kind: handback.KindInPlace, Workspace: workspace, LastScanKind: kind}
	rec, fresh, err := h.store.MergeScan(key, base, scanAt, rep)
	if err != nil {
		return session.HandbackNote{}, fmt.Errorf("cannot record what was flagged in %s: %w", workspace, err)
	}
	return session.HandbackNote{ID: rec.ID, Count: fresh, InPlace: true, Truncated: rep.Truncated != ""}, nil
}

func joinNotes(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

// safeInspect runs the walk and turns a panic (hostile input reaching a bug)
// into a report that says the scan stopped, instead of taking the daemon down.
// A walk that errors is reported the same way. walked is false only when the
// walk could not start (its error): a panic comes after the walk has spent
// its time, so it counts as walked.
func (h *handbackHooks) safeInspect(ctx context.Context, r *os.Root, since time.Time) (rep handback.Report, walked bool) {
	defer func() {
		if p := recover(); p != nil {
			log.Printf("handback: scan of %q stopped with an internal error: %v", r.Name(), p)
			rep, walked = handback.Report{Truncated: scanPanickedNote, Incomplete: true}, true
		}
	}()
	rep, err := h.inspect(ctx, r, since, h.limits.WithDefaults())
	if err != nil {
		return handback.Report{Truncated: "the workspace could not be read: " + handback.SanitizePath(err.Error()), Incomplete: true}, false
	}
	return rep, true
}

// OnStop is the box layer's stop hook: a box over an in-place workspace was
// stopped, and files its sessions wrote since the last scan are on the host.
// It scans only a workspace that has a record (a session of ours ended there
// and scanned it) and is not a clone: a clone's flags come from its branches.
// Failures and panics are logged; the stop has already happened.
func (h *handbackHooks) OnStop(name, workspace string) {
	defer func() {
		if p := recover(); p != nil {
			log.Printf("handback: box %s stopped; scan of %q stopped with an internal error: %v", name, workspace, p)
		}
	}()
	if workspace == "" || (h.owned != nil && h.owned(workspace)) {
		return
	}
	last, ok, err := h.store.LastScan(handback.InPlaceKey(workspace))
	if err != nil {
		log.Printf("handback: box %s stopped; last scan of %q unreadable: %v", name, workspace, err)
		return
	}
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), handbackStopTimeout)
	defer cancel()
	if _, err := h.scanInPlace(ctx, workspace, last, true, handback.ScanStop); err != nil {
		log.Printf("handback: box %s stopped; scan of %q: %v", name, workspace, err)
	}
}

// cloneBranchKey is the record key of one returned clone branch. branch is the
// clone's branch name; the host sees it as box/<branch>.
func cloneBranchKey(of, branch string) string { return handback.CloneKey(of, branch) }

// recordClone merges one returned branch's report into its record and returns
// the note. hostRef is the branch as the host sees it ("box/<branch>"). A zero-
// flag report is merged too, so a record left by an earlier return of the same
// branch is brought up to date. ok is false (with the reason in err) when the
// record could not be written.
func (h *handbackHooks) recordClone(of, branch, hostRef string, rep handback.Report) (session.HandbackNote, error) {
	base := handback.Record{Kind: handback.KindClone, CloneOf: of, Branch: hostRef}
	rec, fresh, err := h.store.MergeScan(cloneBranchKey(of, branch), base, h.now(), rep)
	if err != nil {
		return session.HandbackNote{}, err
	}
	// Count is what is new to the user: a file already reviewed and handed back
	// unchanged is not counted (nor listed).
	return session.HandbackNote{ID: rec.ID, Count: fresh, Branch: hostRef, Truncated: rep.Truncated != ""}, nil
}

// flaggedOf records every branch of a return that came home and lists the ones
// with flags or with a scan that was cut short. A record that cannot be
// written is logged and said in warnings (never dropped silently: the flags
// would be lost with it). Branches are visited in sorted order so the summary
// is stable.
func (h *handbackHooks) flaggedOf(of string, reports map[string]handback.Report) (flagged []session.HandbackNote, warnings []string) {
	if h == nil {
		return nil, nil
	}
	for _, hostRef := range slices.Sorted(maps.Keys(reports)) {
		rep := reports[hostRef]
		branch := strings.TrimPrefix(hostRef, "box/")
		note, err := h.recordClone(of, branch, hostRef, rep)
		if err != nil {
			log.Printf("handback: cannot record the flags of %s from %s: %v", hostRef, of, err)
			msg := fmt.Sprintf("%s: could not record what was flagged for review: %s", hostRef, handback.SanitizePath(err.Error()))
			if n := len(rep.Flags); n > 0 {
				msg = fmt.Sprintf("%s: %d file(s) flagged for review, but they could not be recorded: %s", hostRef, n, handback.SanitizePath(err.Error()))
			}
			warnings = append(warnings, msg)
			continue
		}
		if note.Count > 0 || note.Truncated {
			flagged = append(flagged, note)
		}
	}
	return flagged, warnings
}

// printFlags writes the flagged files of one fetched branch after the review
// line: path, detail, the target of a symlink and the SHA-256 (or why there is
// none). Every string is already sanitized. Nothing is ever called clean: a
// short list is not a verdict, and a scan that did not see everything never
// says "nothing flagged", only what it did not check.
func printFlags(w io.Writer, rep handback.Report) {
	switch {
	case len(rep.Flags) == 0 && rep.Truncated == "":
		fmt.Fprintln(w, "Nothing flagged. This is not a malware scan.")
	case len(rep.Flags) > 0:
		fmt.Fprintf(w, "Flagged for your review (%d):\n", len(rep.Flags))
		for _, f := range rep.Flags {
			line := "  " + f.Path + "  " + f.Detail
			if f.Target != "" {
				line += "  -> " + f.Target
			}
			if f.SHA256 != "" {
				line += "  sha256 " + f.SHA256
			} else if f.HashNote != "" {
				line += "  " + f.HashNote
			}
			fmt.Fprintln(w, line)
		}
		fmt.Fprintln(w, "This is not a malware scan.")
	}
	if rep.Truncated != "" {
		fmt.Fprintf(w, "Not everything was checked: %s\n", rep.Truncated)
	}
}
