package analytics

import (
	"encoding/json"
	"strings"
	"testing"
)

// putSession inserts one rollup row (one model, one day) and, when dim is not
// nil, its session_dim row.
func putSession(t *testing.T, s *Store, id, project, model string, cost float64, dim *DimRowFixture) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO session_rollup(day,session_id,project,model,input_tokens,cache_read,cache_creation,prompts,cost_usd)
VALUES('2026-08-05',?,?,?,100,300,100,1,?)`, id, project, model, cost); err != nil {
		t.Fatal(err)
	}
	if dim == nil {
		return
	}
	if _, err := s.db.Exec(`INSERT INTO session_dim(session_id,project,runtime,workspace,unmatched,profile,via,via_path,parsed)
VALUES(?,?,?,?,?,?,?,?,?)`, id, project, dim.Runtime, dim.Workspace, dim.Unmatched, dim.Profile, dim.Via, dim.ViaPath, dim.Parsed); err != nil {
		t.Fatal(err)
	}
}

// DimRowFixture is the session_dim columns a test sets.
type DimRowFixture struct {
	Runtime, Workspace, Profile, Via, ViaPath string
	Unmatched, Parsed                         int
}

func TestWorkspacesFoldsCloneHostWorktree(t *testing.T) {
	s := openTemp(t)
	putSession(t, s, "H", "p-host", "m", 1, &DimRowFixture{Runtime: "host", Workspace: "/r", Via: "host"})
	putSession(t, s, "C", "p-clone", "m", 2, &DimRowFixture{Runtime: "box", Workspace: "/r", Profile: "prd", Via: "clone", ViaPath: "/boxes/prd/r"})
	putSession(t, s, "W", "p-wt", "m", 4, &DimRowFixture{Runtime: "host", Workspace: "/r", Via: "worktree", ViaPath: "/r/.claude/worktrees/a"})

	rows, err := s.Workspaces("2026-08-01", "2026-08-31", Where{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1: %+v", len(rows), rows)
	}
	r := rows[0]
	if r.Workspace != "/r" || r.Name != "r" || r.Unmatched || r.Sessions != 3 || !approx(r.Cost, 7) {
		t.Errorf("row = %+v", r)
	}
	if !approx(r.HostCost+r.BoxCost, r.Cost) || !approx(r.BoxCost, 2) {
		t.Errorf("host %v + box %v != cost %v", r.HostCost, r.BoxCost, r.Cost)
	}
	if len(r.SessionCosts) != 3 {
		t.Errorf("sessionCosts = %v, want 3", r.SessionCosts)
	}
	if r.CacheRead != 900 || r.CacheDenom != 1500 {
		t.Errorf("cache = %d / %d, want 900 / 1500", r.CacheRead, r.CacheDenom)
	}
	if !approx(r.ByModel["m"], 7) {
		t.Errorf("byModel = %v", r.ByModel)
	}
	type want struct {
		label, path string
		cost        float64
	}
	wants := []want{
		{"worktrees (1)", "/r/.claude/worktrees/*", 4},
		{"box · prd", "/boxes/prd/r", 2},
		{"host", "/r", 1},
	}
	if len(r.Children) != 3 {
		t.Fatalf("children = %+v, want 3", r.Children)
	}
	for i, w := range wants {
		c := r.Children[i]
		if c.Label != w.label || c.Path != w.path || !approx(c.Cost, w.cost) || c.Sessions != 1 || len(c.SessionCosts) != 1 {
			t.Errorf("child %d = %+v, want %+v", i, c, w)
		}
	}
}

func TestWorkspacesChildLabels(t *testing.T) {
	s := openTemp(t)
	putSession(t, s, "A", "p", "m", 5, &DimRowFixture{Runtime: "box", Workspace: "/r", Via: "inplace"})
	putSession(t, s, "B", "p", "m", 4, &DimRowFixture{Runtime: "", Workspace: "/r"})
	putSession(t, s, "C", "p", "m", 3, &DimRowFixture{Runtime: "host", Workspace: "/r", Via: "worktree", ViaPath: "/r/.claude/worktrees/a"})
	putSession(t, s, "D", "p", "m", 2, &DimRowFixture{Runtime: "host", Workspace: "/r", Via: "worktree", ViaPath: "/r/.claude/worktrees/b"})
	putSession(t, s, "E", "p", "m", 1, &DimRowFixture{Runtime: "host", Workspace: "/r", Via: "worktree", ViaPath: "/r/.claude/worktrees/b"})
	rows, err := s.Workspaces("2026-08-01", "2026-08-31", Where{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range rows[0].Children {
		got = append(got, c.Label+"|"+c.Path)
	}
	want := []string{"worktrees (2)|/r/.claude/worktrees/*", "box · unnamed|/r", "unknown|/r"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("children = %v, want %v", got, want)
	}
	if rows[0].Children[0].Sessions != 3 {
		t.Errorf("worktree sessions = %d, want 3", rows[0].Children[0].Sessions)
	}
}

// One host child however the host sessions were reached (live or legacy folder),
// and one child per box profile whether it cloned or worked in place; the box
// child's path is the clone dir.
func TestWorkspacesOneChildPerRuntimeProfile(t *testing.T) {
	s := openTemp(t)
	putSession(t, s, "H1", "p", "m", 1, &DimRowFixture{Runtime: "host", Workspace: "/r", Via: "host"})
	putSession(t, s, "H2", "p", "m", 2, &DimRowFixture{Runtime: "host", Workspace: "/r", Via: "legacy"})
	putSession(t, s, "C", "p", "m", 4, &DimRowFixture{Runtime: "box", Workspace: "/r", Profile: "prd", Via: "clone", ViaPath: "/boxes/prd/r"})
	putSession(t, s, "I", "p", "m", 8, &DimRowFixture{Runtime: "box", Workspace: "/r", Profile: "prd", Via: "inplace"})
	rows, err := s.Workspaces("2026-08-01", "2026-08-31", Where{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0].Children) != 2 {
		t.Fatalf("want one row with 2 children: %+v", rows)
	}
	box, host := rows[0].Children[0], rows[0].Children[1]
	if box.Label != "box · prd" || box.Path != "/boxes/prd/r" || box.Sessions != 2 || !approx(box.Cost, 12) {
		t.Errorf("box child = %+v", box)
	}
	if host.Label != "host" || host.Path != "/r" || host.Sessions != 2 || !approx(host.Cost, 3) {
		t.Errorf("host child = %+v", host)
	}
	// A box child with no clone row at all (in place only) points at the workspace.
	s2 := openTemp(t)
	putSession(t, s2, "I", "p", "m", 8, &DimRowFixture{Runtime: "box", Workspace: "/r", Profile: "prd", Via: "inplace"})
	rows, _ = s2.Workspaces("2026-08-01", "2026-08-31", Where{})
	if c := rows[0].Children; len(c) != 1 || c[0].Path != "/r" || c[0].Via != "inplace" {
		t.Errorf("in-place child = %+v", c)
	}
}

// Every worktree session of a workspace is one child, whatever its runtime.
func TestWorkspacesOneWorktreesChild(t *testing.T) {
	s := openTemp(t)
	putSession(t, s, "HW", "p", "m", 1, &DimRowFixture{Runtime: "host", Workspace: "/r", Via: "worktree", ViaPath: "/r/.claude/worktrees/a"})
	putSession(t, s, "BW", "p", "m", 3, &DimRowFixture{Runtime: "box", Workspace: "/r", Profile: "prd", Via: "worktree", ViaPath: "/r/.claude/worktrees/b"})
	rows, err := s.Workspaces("2026-08-01", "2026-08-31", Where{})
	if err != nil {
		t.Fatal(err)
	}
	r := rows[0]
	if len(r.Children) != 1 {
		t.Fatalf("children = %+v, want one worktrees child", r.Children)
	}
	c := r.Children[0]
	if c.Label != "worktrees (2)" || c.Via != "worktree" || c.Path != "/r/.claude/worktrees/*" ||
		c.Sessions != 2 || !approx(c.Cost, 4) || c.Runtime != "box" || c.Profile != "prd" {
		t.Errorf("child = %+v", c)
	}
	// Row-level host and box cost still follow each session's own runtime.
	if !approx(r.HostCost, 1) || !approx(r.BoxCost, 3) {
		t.Errorf("host %v box %v, want 1 and 3", r.HostCost, r.BoxCost)
	}
}

func TestWorkspacesUnmatched(t *testing.T) {
	s := openTemp(t)
	slug := "-home-u-box-clones-old-proj"
	// A legacy session: a dim row that no workspace explained.
	putSession(t, s, "L", slug, "m", 3, &DimRowFixture{Workspace: slug, Unmatched: 1})
	// A session with no dim row at all.
	putSession(t, s, "N", "-home-u-other", "m", 2, nil)
	putSession(t, s, "M", "p", "m", 1, &DimRowFixture{Runtime: "host", Workspace: "/r", Via: "host"})

	rows, err := s.Workspaces("2026-08-01", "2026-08-31", Where{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %+v, want 3", rows)
	}
	byWS := map[string]WorkspaceRow{}
	var sum float64
	for _, r := range rows {
		byWS[r.Workspace] = r
		sum += r.Cost
	}
	if !approx(sum, 6) {
		t.Errorf("rows sum to %v, want the window total 6", sum)
	}
	if r := byWS[slug]; !r.Unmatched || r.Name != "box-clones-old-proj" || len(r.Children) != 1 || r.Children[0].Label != "unknown" {
		t.Errorf("legacy row = %+v", r)
	}
	if r := byWS["-home-u-other"]; !r.Unmatched || r.Name != "other" {
		t.Errorf("no-dim row = %+v", r)
	}
	if r := byWS["/r"]; r.Unmatched {
		t.Errorf("matched row flagged unmatched: %+v", r)
	}
}

func TestWorkspacesWhere(t *testing.T) {
	s := openTemp(t)
	putSession(t, s, "H", "p", "m", 1, &DimRowFixture{Runtime: "host", Workspace: "/r", Via: "host"})
	putSession(t, s, "C", "p", "m", 2, &DimRowFixture{Runtime: "box", Workspace: "/r", Profile: "prd", Via: "clone"})
	rows, err := s.Workspaces("2026-08-01", "2026-08-31", Where{Kind: "boxp", Profile: "prd"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !approx(rows[0].Cost, 2) || len(rows[0].Children) != 1 {
		t.Errorf("rows = %+v", rows)
	}
}

func TestWorkspacesJSON(t *testing.T) {
	s := openTemp(t)
	rows, err := s.Workspaces("2026-08-01", "2026-08-31", Where{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(rows)
	if string(b) != "[]" {
		t.Errorf("empty workspaces = %s, want []", b)
	}
	putSession(t, s, "H", "p", "m", 1, &DimRowFixture{Runtime: "host", Workspace: "/r", Via: "host"})
	rows, _ = s.Workspaces("2026-08-01", "2026-08-31", Where{})
	b, _ = json.Marshal(rows[0])
	for _, k := range []string{`"costUsd":1`, `"hostCost":1`, `"boxCost":0`, `"byModel":{"m":1}`, `"cacheDenom":500`,
		`"sessionCosts":[1]`, `"children":[{`, `"unmatched":false`, `"label":"host"`, `"path":"/r"`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("json lacks %s: %s", k, b)
		}
	}
	// Drivers: empty lists are [], keys are lowerCamel.
	d, err := s.Drivers("2026-08-01", "2026-08-31", Where{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(d)
	want := `{"total":1,"coveredTotal":0,"agent":[],"skill":[],"plugin":[],"mcp":[]}`
	if string(b) != want {
		t.Errorf("drivers json = %s, want %s", b, want)
	}
}

func TestWhereOptionsSumToAll(t *testing.T) {
	s := openTemp(t)
	putSession(t, s, "H", "p", "m", 1, &DimRowFixture{Runtime: "host", Workspace: "/r", Via: "host"})
	putSession(t, s, "B1", "p", "m", 2, &DimRowFixture{Runtime: "box", Workspace: "/r", Profile: "prd", Via: "clone"})
	putSession(t, s, "B2", "p", "m", 4, &DimRowFixture{Runtime: "box", Workspace: "/r", Profile: "dev", Via: "clone"})
	putSession(t, s, "B3", "p", "m", 8, &DimRowFixture{Runtime: "box", Workspace: "/r", Via: "inplace"})
	putSession(t, s, "U", "p", "m", 16, nil)

	opts, err := s.WhereOptions("2026-08-01", "2026-08-31")
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	cost := map[string]float64{}
	for _, o := range opts {
		keys = append(keys, o.Key)
		cost[o.Key] = o.Cost
	}
	if got, want := strings.Join(keys, ","), ",host,box,box:dev,box:prd,box:,unknown"; got != want {
		t.Fatalf("keys = %q, want %q", got, want)
	}
	if opts[0].Label != "All" || opts[2].Label != "Box · all profiles" || opts[3].Label != "Box · dev" ||
		opts[5].Label != "Box · unnamed" || opts[6].Label != "Unknown" {
		t.Errorf("labels = %+v", opts)
	}
	if !approx(cost[""], 31) || !approx(cost["host"]+cost["box"]+cost["unknown"], cost[""]) {
		t.Errorf("host+box+unknown != all: %v", cost)
	}
	if !approx(cost["box:dev"]+cost["box:prd"]+cost["box:"], cost["box"]) {
		t.Errorf("profiles != box: %v", cost)
	}
	// Each option's cost matches the filtered query it selects.
	for _, k := range keys {
		var n float64
		rows, err := s.ByModel("2026-08-01", "2026-08-31", ParseWhere(k))
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			n += r.Cost
		}
		if !approx(n, cost[k]) {
			t.Errorf("option %q cost %v, filtered query %v", k, cost[k], n)
		}
	}

	// A fleet with no unknown sessions and no unnamed profile has neither entry.
	s2 := openTemp(t)
	putSession(t, s2, "H", "p", "m", 1, &DimRowFixture{Runtime: "host", Workspace: "/r", Via: "host"})
	opts, _ = s2.WhereOptions("2026-08-01", "2026-08-31")
	if len(opts) != 3 {
		t.Errorf("options = %+v, want All, Host, Box", opts)
	}
}

func TestSessionModelCosts(t *testing.T) {
	s := openTemp(t)
	putSession(t, s, "S", "p", "claude-opus-5", 5, &DimRowFixture{Runtime: "host", Workspace: "/r", Via: "host"})
	if _, err := s.db.Exec(`INSERT INTO session_rollup(day,session_id,project,model,cost_usd) VALUES('2026-08-06','S','p','claude-haiku-4-5',1)`); err != nil {
		t.Fatal(err)
	}
	rows, err := s.SessionModelCosts("2026-08-01", "2026-08-31", Where{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Model != "claude-opus-5" || !approx(rows[0].Cost, 5) ||
		rows[1].Model != "claude-haiku-4-5" || !approx(rows[1].Cost, 1) {
		t.Fatalf("rows = %+v", rows)
	}
	sc, err := s.SessionCosts("2026-08-01", "2026-08-31", Where{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sc) != 1 || sc[0].Model != "mixed" || !approx(sc[0].Cost, 6) {
		t.Errorf("SessionCosts = %+v, want one mixed $6", sc)
	}
	if sc[0].Workspace != "/r" || sc[0].Runtime != "host" {
		t.Errorf("SessionCosts lacks dim fields: %+v", sc[0])
	}
	b, _ := json.Marshal(rows[0])
	if string(b) != `{"sessionId":"S","project":"p","model":"claude-opus-5","costUsd":5}` {
		t.Errorf("json = %s", b)
	}
	none, _ := openTemp(t).SessionModelCosts("2026-08-01", "2026-08-31", Where{})
	if b, _ := json.Marshal(none); string(b) != "[]" {
		t.Errorf("empty = %s, want []", b)
	}
}

func TestSessionCostsWorkspaceForNoDim(t *testing.T) {
	s := openTemp(t)
	putSession(t, s, "N", "-home-u-other", "m", 2, nil)
	putSession(t, s, "L", "-home-u-old", "m", 1, &DimRowFixture{Workspace: "-home-u-old", Unmatched: 1})
	sc, err := s.SessionCosts("2026-08-01", "2026-08-31", Where{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sc) != 2 || sc[0].Workspace != "-home-u-other" || sc[0].Runtime != "" || sc[1].Workspace != "-home-u-old" {
		t.Errorf("rows = %+v", sc)
	}
}
