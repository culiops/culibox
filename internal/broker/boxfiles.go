package broker

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/culiops/agent-box/internal/config"
)

//go:embed agentbox-cred.js
var clientScript []byte

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

// EnsureBindingDir writes <stateRoot>/broker/binding-<hash>/{aws-config,agentbox-cred.js}.
// The binding is validated first (a hand-edited profiles.json is not
// re-validated at load, and Region is formatted into an INI file). All writes go
// through os.Root, so a symlink planted at any level is refused rather than
// followed (repo rule: host writes go through os.Root).
func EnsureBindingDir(stateRoot string, b config.AWSBinding) (string, error) {
	if err := b.Validate(); err != nil {
		return "", fmt.Errorf("broker: %w", err)
	}
	root, err := os.OpenRoot(stateRoot)
	if err != nil {
		return "", fmt.Errorf("broker: opening state root: %w", err)
	}
	defer root.Close()
	rel := filepath.Join("broker", "binding-"+b.Hash())
	for _, d := range []string{"broker", rel} {
		if err := root.Mkdir(d, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("broker: %w", err)
		}
	}
	if fi, err := root.Lstat(rel); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("broker: %s is not a plain directory", rel)
	}
	cfg := fmt.Sprintf("[profile agentbox]\ncredential_process = node %s/agentbox-cred.js\nregion = %s\n", BoxDir, b.Region)
	files := []struct {
		name string
		data []byte
	}{{"aws-config", []byte(cfg)}, {"agentbox-cred.js", clientScript}}
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

// BoxEnv is the environment that points a box's AWS tooling at the broker.
// The per-session token is added by the session layer, not here.
func BoxEnv(b config.AWSBinding, boxURL string) []string {
	return []string{
		"AWS_CONFIG_FILE=" + BoxDir + "/aws-config",
		"AWS_PROFILE=agentbox",
		"AWS_REGION=" + b.Region,
		"AWS_DEFAULT_REGION=" + b.Region,
		"AGENTBOX_BROKER=" + boxURL,
		"TF_CLI_ARGS_plan=-lock=false", // plan never takes the state lock: M3a is strictly read-only
	}
}
