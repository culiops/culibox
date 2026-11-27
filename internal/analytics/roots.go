package analytics

import (
	"path/filepath"
	"runtime"
	"strings"
)

// RootInfo says where a transcript was written, from the ingest ROOT its path
// sits under, never from the transcript's content.
type RootInfo struct {
	// Runtime is "host" (the user's own Claude Code, directly or through an
	// agent account), "box" (a contained agent), or "" when the path is under no
	// known root.
	Runtime string
	// Account is the agent account of an `accounts/<a>` root, else "".
	Account string
	// Key names the box: `<box>` for boxstate/<box>/..., `.conversations/<x>`
	// for a conversations folder, else "".
	Key string
}

// classifier maps a transcript's absolute path to its root's info and the path
// below that root. A nil classifier knows no root.
type classifier func(path string) (info RootInfo, rel string, ok bool)

// underDir returns the part of path below dir (no leading slash) when path is
// strictly inside dir. On darwin the prefix is compared case-folded, because the
// default volume is case-insensitive; the rest keeps the path's own case.
func underDir(path, dir string) (string, bool) {
	dir = strings.TrimRight(dir, "/")
	if dir == "" || len(path) <= len(dir)+1 || path[len(dir)] != '/' {
		return "", false
	}
	head := path[:len(dir)]
	if head != dir && !(runtime.GOOS == "darwin" && strings.EqualFold(head, dir)) {
		return "", false
	}
	return path[len(dir)+1:], true
}

// eqSeg compares one literal path segment; case-folded on darwin like underDir.
func eqSeg(seg, lit string) bool {
	return seg == lit || (runtime.GOOS == "darwin" && strings.EqualFold(seg, lit))
}

// classifyPath finds which transcript root a path is under. It is pure string
// work: it never stats, so a box-chosen path costs nothing and cannot be
// followed. Recognised, with rel the path below the projects dir:
//
//	<home>/.claude/projects/<rel>                       host
//	<dataDir>/accounts/<a>/claude/projects/<rel>        host, account a
//	<dataDir>/boxstate/.conversations/<x>/<rel>         box, key .conversations/<x>
//	<dataDir>/boxstate/<box>/.claude/projects/<rel>     box, key <box>
//
// Anything else returns ok = false.
func classifyPath(path, home, dataDir string) (info RootInfo, rel string, ok bool) {
	if home != "" {
		if r, in := underDir(path, filepath.Join(home, ".claude", "projects")); in {
			return RootInfo{Runtime: "host"}, r, true
		}
	}
	if dataDir == "" {
		return RootInfo{}, "", false
	}
	if r, in := underDir(path, filepath.Join(dataDir, "accounts")); in {
		p := strings.SplitN(r, "/", 4)
		if len(p) == 4 && p[0] != "" && eqSeg(p[1], "claude") && eqSeg(p[2], "projects") && p[3] != "" {
			return RootInfo{Runtime: "host", Account: p[0]}, p[3], true
		}
		return RootInfo{}, "", false
	}
	if r, in := underDir(path, filepath.Join(dataDir, "boxstate")); in {
		if strings.HasPrefix(r, ".conversations/") {
			p := strings.SplitN(r, "/", 3)
			if len(p) == 3 && p[1] != "" && p[2] != "" {
				return RootInfo{Runtime: "box", Key: ".conversations/" + p[1]}, p[2], true
			}
			return RootInfo{}, "", false
		}
		p := strings.SplitN(r, "/", 4)
		if len(p) == 4 && p[0] != "" && eqSeg(p[1], ".claude") && eqSeg(p[2], "projects") && p[3] != "" {
			return RootInfo{Runtime: "box", Key: p[0]}, p[3], true
		}
	}
	return RootInfo{}, "", false
}

// legacyKeyFromRel derives a session's (session_id, project) from a transcript
// path below its root: `<slug>/<sid>.jsonl` and `<slug>/<sid>/subagents/<any>.jsonl`
// (a subagent file belongs to its parent session). A file directly in the root,
// or any other shape, returns ok = false.
func legacyKeyFromRel(rel string) (sessionID, project string, ok bool) {
	p := strings.Split(filepath.ToSlash(rel), "/")
	for _, s := range p {
		if s == "" {
			return "", "", false
		}
	}
	if !strings.HasSuffix(p[len(p)-1], ".jsonl") {
		return "", "", false
	}
	switch {
	case len(p) == 2:
		return strings.TrimSuffix(p[1], ".jsonl"), p[0], true
	case len(p) >= 4 && p[2] == "subagents":
		return p[1], p[0], true
	}
	return "", "", false
}

// tailRel is the last segments of a path that sit where a rel would: the last
// four for a subagent file, else the last two. It lets a path under no known
// root still give a session its id and project.
func tailRel(path string) string {
	p := strings.Split(filepath.ToSlash(path), "/")
	n := 2
	if len(p) >= 4 && p[len(p)-2] == "subagents" {
		n = 4
	}
	if len(p) < n {
		return ""
	}
	return strings.Join(p[len(p)-n:], "/")
}

// isSubagentPath reports whether a transcript path is a subagent file,
// `<slug>/<sid>/subagents/<any>.jsonl`, the shape legacyKeyFromRel gives to the
// parent session. It reads the path's last four segments (as tailRel does), so
// it needs no root and a "subagents" dir higher up the path cannot match.
func isSubagentPath(path string) bool {
	p := strings.Split(filepath.ToSlash(path), "/")
	return len(p) >= 4 && p[len(p)-2] == "subagents"
}
