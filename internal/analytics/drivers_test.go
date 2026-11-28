package analytics

import (
	"fmt"
	"reflect"
	"testing"
)

func putDriver(t *testing.T, s *Store, id, kind, name, parent string, cost float64) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO driver_rollup(day,session_id,project,kind,name,parent,cost_usd) VALUES('2026-08-05',?,'p',?,?,?,?)`,
		id, kind, name, parent, cost); err != nil {
		t.Fatal(err)
	}
}

func TestDriversCoveredTotal(t *testing.T) {
	s := openTemp(t)
	putSession(t, s, "P", "p", "m", 10, &DimRowFixture{Runtime: "host", Workspace: "/r", Via: "host", Parsed: 1})
	putSession(t, s, "L", "p", "m", 90, &DimRowFixture{Runtime: "host", Workspace: "/r", Via: "host"})
	putDriver(t, s, "P", "agent", "general-purpose", "", 7)

	d, err := s.Drivers("2026-08-01", "2026-08-31", Where{})
	if err != nil {
		t.Fatal(err)
	}
	if !approx(d.Total, 100) || !approx(d.CoveredTotal, 10) {
		t.Errorf("total %v covered %v, want 100 and 10", d.Total, d.CoveredTotal)
	}
	if !reflect.DeepEqual(d.Agent, []NamedCost{{"general-purpose", 7}}) {
		t.Errorf("agent = %+v", d.Agent)
	}
	if d.Skill == nil || d.Plugin == nil || d.MCP == nil || len(d.Skill)+len(d.Plugin)+len(d.MCP) != 0 {
		t.Errorf("empty lists must be non-nil: %+v", d)
	}

	// The where filter narrows total, covered and lists the same way.
	putSession(t, s, "B", "p", "m", 5, &DimRowFixture{Runtime: "box", Workspace: "/r", Via: "inplace", Parsed: 1})
	putDriver(t, s, "B", "agent", "Explore", "", 2)
	d, err = s.Drivers("2026-08-01", "2026-08-31", Where{Kind: "box"})
	if err != nil {
		t.Fatal(err)
	}
	if !approx(d.Total, 5) || !approx(d.CoveredTotal, 5) || !reflect.DeepEqual(d.Agent, []NamedCost{{"Explore", 2}}) {
		t.Errorf("box drivers = %+v", d)
	}
	// Outside the window nothing counts.
	d, _ = s.Drivers("2026-09-01", "2026-09-30", Where{})
	if d.Total != 0 || d.CoveredTotal != 0 || len(d.Agent) != 0 {
		t.Errorf("empty window = %+v", d)
	}
}

func TestDriversMCPNesting(t *testing.T) {
	s := openTemp(t)
	putSession(t, s, "P", "p", "m", 50, &DimRowFixture{Runtime: "host", Workspace: "/r", Via: "host", Parsed: 1})
	putDriver(t, s, "P", "mcp_server", "playwright", "", 3)
	putDriver(t, s, "P", "mcp_tool", "browser_click", "playwright", 1)
	putDriver(t, s, "P", "mcp_tool", "browser_navigate", "playwright", 2.5)
	putDriver(t, s, "P", "mcp_server", "claude-design", "", 6)
	putDriver(t, s, "P", "mcp_tool", "get_design", "claude-design", 4)
	// A tool whose server has no row of its own still nests, at cost 0.
	putDriver(t, s, "P", "mcp_tool", "query", "orphan", 1)
	putDriver(t, s, "P", "skill", "brainstorming", "", 2)
	putDriver(t, s, "P", "skill", "debugging", "", 9)
	putDriver(t, s, "P", "plugin", "superpowers", "", 3)

	d, err := s.Drivers("2026-08-01", "2026-08-31", Where{})
	if err != nil {
		t.Fatal(err)
	}
	want := []MCPServerCost{
		{"claude-design", 6, []NamedCost{{"get_design", 4}}},
		{"playwright", 3, []NamedCost{{"browser_navigate", 2.5}, {"browser_click", 1}}},
		{"orphan", 0, []NamedCost{{"query", 1}}},
	}
	if !reflect.DeepEqual(d.MCP, want) {
		t.Errorf("mcp = %+v, want %+v", d.MCP, want)
	}
	if !reflect.DeepEqual(d.Skill, []NamedCost{{"debugging", 9}, {"brainstorming", 2}}) ||
		!reflect.DeepEqual(d.Plugin, []NamedCost{{"superpowers", 3}}) {
		t.Errorf("skill/plugin = %+v / %+v", d.Skill, d.Plugin)
	}
}

// A window with more names than a card can show returns the costliest ones and
// one last "other (N more)" row holding the rest, so a kind's rows still add up
// to its cost (the page's muted row is CoveredTotal minus that sum) while the
// response stays bounded however many names a transcript invents.
func TestDriversCapFoldsTheRest(t *testing.T) {
	s := openTemp(t)
	putSession(t, s, "P", "p", "m", 1e6, &DimRowFixture{Runtime: "host", Workspace: "/r", Via: "host", Parsed: 1})
	n := driversPerKind + 10
	var skillSum float64
	for i := 1; i <= n; i++ {
		putDriver(t, s, "P", "skill", fmt.Sprintf("s%03d", i), "", float64(i))
		skillSum += float64(i)
	}
	// Servers: driversPerKind+5 with their own rows, each with one tool; the
	// costliest also has driversToolsPerServer+3 tools.
	for i := 1; i <= driversPerKind+5; i++ {
		srv := fmt.Sprintf("m%03d", i)
		putDriver(t, s, "P", "mcp_server", srv, "", float64(1000+i))
		putDriver(t, s, "P", "mcp_tool", "t", srv, 1)
	}
	top := fmt.Sprintf("m%03d", driversPerKind+5)
	for i := 1; i <= driversToolsPerServer+3; i++ {
		putDriver(t, s, "P", "mcp_tool", fmt.Sprintf("x%03d", i), top, float64(10+i))
	}

	d, err := s.Drivers("2026-08-01", "2026-08-31", Where{})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Skill) != driversPerKind+1 {
		t.Fatalf("skills = %d rows, want %d", len(d.Skill), driversPerKind+1)
	}
	if d.Skill[0] != (NamedCost{fmt.Sprintf("s%03d", n), float64(n)}) {
		t.Errorf("first skill = %+v", d.Skill[0])
	}
	if last := d.Skill[driversPerKind]; last != (NamedCost{"other (10 more)", 55}) {
		t.Errorf("fold row = %+v, want other (10 more) at 55", last)
	}
	var got float64
	for _, x := range d.Skill {
		got += x.Cost
	}
	if !approx(got, skillSum) {
		t.Errorf("skill rows add to %v, want %v", got, skillSum)
	}

	if len(d.MCP) != driversPerKind+1 {
		t.Fatalf("servers = %d rows, want %d", len(d.MCP), driversPerKind+1)
	}
	if d.MCP[0].Server != top || len(d.MCP[0].Tools) != driversToolsPerServer+1 {
		t.Fatalf("top server = %s with %d tools", d.MCP[0].Server, len(d.MCP[0].Tools))
	}
	if last := d.MCP[0].Tools[driversToolsPerServer]; last.Name != "other (4 more)" {
		t.Errorf("tool fold row = %+v, want other (4 more)", last)
	}
	fold := d.MCP[driversPerKind]
	if fold.Server != "other (5 more)" || !approx(fold.Cost, 1001+1002+1003+1004+1005) || len(fold.Tools) != 0 {
		t.Errorf("server fold row = %+v", fold)
	}
}
