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
func (m *Manager) runArgs(boxName, cwd string) []string {
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
		"-v", stateVolume(boxName) + ":" + boxHome + "/.claude",
	}
	if m.runtime == runtimeSysbox {
		args = append(args, "--runtime="+runtimeSysbox)
	}
	return append(args, m.opts.Image, "sleep", "infinity")
}

// execArgv builds the argv a session actually runs. Environment is forwarded BY
// NAME (`-e NAME`), never by value: argv is world-readable through `ps` on the
// host, so a value here would undo the secret handling the launcher does.
func (m *Manager) execArgv(boxName, cwd string, argv, env []string) []string {
	out := []string{m.opts.DockerBin, "exec", "-it", "-w", cwd}
	for _, name := range forwardableEnvNames(env) {
		out = append(out, "-e", name)
	}
	out = append(out, boxName)
	return append(out, argv...)
}

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
