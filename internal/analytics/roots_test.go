package analytics

import (
	"runtime"
	"testing"
)

func TestClassifyPath(t *testing.T) {
	const home, data = "/h/u", "/h/u/.local/share/culibox"
	cases := []struct {
		name, path string
		info       RootInfo
		rel        string
		ok         bool
	}{
		{"host", home + "/.claude/projects/-p/S1.jsonl", RootInfo{Runtime: "host"}, "-p/S1.jsonl", true},
		{"host subagent", home + "/.claude/projects/-p/S1/subagents/agent-x.jsonl", RootInfo{Runtime: "host"}, "-p/S1/subagents/agent-x.jsonl", true},
		{"account", data + "/accounts/work/claude/projects/-p/S1.jsonl", RootInfo{Runtime: "host", Account: "work"}, "-p/S1.jsonl", true},
		{"box", data + "/boxstate/B1/.claude/projects/-p/S1.jsonl", RootInfo{Runtime: "box", Key: "B1"}, "-p/S1.jsonl", true},
		{"conversations", data + "/boxstate/.conversations/c9/-p/S1.jsonl", RootInfo{Runtime: "box", Key: ".conversations/c9"}, "-p/S1.jsonl", true},
		{"unknown root", "/elsewhere/projects/-p/S1.jsonl", RootInfo{}, "", false},
		{"home sibling", "/h/u2/.claude/projects/-p/S1.jsonl", RootInfo{}, "", false},
		{"nothing below the projects dir", home + "/.claude/projects", RootInfo{}, "", false},
		{"box with no projects dir", data + "/boxstate/B1/other/-p/S1.jsonl", RootInfo{}, "", false},
		{"account with no projects dir", data + "/accounts/work/-p/S1.jsonl", RootInfo{}, "", false},
		{"bare conversations folder", data + "/boxstate/.conversations/c9", RootInfo{}, "", false},
	}
	for _, c := range cases {
		info, rel, ok := classifyPath(c.path, home, data)
		if info != c.info || rel != c.rel || ok != c.ok {
			t.Errorf("%s: classifyPath(%q) = %+v, %q, %v; want %+v, %q, %v", c.name, c.path, info, rel, ok, c.info, c.rel, c.ok)
		}
	}
	// Without a home, a host path is unknown; without a data dir only the home root is.
	if _, _, ok := classifyPath(home+"/.claude/projects/-p/S1.jsonl", "", data); ok {
		t.Error("a host path classified with no home set")
	}
	if _, _, ok := classifyPath(data+"/boxstate/B1/.claude/projects/-p/S1.jsonl", home, ""); ok {
		t.Error("a box path classified with no data dir set")
	}
}

// On macOS the default volume is case-insensitive, so the prefix compares
// case-folded and rel keeps the path's own case.
func TestClassifyPathDarwinCaseFold(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("case-folded prefix compare is darwin-only")
	}
	info, rel, ok := classifyPath("/Users/X/.claude/projects/-P/S1.jsonl", "/users/x", "/users/x/data")
	if !ok || info.Runtime != "host" || rel != "-P/S1.jsonl" {
		t.Errorf("host = %+v, %q, %v", info, rel, ok)
	}
	info, rel, ok = classifyPath("/Users/X/Data/BoxState/B1/.claude/projects/-P/S1.jsonl", "/users/x", "/users/x/data")
	if !ok || info.Runtime != "box" || info.Key != "B1" || rel != "-P/S1.jsonl" {
		t.Errorf("box = %+v, %q, %v", info, rel, ok)
	}
}

func TestLegacyKeyFromRel(t *testing.T) {
	cases := []struct {
		rel, sid, project string
		ok                bool
	}{
		{"-p/S1.jsonl", "S1", "-p", true},
		{"-p/S1/subagents/agent-x.jsonl", "S1", "-p", true},
		{"S1.jsonl", "", "", false},
		{"-p/S1.txt", "", "", false},
		{"-p/S1/other/agent-x.jsonl", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		sid, project, ok := legacyKeyFromRel(c.rel)
		if sid != c.sid || project != c.project || ok != c.ok {
			t.Errorf("legacyKeyFromRel(%q) = %q, %q, %v; want %q, %q, %v", c.rel, sid, project, ok, c.sid, c.project, c.ok)
		}
	}
}
