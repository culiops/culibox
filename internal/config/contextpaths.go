package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

// maxContextPaths bounds context_paths: each entry is one more bind mount and
// one more path the box can read.
const maxContextPaths = 16

// ContextPathsEnv carries the profile's context paths into the box, ":"-joined
// (a path with ":" is refused, so the join is unambiguous).
const ContextPathsEnv = "AGENTBOX_CONTEXT_PATHS"

// A context path is mounted read-only at the same path inside the box, so an
// entry under one of these would shadow the image, the box home (/home/agent,
// see boxProjects in internal/clone/share.go) or agent-box's own mounts
// (/opt/agentbox-*). contextDenyTree refuses the path itself and anything
// inside it; contextDenyExact refuses only the path itself, because a
// directory below it (a project under /tmp, /mnt/cold, /home/<user>) is a
// legitimate context.
var (
	contextDenyTree = []string{
		"/bin", "/boot", "/dev", "/etc", "/home/agent", "/lib", "/lib32", "/lib64",
		"/opt", "/proc", "/root", "/run", "/sbin", "/sys", "/usr", "/var",
	}
	contextDenyExact = []string{"/home", "/tmp", "/mnt", "/media"}
)

// contextSecretPatterns are base-name globs that look like a secret or config
// file; ContextNameWarnings flags them.
var contextSecretPatterns = []string{
	".env*", "*.pem", "*.key", "config*.json", "id_*", "*.tfstate", "*.tfstate.*",
}

// ContextHash identifies the context set for the box name: two profiles with
// different context paths must never reuse one box. The order is irrelevant.
// 16 hex chars, or "" for an empty list.
func ContextHash(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	sorted := slices.Clone(paths)
	slices.Sort(sorted)
	h := sha256.New()
	for _, p := range sorted {
		h.Write([]byte(p + "\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// ContextNameWarnings returns one line per context path whose base name looks
// like a secret or config file. Read-only still means readable, and a box has
// internet egress. This warns and does not refuse: it is the user's call.
func ContextNameWarnings(paths []string) []string {
	var out []string
	for _, p := range paths {
		base := filepath.Base(p)
		for _, pat := range contextSecretPatterns {
			if ok, _ := filepath.Match(pat, base); ok {
				out = append(out, fmt.Sprintf("context_paths %q looks like a secret or config file; the box will be able to read and send it", p))
				break
			}
		}
	}
	return out
}

// ContextPathsEnvValue is the value of ContextPathsEnv for these paths.
func ContextPathsEnvValue(paths []string) string {
	return strings.Join(paths, ":")
}

// validateContextPaths is the pure, filesystem-free check shared by
// Config.Validate and ValidateProfile. It looks at the profile's own runtime
// only; Config.Validate also resolves the instance default. Checks that need
// the disk (existence, symlinks, MountUnsafe) belong to the spawn path.
func validateContextPaths(a AgentConfig) error {
	if len(a.ContextPaths) == 0 {
		return nil
	}
	if a.Runtime == RuntimeHost {
		return errContextNeedsBox
	}
	if len(a.ContextPaths) > maxContextPaths {
		return fmt.Errorf("context_paths has %d entries; at most %d", len(a.ContextPaths), maxContextPaths)
	}
	for i, p := range a.ContextPaths {
		if err := checkContextPath(p); err != nil {
			return err
		}
		for _, prev := range a.ContextPaths[:i] {
			if prev == p {
				return fmt.Errorf("context_paths %q: duplicate", p)
			}
			if pathOverlap(prev, p) {
				return fmt.Errorf("context_paths %q and %q nest; list only the outer path", prev, p)
			}
		}
		// default_cwd and clone_of are not guaranteed clean here (an in-place
		// profile's default_cwd is never validated as clean), and pathOverlap
		// needs clean paths, so clean them or "/b/" would miss "/b/docs".
		if a.DefaultCwd != "" && pathOverlap(p, filepath.Clean(a.DefaultCwd)) {
			return fmt.Errorf("context_paths %q overlaps default_cwd %q; the workspace is already mounted", p, a.DefaultCwd)
		}
		if a.CloneOf != "" && pathOverlap(p, filepath.Clean(a.CloneOf)) {
			return fmt.Errorf("context_paths %q overlaps clone_of %q; clone_share is the way to share part of that repo", p, a.CloneOf)
		}
	}
	return nil
}

var errContextNeedsBox = errors.New(`context_paths needs runtime "box"; host sessions can already read any path`)

// checkContextPath validates one entry on its own, before it is compared with
// the others.
func checkContextPath(p string) error {
	if err := absClean("context_paths", p); err != nil {
		return err
	}
	if strings.Contains(p, ":") {
		return fmt.Errorf("context_paths %q: \":\" is not allowed", p)
	}
	if !utf8.ValidString(p) {
		return fmt.Errorf("context_paths %q: must be valid UTF-8", p)
	}
	if p == "/" {
		return fmt.Errorf("context_paths %q: the filesystem root cannot be shared", p)
	}
	for _, d := range contextDenyTree {
		if within(d, p) {
			return fmt.Errorf("context_paths %q: refused: it would shadow %s inside the box", p, d)
		}
	}
	if slices.Contains(contextDenyExact, p) {
		return fmt.Errorf("context_paths %q: refused: it would shadow %s inside the box", p, p)
	}
	return nil
}
