package analytics

import (
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
