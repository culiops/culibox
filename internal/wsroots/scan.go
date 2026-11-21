package wsroots

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Place is a project folder the scan found outside home and every root. Label
// is the folder's base name.
type Place struct {
	Path  string `json:"path"`
	Label string `json:"label"`
}

// ScanOpts bounds a scan. A zero field takes its default.
type ScanOpts struct {
	// Depth is how many folder levels below a base a project folder may be:
	// base/a/b/c is at depth 3.
	Depth int
	// Max is the most places returned.
	Max int
	// Budget is the total time a scan may take; it returns what it found by then.
	Budget time.Duration
	// Markers are the entry names that make a folder a project.
	Markers []string
}

// DefaultScanOpts are the limits of spec 6.8. The markers are the index's
// project markers.
func DefaultScanOpts() ScanOpts {
	return ScanOpts{
		Depth:   3,
		Max:     50,
		Budget:  time.Second,
		Markers: []string{".git", "go.mod", "package.json", "Cargo.toml", "pyproject.toml"},
	}
}

func (o ScanOpts) filled() ScanOpts {
	d := DefaultScanOpts()
	if o.Depth <= 0 {
		o.Depth = d.Depth
	}
	if o.Max <= 0 {
		o.Max = d.Max
	}
	if o.Budget <= 0 {
		o.Budget = d.Budget
	}
	if len(o.Markers) == 0 {
		o.Markers = d.Markers
	}
	return o
}

// Scan looks for project folders under bases and returns them sorted by path.
// It reads directory entries only (names and types, never file contents). It
// descends only into readable real directories: a symlinked directory is not
// followed, a folder Refusal rejects is skipped with everything under it, and
// a dot folder is not entered. It stops at a project folder, so nothing below
// one is reported. A marker counts when an entry of that name exists (.git may
// be a folder or a file). It returns what it has when ctx ends or o.Budget
// passes. A base is resolved once (a mount point may be a link) and the
// results are under its resolved path.
func Scan(ctx context.Context, bases []string, env Env, o ScanOpts) []Place {
	o = o.filled()
	ctx, cancel := context.WithTimeout(ctx, o.Budget)
	defer cancel()
	markers := make(map[string]bool, len(o.Markers))
	for _, m := range o.Markers {
		markers[m] = true
	}
	s := scanner{ctx: ctx, env: env, o: o, markers: markers}
	seen := map[string]bool{}
	for _, b := range bases {
		if !filepath.IsAbs(b) {
			continue
		}
		real, err := filepath.EvalSymlinks(b)
		if err != nil {
			continue
		}
		real = filepath.Clean(real)
		if seen[real] {
			continue
		}
		seen[real] = true
		s.walk(real, 0)
		if s.done() {
			break
		}
	}
	sort.Slice(s.found, func(i, j int) bool { return s.found[i].Path < s.found[j].Path })
	return s.found
}

type scanner struct {
	ctx     context.Context
	env     Env
	o       ScanOpts
	markers map[string]bool
	found   []Place
}

func (s *scanner) done() bool {
	return s.ctx.Err() != nil || len(s.found) >= s.o.Max
}

// walk reads dir (at depth below its base) and descends into its folders.
func (s *scanner) walk(dir string, depth int) {
	if s.done() || Refusal(dir, s.env) != "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // not readable: skipped
	}
	if depth > 0 {
		for _, e := range entries {
			if s.markers[e.Name()] {
				s.found = append(s.found, Place{Path: dir, Label: filepath.Base(dir)})
				return // a project: nothing below it is reported
			}
		}
	}
	if depth >= s.o.Depth {
		return
	}
	for _, e := range entries {
		// Type() comes from the directory entry itself, so a link to a folder
		// reads as a link and is not followed.
		if !e.Type().IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		s.walk(filepath.Join(dir, e.Name()), depth+1)
		if s.done() {
			return
		}
	}
}

// globFn expands a pattern (no match, or a bad pattern, is none); a test
// replaces it.
var globFn = func(pattern string) []string {
	m, _ := filepath.Glob(pattern)
	return m
}

// DefaultBases are the folders the scan starts from (spec 6.8 and 7), for goos
// "linux" or "darwin" (anything else: none). user names the folders mounted
// for one user; an empty or odd one skips those. A pattern that matches
// nothing adds nothing; a fixed folder that does not exist is left for Scan
// to skip.
func DefaultBases(goos, user string) []string {
	switch goos {
	case "linux":
		out := []string{"/srv", "/opt", "/data"}
		out = append(out, globFn("/mnt/*")...)
		if user != "" && !strings.ContainsAny(user, `/\*?[`) && user != "." && user != ".." {
			out = append(out, globFn(filepath.Join("/media", user, "*"))...)
			out = append(out, globFn(filepath.Join("/run/media", user, "*"))...)
		}
		return out
	case "darwin":
		var out []string
		for _, v := range globFn("/Volumes/*") {
			// The boot volume is listed there as a link to "/": scanning it is
			// scanning the whole disk.
			if real, err := filepath.EvalSymlinks(v); err != nil || filepath.Clean(real) == "/" {
				continue
			}
			out = append(out, v)
		}
		return append(out, "/opt", "/srv")
	}
	return nil
}

// currentUser is the login name of the running user: os/user, else $USER.
func currentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}
