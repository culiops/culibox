// Package launcher composes the command + environment for a spawned agent.
// It owns the env model: base policy (clean/login/inherit), Claude-marker
// stripping, per-agent static env, and secret injection by reference. Secret
// values are read only here for injection and are never logged.
package launcher

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/culiops/culiops-agent-console/internal/config"
	"github.com/culiops/culiops-agent-console/internal/secrets"
)

// SpawnSpec is the request to launch an agent.
type SpawnSpec struct {
	Agent string
	Cwd   string
}

// Builder composes argv + env for a spawn.
type Builder interface {
	Build(spec SpawnSpec) (argv []string, env []string, err error)
}

// claudeSessionEnvBlocklist: Claude Code session-identity vars that make a
// spawned `claude` behave as a nested child session. Stripped from every base.
var claudeSessionEnvBlocklist = map[string]bool{
	"CLAUDECODE":                                true,
	"CLAUDE_CODE_CHILD_SESSION":                 true,
	"CLAUDE_CODE_SESSION_ID":                    true,
	"CLAUDE_CODE_ENTRYPOINT":                    true,
	"CLAUDE_CODE_EXECPATH":                      true,
	"CLAUDE_PID":                                true,
	"CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX": true,
}

// cleanBaseVars: the curated allowlist for base=clean (plus any LC_* and TERM).
var cleanBaseVars = []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG"}

// AgentLauncher builds commands from configured agents + a secret resolver.
type AgentLauncher struct {
	agents  map[string]config.AgentConfig
	secrets secrets.Resolver
	shell   string
}

// New indexes agents and resolves a usable login shell.
func New(agents []config.AgentConfig, resolver secrets.Resolver) *AgentLauncher {
	m := make(map[string]config.AgentConfig, len(agents))
	for _, a := range agents {
		m[a.Name] = a
		if a.BasePolicy() == config.BaseLogin && len(a.Secrets) > 0 {
			log.Printf("config warning: agent %q is base=login and declares secrets; the login profile "+
				"already exposes profile env and can override injected values — use base=clean for contained/authoritative secrets", a.Name)
		}
	}
	return &AgentLauncher{agents: m, secrets: resolver, shell: resolveShell()}
}

// resolveShell returns a usable login shell: $SHELL if it's an executable that
// isn't nologin, else /bin/bash (so a service account with SHELL=/usr/sbin/nologin
// doesn't break base=login).
func resolveShell() string {
	sh := os.Getenv("SHELL")
	if sh == "" || strings.Contains(filepath.Base(sh), "nologin") || !isExecutable(sh) {
		return "/bin/bash"
	}
	return sh
}

func isExecutable(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

// Build composes (argv, env) for the agent per its base policy + env + secrets.
func (l *AgentLauncher) Build(spec SpawnSpec) ([]string, []string, error) {
	cfg, ok := l.agents[spec.Agent]
	if !ok {
		return nil, nil, fmt.Errorf("launcher: unknown agent %q", spec.Agent)
	}

	env := l.baseEnv(cfg.BasePolicy())
	for k := range env { // strip Claude session markers from any base
		if claudeSessionEnvBlocklist[k] {
			delete(env, k)
		}
	}
	for k, v := range cfg.Env { // static per-agent overrides
		env[k] = v
	}
	for _, name := range cfg.Secrets { // secrets by reference (win over env)
		v, ok, err := l.secrets.Resolve(name)
		if err != nil {
			return nil, nil, fmt.Errorf("launcher: resolving secret %q for agent %q: %w", name, cfg.Name, err)
		}
		if !ok {
			return nil, nil, fmt.Errorf("launcher: agent %q declares secret %q but it is not resolvable", cfg.Name, name)
		}
		env[name] = v
	}
	if _, ok := env["TERM"]; !ok { // explicit agent.env TERM wins; else default
		env["TERM"] = "xterm-256color"
	}

	argv, err := l.argv(cfg, env["PATH"])
	if err != nil {
		return nil, nil, err
	}
	return argv, envSlice(env), nil
}

func (l *AgentLauncher) baseEnv(policy string) map[string]string {
	if policy == config.BaseInherit {
		out := map[string]string{}
		for _, kv := range os.Environ() {
			if i := strings.IndexByte(kv, '='); i >= 0 {
				out[kv[:i]] = kv[i+1:]
			}
		}
		return out
	}
	return curatedBase() // clean and login both start here
}

func curatedBase() map[string]string {
	out := map[string]string{}
	for _, k := range cleanBaseVars {
		if v, ok := os.LookupEnv(k); ok {
			out[k] = v
		}
	}
	for _, kv := range os.Environ() { // locale
		if strings.HasPrefix(kv, "LC_") {
			if i := strings.IndexByte(kv, '='); i >= 0 {
				out[kv[:i]] = kv[i+1:]
			}
		}
	}
	if out["PATH"] == "" {
		out["PATH"] = "/usr/local/bin:/usr/bin:/bin"
	}
	if out["HOME"] == "" {
		if h, err := os.UserHomeDir(); err == nil {
			out["HOME"] = h
		}
	}
	return out
}

// argv composes the command. base=login wraps in a login shell (exec so the
// agent keeps the process-group leadership the daemon's negative-pid kill needs;
// zsh needs -i to source ~/.zshrc). Non-login resolves bin against the COMPOSED
// PATH (not the daemon's), so a clean agent's custom PATH actually finds it.
func (l *AgentLauncher) argv(cfg config.AgentConfig, pathv string) ([]string, error) {
	cmd := append([]string{cfg.Bin}, cfg.Args...)
	if cfg.BasePolicy() == config.BaseLogin {
		flags := []string{"-l", "-c"}
		if strings.Contains(filepath.Base(l.shell), "zsh") {
			flags = []string{"-l", "-i", "-c"}
		}
		return append(append([]string{l.shell}, flags...), "exec "+shellJoin(cmd)), nil
	}
	bin, err := lookPathIn(cfg.Bin, pathv)
	if err != nil {
		return nil, fmt.Errorf("launcher: agent %q binary %q not found on its PATH", cfg.Name, cfg.Bin)
	}
	return append([]string{bin}, cfg.Args...), nil
}

// lookPathIn resolves bin against the given PATH string (not the daemon's env).
// A bin containing a slash is used verbatim.
func lookPathIn(bin, pathv string) (string, error) {
	if strings.Contains(bin, "/") {
		return bin, nil
	}
	for _, dir := range filepath.SplitList(pathv) {
		if dir == "" {
			continue
		}
		cand := filepath.Join(dir, bin)
		if info, err := os.Stat(cand); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return cand, nil
		}
	}
	return "", fmt.Errorf("not found")
}

// shellJoin single-quote-escapes each token for a shell -c string. bin/args come
// from trusted config (never the API), so this is for correctness, not a trust boundary.
func shellJoin(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(q, " ")
}

func envSlice(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out) // deterministic
	return out
}
