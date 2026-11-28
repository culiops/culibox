package analytics

import (
	"fmt"
	"sort"
)

// NamedCost is one agent, skill or plugin with its cost.
type NamedCost struct {
	Name string  `json:"name"`
	Cost float64 `json:"costUsd"`
	// Other marks the one fold row of a list: the names past its cap.
	Other bool `json:"other,omitempty"`
}

// MCPServerCost is one MCP server and the tools called under it. Cost is the
// server's own row, not the sum of its tools.
type MCPServerCost struct {
	Server string      `json:"server"`
	Cost   float64     `json:"costUsd"`
	Other  bool        `json:"other,omitempty"` // the fold row of the servers past the cap
	Tools  []NamedCost `json:"tools"`
}

// DriversResp is the cost-drivers view. Total is every session's cost in the
// window; CoveredTotal is the part from sessions whose transcripts carried
// driver data (parsed = 1). The lists are never summed into each other: one
// message's cost can sit under several kinds.
type DriversResp struct {
	Total        float64         `json:"total"`
	CoveredTotal float64         `json:"coveredTotal"`
	Agent        []NamedCost     `json:"agent"`
	Skill        []NamedCost     `json:"skill"`
	Plugin       []NamedCost     `json:"plugin"`
	MCP          []MCPServerCost `json:"mcp"`
}

func sortNamed(l []NamedCost) {
	sort.Slice(l, func(i, j int) bool {
		if l[i].Cost != l[j].Cost {
			return l[i].Cost > l[j].Cost
		}
		return l[i].Name < l[j].Name
	})
}

// driversPerKind and driversToolsPerServer bound the cost-drivers response:
// names past them fold into one "other (N more)" row (Drivers).
const (
	driversPerKind        = 50
	driversToolsPerServer = 25
)

// Drivers returns the window's cost per agent, skill, plugin and MCP server
// (with its tools), narrowed by w. Each list keeps its costliest names and
// folds the rest into one last row, so a list's costs still add up.
func (s *Store) Drivers(from, to string, w Where) (DriversResp, error) {
	resp := DriversResp{Agent: []NamedCost{}, Skill: []NamedCost{}, Plugin: []NamedCost{}, MCP: []MCPServerCost{}}

	tsrc, tand, targs := w.source("session_rollup")
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(r.cost_usd),0) FROM `+tsrc+` WHERE r.day BETWEEN ? AND ?`+tand,
		append([]any{from, to}, targs...)...).Scan(&resp.Total); err != nil {
		return resp, err
	}
	csrc, cand, cargs := w.joined("session_rollup")
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(r.cost_usd),0) FROM `+csrc+` WHERE r.day BETWEEN ? AND ? AND d.parsed = 1`+cand,
		append([]any{from, to}, cargs...)...).Scan(&resp.CoveredTotal); err != nil {
		return resp, err
	}

	src, and, wargs := w.source("driver_rollup")
	// The cap runs in SQL so a transcript that invents names by the million
	// never reaches Go memory or the response: per kind the costliest
	// driversPerKind names, per kept server its costliest driversToolsPerServer
	// tools, and one fold row (name '', n > 0) per kind or server for the rest.
	// Servers rank over every server key, a tool-only parent included at cost 0,
	// by cost then name, as the sort below does. Tools of a folded server are
	// left out: the page never sums tools, only servers.
	args := append([]any{from, to}, wargs...)
	args = append(args, driversPerKind, driversToolsPerServer, driversPerKind, driversPerKind, driversToolsPerServer, driversPerKind)
	rows, err := s.db.Query(`
WITH g AS (
  SELECT r.kind AS kind, r.name AS name, CASE WHEN r.kind = 'mcp_tool' THEN r.parent ELSE '' END AS parent, SUM(r.cost_usd) AS c
  FROM `+src+` WHERE r.day BETWEEN ? AND ?`+and+`
  GROUP BY 1, 2, 3),
srv AS (
  SELECT server, SUM(sc) AS sc FROM (
    SELECT name AS server, c AS sc FROM g WHERE kind = 'mcp_server'
    UNION ALL SELECT parent, 0 FROM g WHERE kind = 'mcp_tool') GROUP BY server),
sr AS (SELECT server, ROW_NUMBER() OVER (ORDER BY sc DESC, server) AS srn FROM srv),
k AS (
  SELECT g.kind, g.name, g.parent, g.c,
    CASE WHEN g.kind = 'mcp_server' THEN s1.srn
         ELSE ROW_NUMBER() OVER (PARTITION BY g.kind, g.parent ORDER BY g.c DESC, g.name) END AS rn,
    CASE WHEN g.kind = 'mcp_tool' THEN s2.srn ELSE 0 END AS prn
  FROM g
  LEFT JOIN sr s1 ON g.kind = 'mcp_server' AND s1.server = g.name
  LEFT JOIN sr s2 ON g.kind = 'mcp_tool' AND s2.server = g.parent)
SELECT kind, name, parent, c, 0 FROM k
  WHERE prn <= ? AND rn <= CASE WHEN kind = 'mcp_tool' THEN ? ELSE ? END
UNION ALL
SELECT kind, '', parent, SUM(c), COUNT(*) FROM k
  WHERE prn <= ? AND rn > CASE WHEN kind = 'mcp_tool' THEN ? ELSE ? END
  GROUP BY kind, parent`, args...)
	if err != nil {
		return resp, err
	}
	defer rows.Close()
	servers := map[string]*MCPServerCost{}
	server := func(name string) *MCPServerCost {
		m := servers[name]
		if m == nil {
			m = &MCPServerCost{Server: name, Tools: []NamedCost{}}
			servers[name] = m
		}
		return m
	}
	// Fold rows go last in their list whatever their cost: they are not a name.
	folds := map[string]NamedCost{}
	toolFolds := map[string]NamedCost{}
	var serverFold *MCPServerCost
	for rows.Next() {
		var kind, name, parent string
		var c float64
		var n int
		if err := rows.Scan(&kind, &name, &parent, &c, &n); err != nil {
			return resp, err
		}
		if n > 0 {
			fold := NamedCost{Name: fmt.Sprintf("other (%d more)", n), Cost: c, Other: true}
			switch kind {
			case "mcp_server":
				serverFold = &MCPServerCost{Server: fold.Name, Cost: c, Other: true, Tools: []NamedCost{}}
			case "mcp_tool":
				server(parent)
				toolFolds[parent] = fold
			default:
				folds[kind] = fold
			}
			continue
		}
		switch kind {
		case "agent":
			resp.Agent = append(resp.Agent, NamedCost{Name: name, Cost: c})
		case "skill":
			resp.Skill = append(resp.Skill, NamedCost{Name: name, Cost: c})
		case "plugin":
			resp.Plugin = append(resp.Plugin, NamedCost{Name: name, Cost: c})
		case "mcp_server":
			server(name).Cost += c
		case "mcp_tool":
			m := server(parent)
			m.Tools = append(m.Tools, NamedCost{Name: name, Cost: c})
		}
	}
	if err := rows.Err(); err != nil {
		return resp, err
	}
	withFold := func(l []NamedCost, kind string) []NamedCost {
		sortNamed(l)
		if f, ok := folds[kind]; ok {
			l = append(l, f)
		}
		return l
	}
	resp.Agent = withFold(resp.Agent, "agent")
	resp.Skill = withFold(resp.Skill, "skill")
	resp.Plugin = withFold(resp.Plugin, "plugin")
	for p, m := range servers {
		sortNamed(m.Tools)
		if f, ok := toolFolds[p]; ok {
			m.Tools = append(m.Tools, f)
		}
		resp.MCP = append(resp.MCP, *m)
	}
	sort.Slice(resp.MCP, func(i, j int) bool {
		if resp.MCP[i].Cost != resp.MCP[j].Cost {
			return resp.MCP[i].Cost > resp.MCP[j].Cost
		}
		return resp.MCP[i].Server < resp.MCP[j].Server
	})
	if serverFold != nil {
		resp.MCP = append(resp.MCP, *serverFold)
	}
	return resp, nil
}
