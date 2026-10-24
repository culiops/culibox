package session

import (
	"encoding/json"
	"io"
	"sort"
	"strings"

	"github.com/culiops/agent-box/internal/boxread"
)

// maxAttributionBytes bounds how much of one transcript AttributionFor reads. A
// box can grow a transcript without limit; past the cap only what was read is
// attributed.
const maxAttributionBytes = 256 << 20

// attributeCapped is attributeTools over at most cap bytes of r.
func attributeCapped(r io.Reader, cap int64) Attribution {
	return attributeTools(io.LimitReader(r, cap))
}

// ToolStat is one row of the context-attribution breakdown: how much a tool / MCP
// server / skill has poured into the conversation over the session.
type ToolStat struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"` // tool | mcp | skill
	Calls    int    `json:"calls"`
	CtxBytes int64  `json:"ctxBytes"` // cumulative tool_result payload bytes (a proxy)
}

// Attribution is the whole breakdown for one session. Web search/fetch are NOT
// tracked here: in Claude Code they are client-side tools that appear as ordinary
// tool_use rows (WebSearch/WebFetch), so they already show in Tools — a separate
// server_tool_use counter reads 0 and would be misleading.
type Attribution struct {
	Tools        []ToolStat `json:"tools"` // sorted by CtxBytes desc, capped
	OutputTokens int        `json:"outputTokens"`
}

// displayKey groups mcp__<server>__* under <server> (kind=mcp) and the Skill tool
// under "Skill" (kind=skill); everything else stays itself (kind=tool).
func displayKey(name string) (key, kind string) {
	if strings.HasPrefix(name, "mcp__") {
		rest := name[len("mcp__"):]
		if i := strings.Index(rest, "__"); i >= 0 {
			return rest[:i], "mcp"
		}
		return rest, "mcp"
	}
	if name == "Skill" {
		return "Skill", "skill"
	}
	return name, "tool"
}

// attributeTools streams a JSONL transcript once and aggregates tool_result bytes
// by tool. Pure (io.Reader in). Long-line-safe: boxread.LineReader, never Scanner
// (a single JSONL line — a big tool_result or thinking block — exceeds Scanner's
// 64 KiB default and would be truncated/errored). A line over
// boxread.MaxLineBytes is skipped unparsed, so a hostile newline-free file
// cannot make the daemon buffer it whole.
func attributeTools(r io.Reader) Attribution {
	idKey := map[string]string{}
	calls := map[string]int{}
	kind := map[string]string{}
	bytesBy := map[string]int64{}
	var a Attribution
	lr := boxread.NewLineReader(r, boxread.MaxLineBytes)
	for {
		line, _, over, err := lr.Next()
		if len(line) > 0 && !over {
			var rec struct {
				Message struct {
					Role  string `json:"role"`
					Usage struct {
						OutputTokens int `json:"output_tokens"`
					} `json:"usage"`
					Content []struct {
						Type      string          `json:"type"`
						ID        string          `json:"id"`
						Name      string          `json:"name"`
						ToolUseID string          `json:"tool_use_id"`
						Content   json.RawMessage `json:"content"`
					} `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &rec) == nil {
				a.OutputTokens += rec.Message.Usage.OutputTokens
				for _, b := range rec.Message.Content {
					switch b.Type {
					case "tool_use":
						k, kd := displayKey(b.Name)
						idKey[b.ID] = k
						calls[k]++
						kind[k] = kd
					case "tool_result":
						k := idKey[b.ToolUseID]
						if k == "" {
							k = "?"
							if _, seen := kind[k]; !seen {
								kind[k] = "tool"
							}
						}
						bytesBy[k] += int64(len(b.Content))
					}
				}
			}
		}
		if err != nil {
			break
		}
	}
	// Every key that has calls OR bytes becomes a row (an orphan tool_result has
	// bytes but no calls entry).
	seen := map[string]bool{}
	add := func(k string) {
		if seen[k] {
			return
		}
		seen[k] = true
		a.Tools = append(a.Tools, ToolStat{Name: k, Kind: kind[k], Calls: calls[k], CtxBytes: bytesBy[k]})
	}
	for k := range calls {
		add(k)
	}
	for k := range bytesBy {
		add(k)
	}
	sort.SliceStable(a.Tools, func(i, j int) bool { return a.Tools[i].CtxBytes > a.Tools[j].CtxBytes })
	if len(a.Tools) > 20 {
		a.Tools = a.Tools[:20]
	}
	return a
}

// AttributionFor streams a claude session's transcript into attributeTools. Same
// gate/locator as UsageFor. ok=false for non-claude / no-transcript / unreadable.
func (m *Manager) AttributionFor(id string) (Attribution, bool) {
	l := m.locate(id)
	if !l.claude || l.path == "" {
		return Attribution{}, false
	}
	f, _, ok := l.openTranscript()
	if !ok {
		return Attribution{}, false
	}
	defer f.Close()
	return attributeCapped(f, maxAttributionBytes), true
}
