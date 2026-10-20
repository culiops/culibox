// Package config loads and validates the agent-box configuration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/culiops/agent-box/internal/box"
)

// Base env policies for a spawned agent.
const (
	BaseInherit = "inherit" // daemon env minus Claude session markers (default)
	BaseClean   = "clean"   // curated minimal env, no inherited secrets (opt-in lock-down)
	BaseLogin   = "login"   // run via $SHELL -l so the profile/toolchain is sourced
)

// Runtime tiers for a spawned session. box = contained in a per-workspace
// container with no host filesystem, no secrets and no docker socket; host =
// the pre-containment behaviour, trusted by definition. A session's WHOLE
// process tree lives in exactly one of these — there is deliberately no mixed
// mode, because a "boxed" session with a host shell is a trivial bypass.
const (
	RuntimeBox  = "box"
	RuntimeHost = "host"
)

// BoxConfig is the legacy per-agent box override. Nothing reads it: it stays so
// a profile that carries box.image can be detected, refused on save and ignored
// (with a log line) on load.
type BoxConfig struct {
	Image string `json:"image,omitempty"`
}

// SecretSource is one place secrets are resolved from, tried in order.
type SecretSource struct {
	Type string `json:"type"`           // "env" | "file" | "store"
	Path string `json:"path,omitempty"` // for type "file"; "store" reads the encrypted store and takes none
}

// Option types.
const (
	OptBool = "bool"
	OptEnum = "enum"
)

// OptionDef declares a launch option and how it maps to a flag and/or env var.
// bool: value true → append Flag / set Env="1". enum: value (∈ Choices) →
// append Flag <value> / set Env=<value>.
type OptionDef struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Type    string   `json:"type"`              // bool | enum
	Choices []string `json:"choices,omitempty"` // enum only
	Flag    string   `json:"flag,omitempty"`
	Env     string   `json:"env,omitempty"`
	Bins    []string `json:"bins,omitempty"` // agent bins this option applies to; empty = all
}

// AppliesTo reports whether this option is offered for an agent binary. No bins →
// applies to every agent; otherwise matches the bin exactly or by basename (so
// "/usr/local/bin/claude" matches ["claude"]).
func (o OptionDef) AppliesTo(bin string) bool {
	if len(o.Bins) == 0 {
		return true
	}
	base := filepath.Base(bin)
	for _, b := range o.Bins {
		if b == bin || b == base {
			return true
		}
	}
	return false
}

// AgentConfig describes one launchable agent CLI and its environment policy.
type AgentConfig struct {
	Name       string            `json:"name"`
	Bin        string            `json:"bin"`
	Args       []string          `json:"args,omitempty"`
	Base       string            `json:"base,omitempty"`        // inherit(default)|clean|login
	Env        map[string]string `json:"env,omitempty"`         // static per-agent overrides
	Secrets    []string          `json:"secrets,omitempty"`     // names resolved via SecretSources
	Options    map[string]any    `json:"options,omitempty"`     // option key → default value (bool/enum)
	Runtime    string            `json:"runtime,omitempty"`     // box(default)|host — separate axis from Base
	Box        BoxConfig         `json:"box,omitempty"`         // legacy; refused on save, ignored on load (see BoxConfig)
	DefaultCwd string            `json:"default_cwd,omitempty"` // optional default working dir; pre-fills the spawn form's workspace
	Account    string            `json:"account,omitempty"`     // agent account; "" = default (~/.claude)
	AWS        *AWSBinding       `json:"aws,omitempty"`         // credential-broker AWS access; boxed profiles only
	GitHub     *GitHubBinding    `json:"github,omitempty"`      // credential-broker GitHub access (read-only installation tokens); boxed profiles only
	// AWSConnections names host aws_connections entries this profile's boxes may
	// mint credentials for (several at once); exclusive with the inline AWS binding.
	AWSConnections []string `json:"aws_connections,omitempty"`
	Tokens         []string `json:"tokens,omitempty"`      // static_tokens entries (by name) this profile's boxes may fetch; boxed profiles only
	CloneOf        string   `json:"clone_of,omitempty"`    // host repo this boxed profile clones into default_cwd (clone mode)
	CloneShare     []string `json:"clone_share,omitempty"` // read-only host paths (relative to clone_of) shared into the clone

	// Replyable is the profile's default for a spawned session's reply opt-in
	// (the sidebar/card may type into it). Only a claude session ever honours
	// it; the spawn form can override it per run.
	Replyable bool `json:"replyable,omitempty"`
}

// RuntimePolicy returns the effective tier, defaulting to box. An empty value
// means "use the default", never "unset" — containment must not be reachable by
// omission.
func (a AgentConfig) RuntimePolicy() string {
	if a.Runtime == "" {
		return RuntimeBox
	}
	return a.Runtime
}

// BasePolicy returns the effective base, defaulting to inherit (agents get the
// daemon's full env minus Claude session markers; set base:clean per agent to
// contain one).
func (a AgentConfig) BasePolicy() string {
	if a.Base == "" {
		return BaseInherit
	}
	return a.Base
}

// BrokerPortOrDefault is the darwin broker's TCP port on 127.0.0.1.
func (c Config) BrokerPortOrDefault() int {
	if c.BrokerPort > 0 {
		return c.BrokerPort
	}
	return 9071
}

// EgressPortOrDefault is the egress proxy's TCP port (Linux: on the egress
// network's gateway IP; macOS: on 127.0.0.1).
func (c Config) EgressPortOrDefault() int {
	if c.EgressPort > 0 {
		return c.EgressPort
	}
	return 9072
}

// Config is the top-level app configuration.
type Config struct {
	BindAddr    string `json:"bind_addr"`
	PasswordEnv string `json:"password_env"` // env var holding the master login password
	DefaultCwd  string `json:"default_cwd"`
	EnvLabel    string `json:"env_label,omitempty"` // e.g. "PROD"/"DEV"; empty → no UI badge
	EnvColor    string `json:"env_color,omitempty"` // CSS color for the badge/strip
	AllowPublic bool   `json:"allow_public"`
	// AllowedHosts are extra Host header values this instance answers to, beyond
	// its bind address (and the loopback aliases of a loopback bind) — e.g. the
	// public hostname of a reverse proxy or tunnel. Exact host[:port]; no
	// wildcards. Everything else is refused: that is the DNS-rebinding defence.
	AllowedHosts    []string        `json:"allowed_hosts,omitempty"`
	ProfilesPath    string          `json:"profiles_path,omitempty"`    // store for runtime profiles
	DataDir         string          `json:"data_dir,omitempty"`         // default: the data root (see Root)
	RateLimitsFile  string          `json:"rate_limits_file,omitempty"` // account 5h/weekly source; default ~/.claude/abtop-rate-limits.json
	AbducoBin       string          `json:"abduco_bin,omitempty"`       // default "abduco" on PATH
	AbducoSocketDir string          `json:"abduco_socket_dir,omitempty"`
	MaxUploadBytes  int64           `json:"max_upload_bytes,omitempty"`  // per uploaded file; 0 → 64 MiB
	MaxArchiveBytes int64           `json:"max_archive_bytes,omitempty"` // per directory download; 0 → 512 MiB
	MaxArchiveFiles int             `json:"max_archive_files,omitempty"` // per directory download; 0 → 20000
	SecretSources   []SecretSource  `json:"secret_sources,omitempty"`
	Options         []OptionDef     `json:"options,omitempty"`
	Agents          []AgentConfig   `json:"agents"`
	GitHubApps      []GitHubApp     `json:"github_apps,omitempty"`     // GitHub App connections that profile github bindings name
	StaticTokens    []StaticToken   `json:"static_tokens,omitempty"`   // long-lived token entries that profile tokens name; read at daemon start
	AWSConnections  []AWSConnection `json:"aws_connections,omitempty"` // named AWS connections that profile aws_connections name; read at daemon start

	// DefaultRuntime is the tier for any agent that does not name one. It exists
	// because the quick-shell profile is GENERATED, not configured, so no
	// per-agent setting can reach it — without this, an instance whose every
	// configured agent is host still had a boxed shell profile.
	DefaultRuntime    string `json:"default_runtime,omitempty"`      // box(default)|host
	RepliesEnabled    bool   `json:"replies_enabled,omitempty"`      // kill-switch for reply + remote Yes/No; absent = off
	BundleSource      string `json:"bundle_source,omitempty"`        // "" → auto-detect; "off" → no bundle
	BoxImage          string `json:"box_image,omitempty"`            // the image every box runs; DefaultBoxImage when unset
	BoxIdleTTLSeconds int    `json:"box_idle_ttl_seconds,omitempty"` // 0 → 15m
	BoxPidsLimit      int    `json:"box_pids_limit,omitempty"`       // processes+threads per box; 0 → 512
	BoxTmpSize        string `json:"box_tmp_size,omitempty"`         // box /tmp tmpfs size, number + k/m/g; "" → 512m
	BoxMemory         string `json:"box_memory,omitempty"`           // box RAM cap (swap capped equal), number + k/m/g, min 1g; "" → 4g
	DockerBin         string `json:"docker_bin,omitempty"`           // default "docker", resolved absolute at startup
	ColimaBin         string `json:"colima_bin,omitempty"`           // macOS: default "colima"
	BoxVMCPUs         int    `json:"box_vm_cpus,omitempty"`          // macOS box VM; 0 → 4
	BoxVMMemoryGiB    int    `json:"box_vm_memory_gib,omitempty"`    // macOS box VM; 0 → 4
	BrokerPort        int    `json:"broker_port,omitempty"`          // macOS: credential broker TCP port on 127.0.0.1; 0 → 9071
	EgressPort        int    `json:"egress_port,omitempty"`          // egress proxy TCP port; 0 → 9072

	Analytics AnalyticsConfig `json:"analytics,omitempty"`
	Store     StoreConfig     `json:"store,omitzero"` // the encrypted value store (see store.go)
}

// AnalyticsConfig configures the token/prompt analytics store + ingester. The
// zero value is "enabled with defaults": Enabled is a *bool so an omitted field
// means on, and only an explicit false turns the feature (store, routes, ticker)
// off.
type AnalyticsConfig struct {
	Enabled        *bool    `json:"enabled,omitempty"`         // nil/true → on; false → off
	Roots          []string `json:"roots,omitempty"`           // transcript root glob patterns; empty → defaults (see main)
	IngestInterval string   `json:"ingest_interval,omitempty"` // e.g. "24h"; empty/invalid → 24h
	DBPath         string   `json:"db_path,omitempty"`         // empty → <dataDir>/analytics.db
}

// On reports whether analytics is enabled (the default when unset).
func (a AnalyticsConfig) On() bool { return a.Enabled == nil || *a.Enabled }

// RuntimeFor resolves an agent's tier: its own setting when it has one, else
// this instance's default, else box. Unset means contained at every level — the
// safe tier is the one you get by omission.
func (c Config) RuntimeFor(a AgentConfig) string {
	if a.Runtime != "" {
		return a.Runtime
	}
	if c.DefaultRuntime != "" {
		return c.DefaultRuntime
	}
	return RuntimeBox
}

// BoxInstance is the label that keeps this instance's boxes apart from another
// instance's (DEV vs PROD on one docker daemon): the env label, or "default".
func (c Config) BoxInstance() string {
	if c.EnvLabel != "" {
		return c.EnvLabel
	}
	return "default"
}

// DefaultBoxImage is the box image every box runs when the config names none.
const DefaultBoxImage = "culiops/agent-box:base"

// BoxImageOrDefault is the instance-wide box image (box_image), or DefaultBoxImage.
func (c Config) BoxImageOrDefault() string {
	if c.BoxImage != "" {
		return c.BoxImage
	}
	return DefaultBoxImage
}

// BoxVMSize is the macOS box VM's CPUs and memory in GiB, defaulting to 4 and 4.
func (c Config) BoxVMSize() (cpus, memGiB int) {
	cpus, memGiB = c.BoxVMCPUs, c.BoxVMMemoryGiB
	if cpus <= 0 {
		cpus = 4
	}
	if memGiB <= 0 {
		memGiB = 4
	}
	return cpus, memGiB
}

// validImageRef refuses an image name docker would read as something else. The
// image is a positional argument in `docker run … <image>`, `image inspect` and
// `--entrypoint node <image>`, so a leading "-" would be parsed as a flag. Empty
// means "use the default" and is allowed.
func validImageRef(img string) error {
	if img == "" {
		return nil
	}
	if img[0] == '-' {
		return fmt.Errorf("box image %q must not start with '-' (docker would read it as a flag)", img)
	}
	for _, r := range img {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '/', r == ':', r == '@', r == '-':
		default:
			return fmt.Errorf("box image %q contains %q; only letters, digits and . _ / : @ - are allowed", img, r)
		}
	}
	return nil
}

// BoxIdleTTL is how long a box outlives its last session, so returning to a
// project does not pay container start-up again. As with the upload caps, unset
// means the default and never "forever".
func (c Config) BoxIdleTTL() time.Duration {
	if c.BoxIdleTTLSeconds <= 0 {
		return 15 * time.Minute
	}
	return time.Duration(c.BoxIdleTTLSeconds) * time.Second
}

// Load reads and parses a JSON config file.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	return c, nil
}

// CheckPasswordEnvClash refuses a token whose value is read from the env var
// that holds the login password: the password must never double as a token.
func CheckPasswordEnvClash(passwordEnv string, tokens []StaticToken) error {
	if passwordEnv == "" {
		return nil
	}
	for _, e := range tokens {
		if e.Value.Type == "env" && e.Value.Var == passwordEnv {
			return fmt.Errorf("static_tokens[%q].value: env var %q is password_env, the login password, and cannot be a token", e.Name, passwordEnv)
		}
	}
	return nil
}

// Validate enforces the security and completeness invariants.
func (c Config) Validate() error {
	if len(c.Agents) == 0 {
		return errors.New("config: at least one agent required")
	}
	switch c.DefaultRuntime {
	case "", RuntimeBox, RuntimeHost:
	default:
		return fmt.Errorf("config: invalid default_runtime %q (want box|host)", c.DefaultRuntime)
	}
	if err := validImageRef(c.BoxImage); err != nil {
		return fmt.Errorf("config: box_image: %w", err)
	}
	if err := box.ValidateLimits(c.BoxPidsLimit, c.BoxTmpSize, c.BoxMemory); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if c.BrokerPort < 0 || c.BrokerPort > 65535 {
		return fmt.Errorf("config: broker_port %d out of range (want 1-65535, or 0 for the default)", c.BrokerPort)
	}
	if c.EgressPort < 0 || c.EgressPort > 65535 {
		return fmt.Errorf("config: egress_port %d out of range (want 1-65535, or 0 for the default)", c.EgressPort)
	}
	if c.EgressPortOrDefault() == c.BrokerPortOrDefault() {
		return fmt.Errorf("config: egress_port %d must differ from broker_port", c.EgressPortOrDefault())
	}
	if err := validStore(c.Store); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if err := ValidateGitHubApps(c.GitHubApps); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if err := ValidateStaticTokens(c.StaticTokens); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if err := ValidateAWSConnections(c.AWSConnections); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if err := CheckPasswordEnvClash(c.PasswordEnv, c.StaticTokens); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	seen := map[string]bool{}
	for _, a := range c.Agents {
		if a.Name == "" || a.Bin == "" {
			return errors.New("config: each agent requires a name and bin")
		}
		if seen[a.Name] {
			return fmt.Errorf("config: duplicate agent name %q", a.Name)
		}
		seen[a.Name] = true
		switch a.BasePolicy() {
		case BaseClean, BaseLogin, BaseInherit:
		default:
			return fmt.Errorf("config: agent %q has invalid base %q (want clean|login|inherit)", a.Name, a.Base)
		}
		switch a.RuntimePolicy() {
		case RuntimeBox, RuntimeHost:
		default:
			return fmt.Errorf("config: agent %q has invalid runtime %q (want box|host)", a.Name, a.Runtime)
		}
		if err := validateAWS(a); err != nil {
			return fmt.Errorf("config: agent %q: %w", a.Name, err)
		}
		if err := validateAWSConnBinding(a); err != nil {
			return fmt.Errorf("config: agent %q: %w", a.Name, err)
		}
		if err := CheckAWSConnections(a, c.AWSConnections); err != nil {
			return fmt.Errorf("config: agent %q: %w", a.Name, err)
		}
		if err := validateGitHub(a); err != nil {
			return fmt.Errorf("config: agent %q: %w", a.Name, err)
		}
		if err := CheckGitHubApps(a, c.GitHubApps); err != nil {
			return fmt.Errorf("config: agent %q: %w", a.Name, err)
		}
		if err := validateClone(a); err != nil {
			return fmt.Errorf("config: agent %q: %w", a.Name, err)
		}
	}
	optDefs := map[string]OptionDef{}
	for _, o := range c.Options {
		if o.Key == "" {
			return errors.New("config: option needs a key")
		}
		if _, dup := optDefs[o.Key]; dup {
			return fmt.Errorf("config: duplicate option key %q", o.Key)
		}
		if o.Type != OptBool && o.Type != OptEnum {
			return fmt.Errorf("config: option %q has invalid type %q (want bool|enum)", o.Key, o.Type)
		}
		if o.Type == OptEnum && len(o.Choices) == 0 {
			return fmt.Errorf("config: enum option %q needs choices", o.Key)
		}
		if o.Type == OptBool && len(o.Choices) > 0 {
			return fmt.Errorf("config: bool option %q must not set choices", o.Key)
		}
		if o.Flag == "" && o.Env == "" {
			return fmt.Errorf("config: option %q must set a flag and/or env", o.Key)
		}
		optDefs[o.Key] = o
	}
	for _, a := range c.Agents {
		if err := validateTokens(a); err != nil {
			return fmt.Errorf("config: agent %q: %w", a.Name, err)
		}
		if err := CheckTokens(a, c.StaticTokens, optDefs); err != nil {
			return fmt.Errorf("config: agent %q: %w", a.Name, err)
		}
		for k, v := range a.Options {
			def, ok := optDefs[k]
			if !ok {
				return fmt.Errorf("config: agent %q sets unknown option %q", a.Name, k)
			}
			if !def.AppliesTo(a.Bin) {
				return fmt.Errorf("config: agent %q sets option %q that does not apply to bin %q", a.Name, k, a.Bin)
			}
			switch def.Type {
			case OptBool:
				if _, ok := v.(bool); !ok {
					return fmt.Errorf("config: agent %q option %q default must be a bool", a.Name, k)
				}
			case OptEnum:
				if s, ok := v.(string); !ok || !contains(def.Choices, s) {
					return fmt.Errorf("config: agent %q option %q default must be one of %v", a.Name, k, def.Choices)
				}
			}
			// an option env must not collide with a declared secret (secrets stay authoritative)
			if def.Env != "" && contains(a.Secrets, def.Env) {
				return fmt.Errorf("config: agent %q option %q env %q collides with a declared secret", a.Name, k, def.Env)
			}
		}
	}
	stores := 0
	for _, s := range c.SecretSources {
		switch s.Type {
		case "env":
		case "store":
			if s.Path != "" {
				return errors.New("config: store secret_source takes no path")
			}
			if stores++; stores > 1 {
				return errors.New("config: at most one store secret_source")
			}
		case "file":
			if s.Path == "" {
				return errors.New("config: file secret_source requires a path")
			}
		default:
			return fmt.Errorf("config: unknown secret_source type %q", s.Type)
		}
	}
	for _, h := range c.AllowedHosts {
		if !validHostEntry(h) {
			return fmt.Errorf("config: invalid allowed_hosts entry %q (want host or host:port, no scheme, path or wildcard)", h)
		}
	}
	if c.AbducoSocketDir != "" && !filepath.IsAbs(c.AbducoSocketDir) {
		return fmt.Errorf("config: abduco_socket_dir %q must be an absolute path", c.AbducoSocketDir)
	}
	return c.checkBind()
}

// ValidateProfile validates a single profile (used for runtime-stored profiles)
// against the given option definitions.
func ValidateProfile(a AgentConfig, optDefs map[string]OptionDef) error {
	if a.Name == "" || a.Bin == "" {
		return errors.New("profile requires a name and bin")
	}
	if strings.TrimSpace(a.Box.Image) != "" {
		return fmt.Errorf("profile %q: box.image is not per-profile; every box runs the instance-wide box_image (set it in the config)", a.Name)
	}
	switch a.BasePolicy() {
	case BaseClean, BaseLogin, BaseInherit:
	default:
		return fmt.Errorf("profile %q has invalid base %q (want clean|login|inherit)", a.Name, a.Base)
	}
	switch a.RuntimePolicy() {
	case RuntimeBox, RuntimeHost:
	default:
		return fmt.Errorf("profile %q has invalid runtime %q (want box|host)", a.Name, a.Runtime)
	}
	if err := validateAWS(a); err != nil {
		return fmt.Errorf("profile %q: %w", a.Name, err)
	}
	if err := validateAWSConnBinding(a); err != nil {
		return fmt.Errorf("profile %q: %w", a.Name, err)
	}
	if err := validateGitHub(a); err != nil {
		return fmt.Errorf("profile %q: %w", a.Name, err)
	}
	if err := validateTokens(a); err != nil {
		return fmt.Errorf("profile %q: %w", a.Name, err)
	}
	if err := validateClone(a); err != nil {
		return fmt.Errorf("profile %q: %w", a.Name, err)
	}
	for k, v := range a.Options {
		def, ok := optDefs[k]
		if !ok {
			return fmt.Errorf("profile %q sets unknown option %q", a.Name, k)
		}
		if !def.AppliesTo(a.Bin) {
			return fmt.Errorf("profile %q sets option %q that does not apply to bin %q", a.Name, k, a.Bin)
		}
		switch def.Type {
		case OptBool:
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("profile %q option %q must be a bool", a.Name, k)
			}
		case OptEnum:
			if s, ok := v.(string); !ok || !contains(def.Choices, s) {
				return fmt.Errorf("profile %q option %q must be one of %v", a.Name, k, def.Choices)
			}
		}
		if def.Env != "" && contains(a.Secrets, def.Env) {
			return fmt.Errorf("profile %q option %q env %q collides with a declared secret", a.Name, k, def.Env)
		}
	}
	return nil
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// checkBind refuses any all-interfaces bind unless AllowPublic is set.
func (c Config) checkBind() error {
	if c.AllowPublic {
		return nil
	}
	host, _, err := net.SplitHostPort(c.BindAddr)
	if err != nil {
		return fmt.Errorf("config: invalid bind_addr %q: %w", c.BindAddr, err)
	}
	switch host {
	case "", "0.0.0.0", "::":
		return fmt.Errorf("config: bind_addr %q binds all interfaces; set allow_public to override", c.BindAddr)
	}
	return nil
}

// validHostEntry accepts what a Host header can carry: a host, optionally with
// a port, nothing else.
func validHostEntry(h string) bool {
	if h == "" || strings.Contains(h, "://") || strings.ContainsAny(h, "/*@ \t") {
		return false
	}
	if host, port, err := net.SplitHostPort(h); err == nil {
		return host != "" && port != ""
	}
	return !strings.Contains(h, ":") // a bare IPv6 must be bracketed with a port
}

// HostAllowlist is every Host header value this instance answers to, lowercased.
func (c Config) HostAllowlist() []string {
	set := map[string]bool{strings.ToLower(c.BindAddr): true}
	if host, port, err := net.SplitHostPort(c.BindAddr); err == nil && isLoopbackHost(host) {
		for _, alias := range []string{"127.0.0.1", "localhost", "::1"} {
			set[net.JoinHostPort(alias, port)] = true
		}
	}
	for _, h := range c.AllowedHosts {
		set[strings.ToLower(h)] = true
	}
	out := make([]string, 0, len(set))
	for h := range set {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

func isLoopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// ResolvedDataDir is data_dir, or ~/.local/share/agent-box for a Config that
// did not come through Resolve (which always fills data_dir).
func (c Config) ResolvedDataDir(home string) string {
	if c.DataDir != "" {
		return c.DataDir
	}
	return filepath.Join(home, ".local", "share", "agent-box")
}

// ResolvedProfilesPath is profiles_path, else profiles.json in the data dir.
// serve and doctor both locate the profile store with it.
func (c Config) ResolvedProfilesPath(dataDir string) string {
	if c.ProfilesPath != "" {
		return c.ProfilesPath
	}
	return filepath.Join(dataDir, "profiles.json")
}

// UploadLimit is the per-file upload cap, defaulting to 64 MiB. A zero or
// negative value means "unset", never "unlimited" — the upload path must never
// run uncapped, so there is deliberately no way to express that in config.
func (c Config) UploadLimit() int64 {
	if c.MaxUploadBytes <= 0 {
		return 64 << 20
	}
	return c.MaxUploadBytes
}

// ArchiveLimits returns the directory-download caps (total uncompressed bytes,
// file count), defaulting to 512 MiB and 20000. As with UploadLimit, unset is
// never unlimited.
func (c Config) ArchiveLimits() (int64, int) {
	b, n := c.MaxArchiveBytes, c.MaxArchiveFiles
	if b <= 0 {
		b = 512 << 20
	}
	if n <= 0 {
		n = 20000
	}
	return b, n
}
