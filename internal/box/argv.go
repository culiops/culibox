package box

import (
	"os"
	"sort"
	"strconv"
	"strings"
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
}

// runArgs builds `docker run -d` for a box. Deny by default: exactly two mounts,
// and never the docker socket, the host home, or ~/.ssh. The workspace mounts at
// its identical host path so the file tree, viewer, upload/download and terminal
// link tokenizing keep working untouched, and agent-printed paths stay clickable.
func (m *Manager) runArgs(boxName, cwd string, extraRO []Mount) []string {
	args := []string{
		"run", "-d", "--init",
		"--name", boxName,
		"--label", LabelWorkspace + "=" + cwd,
		"--label", LabelInstance + "=" + m.opts.Instance,
		"--cap-drop=ALL",
		"--security-opt", "no-new-privileges",
		"--read-only",
		"--tmpfs", "/tmp:rw,nosuid,nodev,exec,size=512m",
		"--pids-limit", "512",
		"--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()),
		"-v", cwd + ":" + cwd + ":rw",
		// The WHOLE home, not just ~/.claude. Claude Code's primary state file is
		// $HOME/.claude.json, which sits beside that directory — with only
		// ~/.claude mounted it lands on the read-only rootfs, so every boxed
		// session starts un-onboarded and unresumable while writes report success.
		"-v", m.stateDir(boxName) + ":" + boxHome,
	}
	// Extra binds are always :ro. The caller chooses what, never whether.
	for _, mt := range extraRO {
		args = append(args, "-v", mt.Host+":"+mt.Box+":ro")
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
	out := []string{m.opts.DockerBin, "exec", "-it", "-w", cwd}
	for _, name := range forwardableEnvNames(env) {
		out = append(out, "-e", name)
	}
	out = append(out, boxName, "sh", "-c", inContainerWrap, pidFile(sessionID))
	return append(out, argv...)
}

// inContainerWrap records the shell's pid ($$, preserved across exec) into $0,
// then becomes the agent. Only shell builtins, so it works in a minimal image.
const inContainerWrap = `echo $$ > "$0"; exec "$@"`

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
