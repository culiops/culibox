package analytics

import "sort"

// NamedCost is one agent, skill or plugin with its cost.
type NamedCost struct {
	Name string  `json:"name"`
	Cost float64 `json:"costUsd"`
}

// MCPServerCost is one MCP server and the tools called under it. Cost is the
// server's own row, not the sum of its tools.
type MCPServerCost struct {
	Server string      `json:"server"`
	Cost   float64     `json:"costUsd"`
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

// Drivers returns the window's cost per agent, skill, plugin and MCP server
// (with its tools), narrowed by w.
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
	rows, err := s.db.Query(`
SELECT r.kind, r.name, r.parent, SUM(r.cost_usd)
FROM `+src+` WHERE r.day BETWEEN ? AND ?`+and+`
GROUP BY r.kind, r.name, r.parent`, append([]any{from, to}, wargs...)...)
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
	for rows.Next() {
		var kind, name, parent string
		var c float64
		if err := rows.Scan(&kind, &name, &parent, &c); err != nil {
			return resp, err
		}
		switch kind {
		case "agent":
			resp.Agent = append(resp.Agent, NamedCost{name, c})
		case "skill":
			resp.Skill = append(resp.Skill, NamedCost{name, c})
		case "plugin":
			resp.Plugin = append(resp.Plugin, NamedCost{name, c})
		case "mcp_server":
			server(name).Cost += c
		case "mcp_tool":
			m := server(parent)
			m.Tools = append(m.Tools, NamedCost{name, c})
		}
	}
	if err := rows.Err(); err != nil {
		return resp, err
	}
	sortNamed(resp.Agent)
	sortNamed(resp.Skill)
	sortNamed(resp.Plugin)
	for _, m := range servers {
		sortNamed(m.Tools)
		resp.MCP = append(resp.MCP, *m)
	}
	sort.Slice(resp.MCP, func(i, j int) bool {
		if resp.MCP[i].Cost != resp.MCP[j].Cost {
			return resp.MCP[i].Cost > resp.MCP[j].Cost
		}
		return resp.MCP[i].Server < resp.MCP[j].Server
	})
	return resp, nil
}
