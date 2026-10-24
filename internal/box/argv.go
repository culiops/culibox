package box

import (
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/culiops/agent-box/internal/extraca"
)

// hostOnlyEnv names that must never cross into a box. PATH/HOME/SHELL would
// break in-container binary resolution and leak host layout; PWD/OLDPWD are
// stale the moment we set -w; ABDUCO_SOCKET_DIR points at a host path the box
// cannot and must not reach.
var hostOnlyEnv = map[string]bool{
	"PATH":              true,
	"HOME":              true,
	"SHELL":             true,
	"USER":              true,
	"LOGNAME":           true,
	"PWD":               true,
	"OLDPWD":            true,
	"ABDUCO_SOCKET_DIR": true,
	// host paths; inside a box the per-account box home's .claude is the config dir
	"CLAUDE_CONFIG_DIR": true,
	"CODEX_HOME":        true,
}

// runArgs builds `docker run -d` for a box. Deny by default: exactly two
// writable mounts (the workspace and the box home), and never the docker socket,
// the host home, or ~/.ssh. Every extra bind, the in-place guards and a clone's
// mounts included, is read-only. The workspace mounts at
// its identical host path so the file tree, viewer, upload/download and terminal
// link tokenizing keep working untouched, and agent-printed paths stay clickable.
// fenced (an AWS binding, or any box when EgressAll is on) adds the egress
// network flags; fenced=false output is unchanged by it, except for the claims
// label every box carries.
// claims=1 tells other agent-box instances on this docker daemon that this build
// enforces exclusive workspaces; exclusive=1 marks a box another spawn must not
// share a folder with. caBundle != "" adds the extra-CA label and bundle mount;
// "" leaves the output exactly as it was before extra CAs existed.
// memory is the resolved --memory value (see memoryCap), never "" or "max".
func (m *Manager) runArgs(boxName, cwd string, extraRO []Mount, fenced, exclusive bool, caBundle, memory string) []string {
	pids, tmpSize := m.opts.PidsLimit, m.opts.TmpSize
	if pids == 0 {
		pids = DefaultPidsLimit
	}
	if tmpSize == "" {
		tmpSize = DefaultTmpSize
	}
	args := []string{
		// --pull=never: a missing (e.g. deleted pinned) image must fail the spawn,
		// not be fetched from Docker Hub under the same repo name.
		"run", "-d", "--init", "--pull=never",
		"--name", boxName,
		"--label", LabelWorkspace + "=" + cwd,
		"--label", LabelInstance + "=" + m.opts.Instance,
		"--label", LabelClaims + "=1",
		"--cap-drop=ALL",
		"--security-opt", "no-new-privileges",
		"--read-only",
		"--tmpfs", "/tmp:rw,nosuid,nodev,exec,size=" + tmpSize,
		"--pids-limit", strconv.Itoa(pids),
		// --memory-swap equal to --memory: no swap beyond the cap, so a box over
		// it is OOM-killed rather than swapping the host.
		"--memory", memory, "--memory-swap", memory,
		"--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()),
		"--mount", bindMount(cwd, cwd, false),
		// The WHOLE home, not just ~/.claude. Claude Code's primary state file is
		// $HOME/.claude.json, which sits beside that directory — with only
		// ~/.claude mounted it lands on the read-only rootfs, so every boxed
		// session starts un-onboarded and unresumable while writes report success.
		"--mount", bindMount(m.stateDir(boxName), boxHome, false),
	}
	// No box_cpus, no flag: the box can use every core docker has.
	if m.opts.CPUs > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(m.opts.CPUs, 'f', -1, 64))
	}
	if exclusive {
		args = append(args, "--label", LabelExclusive+"=1")
	}
	// Extra binds are always read-only. The caller chooses what, never whether.
	// That includes a clone-mode box's mounts (BoxKey.Clone): none is writable.
	for _, mt := range extraRO {
		args = append(args, "--mount", bindMount(mt.Host, mt.Box, true))
	}
	// The trust bundle (host path from caBundle, "" = CAs off) replaces the
	// image's own, read-only like every extra bind. The label is how ensure
	// tells a box started with another CA set from this one.
	if caBundle != "" {
		args = append(args, "--label", extraca.Label+"="+m.opts.ExtraCA.Hash(),
			"--mount", bindMount(caBundle, extraca.ContainerBundle, true))
	}
	// A fenced box joins the egress network instead of the default bridge. Both
	// values come from constants and validated config, never from a session, and
	// neither is a secret. An unfenced box gets neither flag.
	if fenced {
		e := m.egressConfig()
		args = append(args, "--network", e.Network)
		for _, h := range e.AddHosts {
			args = append(args, "--add-host", h)
		}
	}
	if m.runtime == runtimeSysbox {
		args = append(args, "--runtime="+runtimeSysbox)
	}
	return append(args, m.opts.Image, "sleep", "infinity")
}

// execArgv builds the argv a session actually runs. Environment is forwarded BY
// NAME (`-e NAME`), never by value: argv is world-readable through `ps` on the
// host, so a value here would undo the secret handling the launcher does.
//
// The command is wrapped so the in-container process records its own pid.
// Signals do NOT cross `docker exec`: killing the host-side client leaves the
// agent running inside the box, which is the same orphan class as the
// `abduco -a` clients that once pegged a core here. KillSession needs that pid
// to reach the real process.
func (m *Manager) execArgv(boxName, cwd, sessionID string, argv, env []string) []string {
	out := append([]string{m.opts.DockerBin}, m.opts.DockerArgs...)
	out = append(out, "exec", "-it", "-w", cwd)
	for _, name := range forwardableEnvNames(env) {
		out = append(out, "-e", name)
	}
	out = append(out, boxName, "sh", "-c", inContainerWrap, pidFile(sessionID), homePidArg(sessionID))
	return append(out, argv...)
}

// inContainerWrap records the shell's pid ($$, preserved across exec) into two
// files, then becomes the agent. $0 is the /tmp pidfile KillSession reads from
// inside the box. $1 is the home pidfile (see HomePidRel), which the host reads
// to find the agent's live registry file; the box owns it, so the host treats it
// as untrusted. Its write is best effort and silent: a missing directory, a
// read-only mount or the /dev/null a refused id gets must not stop the agent.
// Only shell builtins, so it works in a minimal image.
const inContainerWrap = `echo $$ > "$0"; { echo $$ > "$1"; } 2>/dev/null; shift; exec "$@"`

// pidFile is where a session's in-container pid lives. /tmp is the box's tmpfs,
// so it is writable even with a read-only rootfs and vanishes with the box.
func pidFile(sessionID string) string { return "/tmp/ac-session-" + sessionID + ".pid" }

// forwardableEnvNames extracts sorted variable names from a KEY=VALUE slice,
// dropping the values entirely and skipping host-only names.
func forwardableEnvNames(env []string) []string {
	names := make([]string, 0, len(env))
	for _, kv := range env {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		name := kv[:i]
		if hostOnlyEnv[name] {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// bindMount renders one --mount value. --mount, unlike -v, refuses a source that
// does not exist: inside a colima VM, -v on an unshared path silently creates an
// empty root-owned directory and starts the agent there. Callers guarantee both
// paths passed MountUnsafe, because docker reads this value as CSV.
func bindMount(src, dst string, ro bool) string {
	s := "type=bind,src=" + src + ",dst=" + dst
	if ro {
		s += ",readonly"
	}
	return s
}

// MountUnsafe names why docker's --mount CSV parser (or a reader of the same
// path elsewhere, such as the VM's YAML) would misread p, or "" when it is safe.
// A comma starts a new option and a quote opens a CSV field, so either one in a
// workspace name could rewrite the mount. Invalid UTF-8, every control character
// (C0, DEL, C1 including U+0085 NEL) and the U+2028/U+2029 separators are refused
// too: a YAML reader folds some of them into a space or a line break, so the VM
// would share a different directory than the one docker is told to mount.
// Refused, not escaped.
func MountUnsafe(p string) string {
	if !utf8.ValidString(p) {
		return "its path is not valid UTF-8"
	}
	for _, r := range p {
		switch {
		case r == ',':
			return "its path contains a comma, which docker --mount cannot carry safely"
		case r == '"':
			return "its path contains a double quote, which docker --mount cannot carry safely"
		case unicode.IsControl(r) || r == '\u2028' || r == '\u2029':
			return "its path contains a control or line-separator character"
		}
	}
	return ""
}
