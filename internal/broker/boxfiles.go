package broker

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/culiops/agent-box/internal/config"
)

//go:embed agentbox-cred.js
var clientScript []byte

// ghScript is the in-box gh wrapper: it asks the broker for an installation
// token and runs the real gh with it (the shim at /usr/local/bin/gh in the
// image calls it). Only a binding with a GitHub scope gets it.
//
//go:embed agentbox-gh.js
var ghScript []byte

// GHConfigDir is gh's config dir in a bound box: a path that dies with the box,
// so nothing gh writes there (hosts.yml, caches) outlives the session.
const GHConfigDir = "/tmp/agentbox-gh"

// BoxDir is where a binding dir is mounted (read-only) inside a box.
const BoxDir = "/opt/agentbox"

// StaticCredEnv are variables through which an SDK would take credentials
// ahead of credential_process. A bound session must never carry them, or the
// broker is silently bypassed.
var StaticCredEnv = []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
	"AWS_SECURITY_TOKEN", "AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
	"AWS_CONTAINER_AUTHORIZATION_TOKEN", "AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE",
	"AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ROLE_ARN", "AWS_ROLE_SESSION_NAME", "AWS_SHARED_CREDENTIALS_FILE"}

// TokenEnv carries a session's broker token into its box. The session layer
// sets it per spawn; BoxEnv, which is per binding, never does.
const TokenEnv = "AGENTBOX_BROKER_TOKEN"

// StripEnv is every variable a bound session must not inherit from its
// profile or the daemon: the static-credential routes around credential_process
// (StaticCredEnv), a profile selector that would pick something other than the
// broker's profile, and every name the broker itself sets (BoxEnv and
// TokenEnv), so a profile can never shadow or duplicate them. It is the single
// list the session layer strips before adding the broker's values.
func StripEnv() []string {
	return append(slices.Clone(StaticCredEnv),
		"AWS_CONFIG_FILE", "AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION",
		"AGENTBOX_BROKER", TokenEnv, "TF_CLI_ARGS_plan")
}

// StripGitHubEnv is what a session with a GitHub binding must also not inherit,
// on top of StripEnv: every static-token route around the broker, plus every
// name GitHubEnv sets (a profile must not repoint gh, git or the owner list).
// It is kept apart from StripEnv so an AWS-only session keeps whatever GitHub
// variables its profile sets.
func StripGitHubEnv() []string {
	return []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN",
		"GH_CONFIG_DIR", "GH_REPO", "GH_HOST",
		"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_CONFIG_KEY_1", "GIT_CONFIG_VALUE_1",
		"GIT_TERMINAL_PROMPT", "GH_PROMPT_DISABLED", "AGENTBOX_GH_OWNERS"}
}

// EnsureBindingDir writes <stateRoot>/broker/binding-<hash>/: aws-config when
// there is an AWS binding, agentbox-cred.js always, agentbox-gh.js when there is
// a GitHub binding. The bindings are validated first (a hand-edited profiles.json
// is not re-validated at load, and Region is formatted into an INI file). With
// only an AWS binding the directory and its contents are what they always were.
// All writes go through os.Root, so a symlink planted at any level is refused
// rather than followed (repo rule: host writes go through os.Root).
func EnsureBindingDir(stateRoot string, bs config.Bindings) (string, error) {
	if !bs.Any() {
		return "", errors.New("broker: no binding to prepare")
	}
	if bs.AWS != nil {
		if err := bs.AWS.Validate(); err != nil {
			return "", fmt.Errorf("broker: %w", err)
		}
	}
	if bs.GitHub != nil {
		if err := bs.GitHub.Validate(); err != nil {
			return "", fmt.Errorf("broker: %w", err)
		}
	}
	root, err := os.OpenRoot(stateRoot)
	if err != nil {
		return "", fmt.Errorf("broker: opening state root: %w", err)
	}
	defer root.Close()
	rel := filepath.Join("broker", "binding-"+bs.Hash())
	for _, d := range []string{"broker", rel} {
		if err := root.Mkdir(d, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("broker: %w", err)
		}
	}
	if fi, err := root.Lstat(rel); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("broker: %s is not a plain directory", rel)
	}
	type file struct {
		name string
		data []byte
	}
	files := []file{{"agentbox-cred.js", clientScript}}
	if bs.AWS != nil {
		cfg := fmt.Sprintf("[profile agentbox]\ncredential_process = node %s/agentbox-cred.js\nregion = %s\n", BoxDir, bs.AWS.Region)
		files = append(files, file{"aws-config", []byte(cfg)})
	}
	if bs.GitHub != nil {
		files = append(files, file{"agentbox-gh.js", ghScript})
	}
	for _, f := range files {
		if err := replaceFile(root, rel, f.name, f.data); err != nil {
			return "", fmt.Errorf("broker: writing %s: %w", f.name, err)
		}
	}
	return filepath.Join(stateRoot, rel), nil
}

// replaceFile atomically replaces dir/name inside root. The temp name is unique
// per call: concurrent spawns on one binding run this at the same time, and a
// shared temp name let one writer truncate another's file or rename it away
// first (ENOENT, a failed spawn). O_EXCL refuses anything already at the temp
// path, a planted symlink included; the temp file is removed on any failure.
func replaceFile(root *os.Root, dir, name string, data []byte) error {
	tmp := filepath.Join(dir, "."+name+"."+NewNonce()+".tmp")
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = root.Rename(tmp, filepath.Join(dir, name))
	}
	if err != nil {
		_ = root.Remove(tmp)
	}
	return err
}

// BoxEnv is the environment that points a box's tooling at the broker: the AWS
// block when there is an AWS binding (unchanged), the broker endpoint always,
// and GitHubEnv when there is a GitHub binding. The per-session token is added
// by the session layer, not here.
func BoxEnv(bs config.Bindings, boxURL string) []string {
	var env []string
	if bs.AWS != nil {
		env = append(env,
			"AWS_CONFIG_FILE="+BoxDir+"/aws-config",
			"AWS_PROFILE=agentbox",
			"AWS_REGION="+bs.AWS.Region,
			"AWS_DEFAULT_REGION="+bs.AWS.Region)
	}
	env = append(env, "AGENTBOX_BROKER="+boxURL)
	if bs.AWS != nil {
		env = append(env, "TF_CLI_ARGS_plan=-lock=false") // plan never takes the state lock: M3a is strictly read-only
	}
	if bs.GitHub != nil {
		env = append(env, GitHubEnv(*bs.GitHub)...)
	}
	return env
}

// GitHubEnv wires git and gh to the broker. git asks agentbox-cred.js for a
// token per request (useHttpPath makes it say which repo), never prompts, and
// stores nothing; gh keeps its config in a throwaway dir and never prompts. No
// token is in this env: both fetch one over the broker socket when they need it.
func GitHubEnv(g config.GitHubBinding) []string {
	owners := make([]string, len(g.Owners))
	for i, o := range g.Owners {
		owners[i] = o.Owner
	}
	return []string{
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=credential.https://github.com.helper",
		"GIT_CONFIG_VALUE_0=!node " + BoxDir + "/agentbox-cred.js github-git",
		"GIT_CONFIG_KEY_1=credential.https://github.com.useHttpPath",
		"GIT_CONFIG_VALUE_1=true",
		"GIT_TERMINAL_PROMPT=0",
		"GH_CONFIG_DIR=" + GHConfigDir,
		"GH_PROMPT_DISABLED=1",
		"AGENTBOX_GH_OWNERS=" + strings.Join(owners, ","),
	}
}
