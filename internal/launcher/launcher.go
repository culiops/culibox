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
	"sync"

	"github.com/culiops/culiops-agent-console/internal/config"
	"github.com/culiops/culiops-agent-console/internal/secrets"
)

// SpawnSpec is the request to launch an agent.
type SpawnSpec struct {
	Agent   string
	Cwd     string
	Options map[string]any // per-spawn option overrides (profile keys only)
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
// The agents map is refreshable at runtime (SetAgents) so newly stored profiles
// are spawnable without a restart.
type AgentLauncher struct {
	mu         sync.RWMutex
	agents     map[string]config.AgentConfig
	secrets    secrets.Resolver
	shell      string
	optionDefs map[string]config.OptionDef
}

// SetAgents replaces the launchable profile set (config seeds merged with the
// runtime store).
func (l *AgentLauncher) SetAgents(agents []config.AgentConfig) {
	m := make(map[string]config.AgentConfig, len(agents))
	for _, a := range agents {
		m[a.Name] = a
	}
	l.mu.Lock()
	l.agents = m
	l.mu.Unlock()
}

func (l *AgentLauncher) agent(name string) (config.AgentConfig, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	c, ok := l.agents[name]
	return c, ok
}

// New indexes agents + option definitions and resolves a usable login shell.
func New(agents []config.AgentConfig, resolver secrets.Resolver, optionDefs []config.OptionDef) *AgentLauncher {
	m := make(map[string]config.AgentConfig, len(agents))
	for _, a := range agents {
		m[a.Name] = a
		if a.BasePolicy() == config.BaseLogin && len(a.Secrets) > 0 {
			log.Printf("config warning: agent %q is base=login and declares secrets; the login profile "+
				"already exposes profile env and can override injected values — use base=clean for contained/authoritative secrets", a.Name)
		}
	}
	defs := make(map[string]config.OptionDef, len(optionDefs))
	for _, o := range optionDefs {
		defs[o.Key] = o
	}
	return &AgentLauncher{agents: m, secrets: resolver, shell: resolveShell(), optionDefs: defs}
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
	cfg, ok := l.agent(spec.Agent)
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

	// Merge option defaults ⊕ per-spawn overrides (profile keys only). Apply the
	// ENV part at the static-env layer — BEFORE secrets — so an injected secret
	// always wins over an option env of the same name.
	opts := l.mergeOptions(cfg, spec)
	l.applyOptionEnv(opts, env)

	for _, name := range cfg.Secrets { // secrets by reference (authoritative)
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

	// Agent command + option FLAGS appended to the cmd slice, so for base=login
	// they pass through shellJoin's single-quoting (never string-concatenated
	// post-quote — that would be an injection vector).
	cmd := append([]string{cfg.Bin}, cfg.Args...)
	cmd = l.applyOptionFlags(opts, cmd)

	argv, err := l.argv(cfg, cmd, env["PATH"])
	if err != nil {
		return nil, nil, err
	}
	return argv, envSlice(env), nil
}

// mergeOptions layers per-spawn overrides over the profile's option defaults,
// keeping only keys the profile actually exposes.
func (l *AgentLauncher) mergeOptions(cfg config.AgentConfig, spec SpawnSpec) map[string]any {
	merged := make(map[string]any, len(cfg.Options))
	for k, v := range cfg.Options {
		merged[k] = v
	}
	for k, v := range spec.Options {
		if _, ok := cfg.Options[k]; !ok {
			continue // option not exposed by this profile → ignore
		}
		// Accept the override only if it's a valid value for the option's type;
		// otherwise keep the (config-validated) profile default. An untrusted
		// enum value never reaches the command.
		if def, ok := l.optionDefs[k]; ok && validOptionValue(def, v) {
			merged[k] = v
		}
	}
	return merged
}

func validOptionValue(def config.OptionDef, v any) bool {
	switch def.Type {
	case config.OptBool:
		_, ok := v.(bool)
		return ok
	case config.OptEnum:
		s, ok := v.(string)
		return ok && contains(def.Choices, s)
	}
	return false
}

// applyOptionEnv sets option-mapped env vars. Enum values are validated against
// choices (an untrusted override with a bogus value is ignored, not injected).
func (l *AgentLauncher) applyOptionEnv(opts map[string]any, env map[string]string) {
	for _, k := range sortedKeys(opts) {
		def, ok := l.optionDefs[k]
		if !ok || def.Env == "" {
			continue
		}
		switch def.Type {
		case config.OptBool:
			if truthy(opts[k]) {
				env[def.Env] = "1"
			}
		case config.OptEnum:
			if s, ok := opts[k].(string); ok && contains(def.Choices, s) {
				env[def.Env] = s
			}
		}
	}
}

// applyOptionFlags appends option-mapped flags to the agent command. Enum values
// are validated against choices before use.
func (l *AgentLauncher) applyOptionFlags(opts map[string]any, cmd []string) []string {
	for _, k := range sortedKeys(opts) {
		def, ok := l.optionDefs[k]
		if !ok || def.Flag == "" {
			continue
		}
		switch def.Type {
		case config.OptBool:
			if truthy(opts[k]) {
				cmd = append(cmd, def.Flag)
			}
		case config.OptEnum:
			if s, ok := opts[k].(string); ok && contains(def.Choices, s) {
				cmd = append(cmd, def.Flag, s)
			}
		}
	}
	return cmd
}

func sortedKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// truthy coerces a JSON-decoded value to bool without panicking on wrong types.
func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true" || t == "1"
	case float64:
		return t != 0
	default:
		return false
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
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
func (l *AgentLauncher) argv(cfg config.AgentConfig, cmd []string, pathv string) ([]string, error) {
	if cfg.BasePolicy() == config.BaseLogin {
		flags := []string{"-l", "-c"}
		if strings.Contains(filepath.Base(l.shell), "zsh") {
			flags = []string{"-l", "-i", "-c"}
		}
		return append(append([]string{l.shell}, flags...), "exec "+shellJoin(cmd)), nil
	}
	bin, err := lookPathIn(cmd[0], pathv)
	if err != nil {
		return nil, fmt.Errorf("launcher: agent %q binary %q not found on its PATH", cfg.Name, cmd[0])
	}
	return append([]string{bin}, cmd[1:]...), nil
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
