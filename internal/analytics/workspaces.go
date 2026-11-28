package analytics

import (
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// WorkspaceChild is one way a workspace was worked in: on the host, in one box
// profile's clone or in-place box, or in worktrees under the workspace.
type WorkspaceChild struct {
	Runtime      string    `json:"runtime"`
	Profile      string    `json:"profile"`
	Via          string    `json:"via"`
	Label        string    `json:"label"`
	Path         string    `json:"path"`
	Cost         float64   `json:"costUsd"`
	Sessions     int64     `json:"sessions"`
	CacheRead    int64     `json:"cacheRead"`
	CacheDenom   int64     `json:"cacheDenom"`
	SessionCosts []float64 `json:"sessionCosts"`
}

// WorkspaceRow is one repo (or one unmatched folder slug) with everything spent
// in it over the window. CacheDenom is input + cache_read + cache_creation.
// SessionCosts holds one whole-session cost per session; the page takes medians
// from it. HostCost and BoxCost count only sessions with that runtime, so a
// session of unknown runtime is in neither.
type WorkspaceRow struct {
	Workspace    string             `json:"workspace"`
	Name         string             `json:"name"`
	Unmatched    bool               `json:"unmatched"`
	Cost         float64            `json:"costUsd"`
	Sessions     int64              `json:"sessions"`
	HostCost     float64            `json:"hostCost"`
	BoxCost      float64            `json:"boxCost"`
	ByModel      map[string]float64 `json:"byModel"`
	CacheRead    int64              `json:"cacheRead"`
	CacheDenom   int64              `json:"cacheDenom"`
	SessionCosts []float64          `json:"sessionCosts"`
	Children     []WorkspaceChild   `json:"children"`
}

var projShortRe = regexp.MustCompile(`^-home-[^-]+-`)

// projShort mirrors the page's projShort: a project slug without its
// "-home-<user>-" prefix.
func projShort(p string) string {
	return strings.TrimPrefix(projShortRe.ReplaceAllString(p, ""), "-")
}

// workspaceName is the last path element of a workspace, or the workspace
// itself when it has none (a root path).
func workspaceName(ws string) string {
	if b := path.Base(ws); b != "/" && b != "." && b != "" {
		return b
	}
	return ws
}

// workspaceOf is the workspace a session is filed under and whether it is
// unmatched: a session with no workspace in its dim row (or no dim row) is its
// own unmatched workspace keyed by its project slug.
func workspaceOf(project, workspace string, unmatched bool) (string, bool) {
	if workspace == "" {
		return project, true
	}
	return workspace, unmatched
}

// sessAcc is one session inside a child: its cost and the dim fields that
// choose the child's own runtime, profile and via (those of its costliest one).
type sessAcc struct {
	cost                  float64
	runtime, profile, via string
}

// childKey is what makes a child: all worktree sessions of a workspace are one
// child, and every other session is filed by (runtime, profile).
type childKey struct {
	worktree         bool
	runtime, profile string
}

type childAcc struct {
	c    WorkspaceChild
	sess map[string]*sessAcc
	// paths: for the worktrees child every via_path seen (the count is its N);
	// for any other child the via_path of each clone row, with its cost.
	paths map[string]float64
}

type wsAcc struct {
	row      WorkspaceRow
	sess     map[string]float64
	children map[childKey]*childAcc
}

// Workspaces folds the window's rollup rows into one row per workspace, each
// with its children. A session with no session_dim row, or none that names a
// workspace, is its own unmatched row keyed by its project slug, so the rows sum
// to the window's total. Rows are folded in Go; their count is bounded by
// sessions x models, as in SessionCosts.
func (s *Store) Workspaces(from, to string, w Where) ([]WorkspaceRow, error) {
	src, and, wargs := w.joined("session_rollup")
	rows, err := s.db.Query(`
SELECT r.session_id, r.project, r.model,
  SUM(r.input_tokens), SUM(r.cache_read), SUM(r.cache_creation), SUM(r.cost_usd),
  COALESCE(d.runtime,''), COALESCE(d.profile,''), COALESCE(d.via,''), COALESCE(d.via_path,''),
  COALESCE(d.workspace,''), COALESCE(d.unmatched,0)
FROM `+src+` WHERE r.day BETWEEN ? AND ?`+and+`
GROUP BY r.session_id, r.project, r.model, d.runtime, d.profile, d.via, d.via_path, d.workspace, d.unmatched`,
		append([]any{from, to}, wargs...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byWS := map[string]*wsAcc{}
	for rows.Next() {
		var sid, project, model, runtime, profile, via, viaPath, ws string
		var in, cr, cc, unmatched int64
		var cost float64
		if err := rows.Scan(&sid, &project, &model, &in, &cr, &cc, &cost,
			&runtime, &profile, &via, &viaPath, &ws, &unmatched); err != nil {
			return nil, err
		}
		ws, um := workspaceOf(project, ws, unmatched != 0)
		a := byWS[ws]
		if a == nil {
			name := workspaceName(ws)
			if um {
				name = projShort(ws)
			}
			a = &wsAcc{
				row: WorkspaceRow{Workspace: ws, Name: name, Unmatched: um,
					ByModel: map[string]float64{}, SessionCosts: []float64{}, Children: []WorkspaceChild{}},
				sess:     map[string]float64{},
				children: map[childKey]*childAcc{},
			}
			byWS[ws] = a
		}
		sk := sid + "\x1f" + project
		denom := in + cr + cc
		a.row.Cost += cost
		a.row.ByModel[model] += cost
		a.row.CacheRead += cr
		a.row.CacheDenom += denom
		a.sess[sk] += cost
		switch runtime {
		case "host":
			a.row.HostCost += cost
		case "box":
			a.row.BoxCost += cost
		}

		// A child is (runtime, profile), except that every worktree session of
		// the workspace is one child whatever its runtime and profile.
		ck := childKey{runtime: runtime, profile: profile}
		if via == "worktree" {
			ck = childKey{worktree: true}
		}
		c := a.children[ck]
		if c == nil {
			c = &childAcc{
				c:     WorkspaceChild{SessionCosts: []float64{}},
				sess:  map[string]*sessAcc{},
				paths: map[string]float64{},
			}
			a.children[ck] = c
		}
		c.c.Cost += cost
		c.c.CacheRead += cr
		c.c.CacheDenom += denom
		si := c.sess[sk]
		if si == nil {
			si = &sessAcc{runtime: runtime, profile: profile, via: via}
			c.sess[sk] = si
		}
		si.cost += cost
		if ck.worktree || via == "clone" {
			c.paths[viaPath] += cost
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]WorkspaceRow, 0, len(byWS))
	for _, a := range byWS {
		a.row.Sessions = int64(len(a.sess))
		for _, v := range a.sess {
			a.row.SessionCosts = append(a.row.SessionCosts, v)
		}
		sort.Float64s(a.row.SessionCosts)
		for k, c := range a.children {
			finishChild(c, a.row.Workspace, k.worktree)
			a.row.Children = append(a.row.Children, c.c)
		}
		sort.Slice(a.row.Children, func(i, j int) bool {
			x, y := a.row.Children[i], a.row.Children[j]
			if x.Cost != y.Cost {
				return x.Cost > y.Cost
			}
			return x.Label < y.Label
		})
		out = append(out, a.row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Cost != out[j].Cost {
			return out[i].Cost > out[j].Cost
		}
		return out[i].Workspace < out[j].Workspace
	})
	return out, nil
}

// finishChild sets a child's runtime, profile, via, label, path, session count
// and session costs once all its rows are in. Runtime, profile and via are those
// of its costliest session (all one value for a child of a single kind). The path
// of a box child is the clone dir of its costliest clone row, or the workspace
// when it has none; host and unknown children are the workspace, and worktrees
// the folder they live under.
func finishChild(c *childAcc, workspace string, worktree bool) {
	c.c.Sessions = int64(len(c.sess))
	bestKey := ""
	var best *sessAcc
	for k, v := range c.sess {
		c.c.SessionCosts = append(c.c.SessionCosts, v.cost)
		if best == nil || v.cost > best.cost || (v.cost == best.cost && k < bestKey) {
			bestKey, best = k, v
		}
	}
	sort.Float64s(c.c.SessionCosts)
	c.c.Runtime, c.c.Profile, c.c.Via = best.runtime, best.profile, best.via
	c.c.Path = workspace
	switch {
	case worktree:
		c.c.Via = "worktree"
		c.c.Label = "worktrees (" + strconv.Itoa(len(c.paths)) + ")"
		c.c.Path = workspace + "/.claude/worktrees/*"
	case c.c.Runtime == "host":
		c.c.Label = "host"
	case c.c.Runtime == "box":
		p := c.c.Profile
		if p == "" {
			p = "unnamed"
		}
		c.c.Label = "box · " + p
		bestPath, bestCost := "", -1.0
		for p, v := range c.paths {
			if p != "" && (v > bestCost || (v == bestCost && p < bestPath)) {
				bestPath, bestCost = p, v
			}
		}
		if bestPath != "" {
			c.c.Path = bestPath
		}
	default:
		c.c.Label = "unknown"
	}
}

// WhereOption is one entry of the page's where select.
type WhereOption struct {
	Key   string  `json:"key"`
	Label string  `json:"label"`
	Cost  float64 `json:"costUsd"`
}

// WhereOptions lists the where select's entries with their cost over the
// window. All come from the same grouped sums, so host + box + unknown equal
// All and the profiles equal box. Order: All, Host, Box (all profiles), each
// named profile by cost, the unnamed profile, Unknown.
func (s *Store) WhereOptions(from, to string) ([]WhereOption, error) {
	rows, err := s.db.Query(`
SELECT COALESCE(d.runtime,''), COALESCE(d.profile,''), SUM(r.cost_usd)
FROM session_rollup`+dimJoin+`
WHERE r.day BETWEEN ? AND ?
GROUP BY d.runtime, d.profile`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all, host, box, unknown, unnamed float64
	var hasUnknown, hasUnnamed bool
	named := []WhereOption{}
	for rows.Next() {
		var rt, prof string
		var c float64
		if err := rows.Scan(&rt, &prof, &c); err != nil {
			return nil, err
		}
		all += c
		switch rt {
		case "host":
			host += c
		case "box":
			box += c
			if prof == "" {
				unnamed, hasUnnamed = unnamed+c, true
			} else {
				named = append(named, WhereOption{Key: "box:" + prof, Label: "Box · " + prof, Cost: c})
			}
		case "":
			unknown, hasUnknown = unknown+c, true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(named, func(i, j int) bool {
		if named[i].Cost != named[j].Cost {
			return named[i].Cost > named[j].Cost
		}
		return named[i].Key < named[j].Key
	})
	out := []WhereOption{{"", "All", all}, {"host", "Host", host}, {"box", "Box · all profiles", box}}
	out = append(out, named...)
	if hasUnnamed {
		out = append(out, WhereOption{"box:", "Box · unnamed", unnamed})
	}
	if hasUnknown {
		out = append(out, WhereOption{"unknown", "Unknown", unknown})
	}
	return out, nil
}
