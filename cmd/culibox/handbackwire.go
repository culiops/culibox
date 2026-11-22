package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/culiops/culibox/internal/handback"
	"github.com/culiops/culibox/internal/session"
)

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
	inspect func(ctx context.Context, root string, since time.Time, lim handback.Limits) (handback.Report, error)
	now     func() time.Time
}

func newHandbackHooks(dataDir string, owned func(string) bool) *handbackHooks {
	return &handbackHooks{
		store:   handback.Store{Dir: handbackDir(dataDir)},
		owned:   owned,
		inspect: handback.InspectDir,
		now:     time.Now,
	}
}

// unknownStartNote is the report's own sentence when a scan has no usable
// start: the session's start time was lost (a restart adopted it) and no
// earlier scan of the workspace is on record.
func unknownStartNote(since time.Time) string {
	return fmt.Sprintf("start time unknown after a restart; files written before %s were not checked", since.UTC().Format(time.RFC3339))
}

// ScanInPlace inspects what a box wrote into workspace (symlinks resolved)
// since `since` and records it. With sinceKnown false the start is a guess: the
// last scan on record replaces it, and with none the record says files before
// the guess were not checked. The scan's start is taken before the walk, so a
// file written during it is seen by the next scan. A scan that stopped early
// (a cap) moves the window back to its own start instead, so the next scan
// still covers what this one did not reach. A scan with nothing flagged still
// writes a record: the next scan (a box stop) starts from it.
func (h *handbackHooks) ScanInPlace(ctx context.Context, workspace string, since time.Time, sinceKnown bool) (session.HandbackNote, error) {
	key := handback.InPlaceKey(workspace)
	scanAt := h.now()
	note := ""
	if !sinceKnown {
		if last, ok, err := h.store.LastScan(key); err != nil {
			// A record that cannot be read does not stop the scan: Merge decides
			// whether it can be replaced, and says so if it cannot.
			log.Printf("handback: last scan of %q unreadable (%v); using the session's start", workspace, err)
			note = unknownStartNote(since)
		} else if ok {
			since = last
		} else {
			note = unknownStartNote(since)
		}
	}
	rep, err := h.inspect(ctx, workspace, since, handback.DefaultLimits())
	if err != nil {
		return session.HandbackNote{}, err
	}
	if rep.Truncated != "" {
		scanAt = since // R8: what this scan did not reach must stay inside the next window
	}
	if note != "" {
		if rep.Truncated != "" {
			rep.Truncated += "; " + note
		} else {
			rep.Truncated = note
		}
	}
	rec, err := h.store.Merge(key, handback.Record{Kind: handback.KindInPlace, Workspace: workspace}, scanAt, rep)
	if err != nil {
		return session.HandbackNote{}, fmt.Errorf("cannot record what was flagged in %s: %w", workspace, err)
	}
	return session.HandbackNote{ID: rec.ID, Count: len(rec.Flags), InPlace: true}, nil
}

// OnStop is the box layer's stop hook: a box over an in-place workspace was
// stopped, and files its sessions wrote since the last scan are on the host.
// It scans only a workspace that has a record (a session of ours ended there
// and scanned it) and is not a clone: a clone's flags come from its branches.
// Failures are logged; the stop has already happened.
func (h *handbackHooks) OnStop(name, workspace string) {
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
	if _, err := h.ScanInPlace(ctx, workspace, last, true); err != nil {
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
	rec, err := h.store.Merge(cloneBranchKey(of, branch), base, h.now(), rep)
	if err != nil {
		return session.HandbackNote{}, err
	}
	return session.HandbackNote{ID: rec.ID, Count: len(rec.Flags), Branch: hostRef}, nil
}

// flaggedOf records every branch of a return that came home and lists the ones
// with flags. A record that cannot be written is logged and said in warnings
// (never dropped silently: the flags would be lost with it). Branches are
// visited in sorted order so the summary is stable.
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
		if note.Count > 0 {
			flagged = append(flagged, note)
		}
	}
	return flagged, warnings
}

// printFlags writes the flagged files of one fetched branch after the review
// line: path, detail and SHA-256 (or why there is none). Every string is
// already sanitized. Nothing is ever called clean: a short list is not a
// verdict.
func printFlags(w io.Writer, rep handback.Report) {
	if len(rep.Flags) == 0 {
		fmt.Fprintln(w, "Nothing flagged. This is not a malware scan.")
	} else {
		fmt.Fprintf(w, "Flagged for your review (%d):\n", len(rep.Flags))
		for _, f := range rep.Flags {
			hash := f.HashNote
			if f.SHA256 != "" {
				hash = "sha256 " + f.SHA256
			}
			if hash == "" {
				fmt.Fprintf(w, "  %s  %s\n", f.Path, f.Detail)
				continue
			}
			fmt.Fprintf(w, "  %s  %s  %s\n", f.Path, f.Detail, hash)
		}
		fmt.Fprintln(w, "This is not a malware scan.")
	}
	if rep.Truncated != "" {
		fmt.Fprintf(w, "Not everything was checked: %s\n", rep.Truncated)
	}
}
