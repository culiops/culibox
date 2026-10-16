package broker

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

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
		tmp := filepath.Join(rel, "."+f.name+".tmp")
		if err := root.WriteFile(tmp, f.data, 0o600); err != nil {
			return "", fmt.Errorf("broker: writing %s: %w", f.name, err)
		}
		if err := root.Rename(tmp, filepath.Join(rel, f.name)); err != nil {
			return "", fmt.Errorf("broker: %w", err)
		}
	}
	return filepath.Join(stateRoot, rel), nil
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
