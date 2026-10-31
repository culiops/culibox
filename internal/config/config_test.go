package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/extraca"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad(t *testing.T) {
	p := writeTemp(t, `{"bind_addr":"127.0.0.1:8080","password_env":"AB_PASS","agents":[{"name":"claude","bin":"claude","args":["--foo"]}]}`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.BindAddr != "127.0.0.1:8080" || c.PasswordEnv != "AB_PASS" {
		t.Fatalf("unexpected config: %+v", c)
	}
	if len(c.Agents) != 1 || c.Agents[0].Bin != "claude" || c.Agents[0].Args[0] != "--foo" {
		t.Fatalf("agents parsed wrong: %+v", c.Agents)
	}
}

func TestValidate_RejectsPublicBind(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8080", ":8080", "[::]:8080"} {
		c := Config{BindAddr: addr, Agents: []AgentConfig{{Name: "a", Bin: "a"}}}
		if err := c.Validate(); err == nil {
			t.Errorf("bind %q should be rejected", addr)
		}
	}
}

func TestValidate_AllowsLoopbackAndOverride(t *testing.T) {
	c := Config{BindAddr: "127.0.0.1:8080", Agents: []AgentConfig{{Name: "a", Bin: "a"}}}
	if err := c.Validate(); err != nil {
		t.Errorf("loopback should pass: %v", err)
	}
	c2 := Config{BindAddr: "0.0.0.0:8080", AllowPublic: true, Agents: []AgentConfig{{Name: "a", Bin: "a"}}}
	if err := c2.Validate(); err != nil {
		t.Errorf("allow_public override should pass: %v", err)
	}
}

func TestValidate_RequiresAgent(t *testing.T) {
	c := Config{BindAddr: "127.0.0.1:8080"}
	if err := c.Validate(); err == nil {
		t.Error("empty agents should be rejected")
	}
}

func TestLoad_EnvModelFields(t *testing.T) {
	p := writeTemp(t, `{
      "bind_addr":"127.0.0.1:8080","password_env":"AB_PASS",
      "secret_sources":[{"type":"env"},{"type":"file","path":"/x/secrets.env"}],
      "agents":[
        {"name":"claude","bin":"claude","base":"login","env":{"ANTHROPIC_MODEL":"m"}},
        {"name":"locked","bin":"claude","base":"clean","secrets":["TOK"]}
      ]}`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.SecretSources) != 2 || c.SecretSources[1].Type != "file" || c.SecretSources[1].Path != "/x/secrets.env" {
		t.Fatalf("secret_sources parsed wrong: %+v", c.SecretSources)
	}
	if c.Agents[0].Base != "login" || c.Agents[0].Env["ANTHROPIC_MODEL"] != "m" {
		t.Fatalf("agent0 parsed wrong: %+v", c.Agents[0])
	}
	if len(c.Agents[1].Secrets) != 1 || c.Agents[1].Secrets[0] != "TOK" {
		t.Fatalf("agent1 secrets parsed wrong: %+v", c.Agents[1])
	}
}

func TestLoad_AbducoFields(t *testing.T) {
	p := writeTemp(t, `{
      "bind_addr":"127.0.0.1:8080","password_env":"AB_PASS",
      "data_dir":"/var/lib/agent-box","abduco_bin":"/usr/local/bin/abduco",
      "abduco_socket_dir":"/run/agent-box/abduco",
      "agents":[{"name":"claude","bin":"claude"}]}`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDir != "/var/lib/agent-box" {
		t.Fatalf("data_dir=%q", c.DataDir)
	}
	if c.AbducoBin != "/usr/local/bin/abduco" {
		t.Fatalf("abduco_bin=%q", c.AbducoBin)
	}
	if c.AbducoSocketDir != "/run/agent-box/abduco" {
		t.Fatalf("abduco_socket_dir=%q", c.AbducoSocketDir)
	}
}

func TestBasePolicy_DefaultsInherit(t *testing.T) {
	if (AgentConfig{}).BasePolicy() != BaseInherit {
		t.Fatal("empty base should default to inherit")
	}
	if (AgentConfig{Base: "clean"}).BasePolicy() != BaseClean {
		t.Fatal("explicit base should be honored")
	}
}

func TestValidate_RejectsInvalidBase(t *testing.T) {
	c := Config{BindAddr: "127.0.0.1:8080", Agents: []AgentConfig{{Name: "a", Bin: "a", Base: "bogus"}}}
	if err := c.Validate(); err == nil {
		t.Error("invalid base should be rejected")
	}
}

func TestValidate_Options(t *testing.T) {
	mk := func(opts []OptionDef, agents []AgentConfig) Config {
		return Config{BindAddr: "127.0.0.1:8080", Options: opts, Agents: agents}
	}
	good := mk(
		[]OptionDef{{Key: "skip", Type: "bool", Flag: "-x"}, {Key: "eff", Type: "enum", Choices: []string{"hi"}, Flag: "--eff"}},
		[]AgentConfig{{Name: "a", Bin: "a", Options: map[string]any{"skip": true, "eff": "hi"}}},
	)
	if err := good.Validate(); err != nil {
		t.Fatalf("valid options config rejected: %v", err)
	}

	bad := map[string]Config{
		"bad type":          mk([]OptionDef{{Key: "x", Type: "weird", Flag: "-x"}}, []AgentConfig{{Name: "a", Bin: "a"}}),
		"enum no choices":   mk([]OptionDef{{Key: "x", Type: "enum", Flag: "-x"}}, []AgentConfig{{Name: "a", Bin: "a"}}),
		"no flag/env":       mk([]OptionDef{{Key: "x", Type: "bool"}}, []AgentConfig{{Name: "a", Bin: "a"}}),
		"dup key":           mk([]OptionDef{{Key: "x", Type: "bool", Flag: "-a"}, {Key: "x", Type: "bool", Flag: "-b"}}, []AgentConfig{{Name: "a", Bin: "a"}}),
		"bool with choices": mk([]OptionDef{{Key: "x", Type: "bool", Flag: "-x", Choices: []string{"y"}}}, []AgentConfig{{Name: "a", Bin: "a"}}),
		"unknown option":    mk([]OptionDef{{Key: "x", Type: "bool", Flag: "-x"}}, []AgentConfig{{Name: "a", Bin: "a", Options: map[string]any{"nope": true}}}),
		"enum default bad":  mk([]OptionDef{{Key: "x", Type: "enum", Choices: []string{"hi"}, Flag: "-x"}}, []AgentConfig{{Name: "a", Bin: "a", Options: map[string]any{"x": "bad"}}}),
		"env-secret clash":  mk([]OptionDef{{Key: "x", Type: "enum", Choices: []string{"hi"}, Env: "TOK"}}, []AgentConfig{{Name: "a", Bin: "a", Options: map[string]any{"x": "hi"}, Secrets: []string{"TOK"}}}),
	}
	for name, bc := range bad {
		if err := bc.Validate(); err == nil {
			t.Errorf("%s: should be rejected", name)
		}
	}
}

func TestOptionDefAppliesTo(t *testing.T) {
	tests := []struct {
		name string
		bins []string
		bin  string
		want bool
	}{
		{"empty bins applies to all", nil, "bash", true},
		{"exact match", []string{"claude"}, "claude", true},
		{"basename match", []string{"claude"}, "/usr/local/bin/claude", true},
		{"no match", []string{"claude"}, "bash", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := OptionDef{Bins: tt.bins}
			if got := o.AppliesTo(tt.bin); got != tt.want {
				t.Errorf("AppliesTo(%q) bins=%v = %v, want %v", tt.bin, tt.bins, got, tt.want)
			}
		})
	}
}

func TestConfigExampleValidates(t *testing.T) {
	c, err := Load("../../config.example.json")
	if err != nil {
		t.Fatalf("load config.example.json: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("config.example.json must validate: %v", err)
	}
}

func TestConfigRepliesEnabledParse(t *testing.T) {
	var on, absent Config
	if err := json.Unmarshal([]byte(`{"replies_enabled":true}`), &on); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !on.RepliesEnabled {
		t.Error("replies_enabled:true not parsed")
	}
	if err := json.Unmarshal([]byte(`{}`), &absent); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if absent.RepliesEnabled {
		t.Error("absent replies_enabled must default to false")
	}
}

func TestConfigEnvFieldsParse(t *testing.T) {
	var c Config
	if err := json.Unmarshal([]byte(`{"env_label":"PROD","env_color":"#b45309"}`), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.EnvLabel != "PROD" || c.EnvColor != "#b45309" {
		t.Fatalf("got %q/%q, want PROD/#b45309", c.EnvLabel, c.EnvColor)
	}
}

func TestValidateProfileBinScoping(t *testing.T) {
	defs := map[string]OptionDef{
		"model": {Key: "model", Label: "Model", Type: OptEnum, Choices: []string{"claude-opus-5"}, Flag: "--model", Bins: []string{"claude"}},
	}
	if err := ValidateProfile(AgentConfig{Name: "b", Bin: "bash", Options: map[string]any{"model": "claude-opus-5"}}, defs); err == nil {
		t.Fatal("expected error: bash may not set a claude-only option")
	}
	if err := ValidateProfile(AgentConfig{Name: "c", Bin: "claude", Options: map[string]any{"model": "claude-opus-5"}}, defs); err != nil {
		t.Fatalf("claude setting model should be valid, got %v", err)
	}
}

func TestUploadLimitDefaults(t *testing.T) {
	if got := (Config{}).UploadLimit(); got != 64<<20 {
		t.Errorf("zero: got %d, want %d", got, int64(64<<20))
	}
	if got := (Config{MaxUploadBytes: -5}).UploadLimit(); got != 64<<20 {
		t.Errorf("negative: got %d, want default", got)
	}
	if got := (Config{MaxUploadBytes: 123}).UploadLimit(); got != 123 {
		t.Errorf("set: got %d, want 123", got)
	}
}

func TestArchiveLimitsDefaults(t *testing.T) {
	b, n := (Config{}).ArchiveLimits()
	if b != 512<<20 || n != 20000 {
		t.Errorf("defaults = %d, %d; want %d, 20000", b, n, int64(512<<20))
	}
	b, n = (Config{MaxArchiveBytes: 7, MaxArchiveFiles: 9}).ArchiveLimits()
	if b != 7 || n != 9 {
		t.Errorf("set = %d, %d; want 7, 9", b, n)
	}
}

func TestRuntimePolicyDefaultsToBox(t *testing.T) {
	if got := (AgentConfig{Name: "a", Bin: "b"}).RuntimePolicy(); got != RuntimeBox {
		t.Fatalf("default runtime = %q, want %q", got, RuntimeBox)
	}
	if got := (AgentConfig{Name: "a", Bin: "b", Runtime: RuntimeHost}).RuntimePolicy(); got != RuntimeHost {
		t.Fatalf("explicit runtime = %q, want %q", got, RuntimeHost)
	}
}

func TestValidateRejectsUnknownRuntime(t *testing.T) {
	c := Config{BindAddr: "127.0.0.1:1", Agents: []AgentConfig{{Name: "a", Bin: "b", Runtime: "vm"}}}
	if err := c.Validate(); err == nil {
		t.Fatal("expected validation error for runtime \"vm\"")
	}
}

func TestValidateProfileRejectsUnknownRuntime(t *testing.T) {
	err := ValidateProfile(AgentConfig{Name: "p", Bin: "b", Runtime: "vm"}, map[string]OptionDef{})
	if err == nil {
		t.Fatal("profiles must not bypass runtime validation")
	}
}

func TestBoxIdleTTLDefaults(t *testing.T) {
	if got := (Config{}).BoxIdleTTL(); got != 15*time.Minute {
		t.Fatalf("default ttl = %v, want 15m", got)
	}
	if got := (Config{BoxIdleTTLSeconds: 60}).BoxIdleTTL(); got != time.Minute {
		t.Fatalf("ttl = %v, want 1m", got)
	}
}

// The auto-detected shell profile is generated, not configured, so no per-agent
// setting can reach it. Without an instance-level default it is always boxed,
// even on an instance whose every configured agent is host — which made the
// startup warning fire on a correct configuration.
func TestRuntimeDefaultAppliesToAgentsThatDoNotSetOne(t *testing.T) {
	c := Config{DefaultRuntime: RuntimeHost}
	if got := c.RuntimeFor(AgentConfig{Name: "generated"}); got != RuntimeHost {
		t.Fatalf("an agent with no runtime must take the instance default, got %q", got)
	}
	if got := c.RuntimeFor(AgentConfig{Name: "pinned", Runtime: RuntimeBox}); got != RuntimeBox {
		t.Fatalf("an agent that sets its own runtime must keep it, got %q", got)
	}
}

// Unset must still mean contained: the safe tier is the one you get by omission.
func TestRuntimeDefaultIsBoxWhenUnset(t *testing.T) {
	if got := (Config{}).RuntimeFor(AgentConfig{Name: "a"}); got != RuntimeBox {
		t.Fatalf("with no instance default the tier must be box, got %q", got)
	}
}

func TestValidateRejectsUnknownDefaultRuntime(t *testing.T) {
	c := Config{BindAddr: "127.0.0.1:1", DefaultRuntime: "vm",
		Agents: []AgentConfig{{Name: "a", Bin: "b"}}}
	if err := c.Validate(); err == nil {
		t.Fatal("expected validation error for default_runtime \"vm\"")
	}
}

func TestValidateBoxLimits(t *testing.T) {
	agents := `"agents":[{"name":"a","bin":"b"}]`
	p := writeTemp(t, `{"bind_addr":"127.0.0.1:1","box_pids_limit":2048,"box_tmp_size":"2g","box_memory":"2048m",`+agents+`}`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.BoxPidsLimit != 2048 || c.BoxTmpSize != "2g" || c.BoxMemory != "2048m" {
		t.Fatalf("limits not parsed: %+v", c)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("valid limits rejected: %v", err)
	}
	for _, tc := range []struct{ cfg, key string }{
		{`"box_tmp_size":"2g,exec"`, "box_tmp_size"},
		{`"box_pids_limit":10`, "box_pids_limit"},
		{`"box_memory":"1.5g"`, "box_memory"},
		{`"box_memory":"512m"`, "box_memory"},
	} {
		c, err := Load(writeTemp(t, `{"bind_addr":"127.0.0.1:1",`+tc.cfg+`,`+agents+`}`))
		if err != nil {
			t.Fatal(err)
		}
		err = c.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.key) {
			t.Errorf("%s: want an error naming %s, got %v", tc.cfg, tc.key, err)
		}
	}
}

// config.example.json is what docs/MACOS.md points readers at: it must load and
// show allowed_hosts.
func TestExampleConfigLoadsAndShowsAllowedHosts(t *testing.T) {
	c, err := Load("../../config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.AllowedHosts) == 0 {
		t.Fatal("config.example.json must carry an allowed_hosts example")
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("example config must validate: %v", err)
	}
}

func TestBoxVMSizeDefaults(t *testing.T) {
	const gib = 1 << 30
	for _, c := range []struct {
		name     string
		cfg      Config
		hostCPUs int
		hostMem  uint64
		cpus, gb int
	}{
		{"all cores, half the RAM", Config{}, 10, 32 * gib, 10, 16},
		{"half rounds down to whole GiB", Config{}, 8, 23 * gib, 8, 11},
		{"small mac keeps the old 4 GiB floor", Config{}, 4, 8 * gib, 4, 4},
		{"tiny mac still gets the floor", Config{}, 2, 4 * gib, 2, 4},
		{"host facts unknown: old 4/4", Config{}, 0, 0, 4, 4},
		{"explicit values win", Config{BoxVMCPUs: 2, BoxVMMemoryGiB: 6}, 10, 32 * gib, 2, 6},
		{"explicit cpus, default memory", Config{BoxVMCPUs: 2}, 10, 32 * gib, 2, 16},
		{"explicit memory, default cpus", Config{BoxVMMemoryGiB: 6}, 10, 32 * gib, 10, 6},
		{"explicit may undercut the floor", Config{BoxVMMemoryGiB: 2}, 10, 32 * gib, 10, 2},
	} {
		if cpus, gb := c.cfg.BoxVMSize(c.hostCPUs, c.hostMem); cpus != c.cpus || gb != c.gb {
			t.Errorf("%s: got %d/%d, want %d/%d", c.name, cpus, gb, c.cpus, c.gb)
		}
	}
}

func TestValidateBoxCPUs(t *testing.T) {
	agents := `"agents":[{"name":"a","bin":"b"}]`
	c, err := Load(writeTemp(t, `{"bind_addr":"127.0.0.1:1","box_cpus":1.5,`+agents+`}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.BoxCPUs != 1.5 {
		t.Fatalf("box_cpus not parsed: %v", c.BoxCPUs)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("valid box_cpus rejected: %v", err)
	}
	for _, bad := range []string{"-1", "0.001", "1.234", "2000"} {
		c, err := Load(writeTemp(t, `{"bind_addr":"127.0.0.1:1","box_cpus":`+bad+`,`+agents+`}`))
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "box_cpus") {
			t.Errorf("box_cpus %s: want an error naming box_cpus, got %v", bad, err)
		}
	}
	c, err = Load(writeTemp(t, `{"bind_addr":"127.0.0.1:1","box_memory":"max",`+agents+`}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf(`box_memory "max" rejected: %v`, err)
	}
}

func TestLoad_BoxVMFields(t *testing.T) {
	p := writeTemp(t, `{"colima_bin":"/opt/homebrew/bin/colima","box_vm_cpus":2,"box_vm_memory_gib":6,"agents":[{"name":"c","bin":"claude"}]}`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.ColimaBin != "/opt/homebrew/bin/colima" || c.BoxVMCPUs != 2 || c.BoxVMMemoryGiB != 6 {
		t.Fatalf("vm fields = %+v", c)
	}
}

func TestBoxImageOrDefault(t *testing.T) {
	if got := (Config{}).BoxImageOrDefault(); got != "culiops/agent-box:base" {
		t.Fatalf("default = %q", got)
	}
	if got := (Config{BoxImage: "me/img:1"}).BoxImageOrDefault(); got != "me/img:1" {
		t.Fatalf("override = %q", got)
	}
}

// An image that starts with "-" is parsed by docker as a flag in
// `docker run … <image>`, `image inspect <image>` and `--entrypoint node <image>`.
func TestValidate_BoxImage(t *testing.T) {
	base := func() Config {
		return Config{BindAddr: "127.0.0.1:8080", Agents: []AgentConfig{{Name: "c", Bin: "claude"}}}
	}
	for _, ok := range []string{"", "culiops/agent-box:base", "registry.example.com:5000/a/b_c-d.e:1.2", "img@sha256:abc123"} {
		c := base()
		c.BoxImage = ok
		if err := c.Validate(); err != nil {
			t.Fatalf("box_image %q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"-v", "--privileged", "a b", "a,b", "a\nb", "img;rm", "img$x", "img\"x", "\u00efmg", "a=b"} {
		c := base()
		c.BoxImage = bad
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "box image") {
			t.Fatalf("box_image %q must be refused for its image, got %v", bad, err)
		}
	}
}

func TestBoxInstanceDefaultsToDefault(t *testing.T) {
	if got := (Config{}).BoxInstance(); got != "default" {
		t.Fatalf("got %q", got)
	}
	if got := (Config{EnvLabel: "PROD"}).BoxInstance(); got != "PROD" {
		t.Fatalf("got %q", got)
	}
}

const boxImageRefusal = `profile "p": box.image is not per-profile; every box runs the instance-wide box_image (set it in the config)`

func TestValidateProfileRefusesBoxImage(t *testing.T) {
	err := ValidateProfile(AgentConfig{Name: "p", Bin: "bash", Box: BoxConfig{Image: "x:1"}}, nil)
	if err == nil || err.Error() != boxImageRefusal {
		t.Fatalf("got %v, want %q", err, boxImageRefusal)
	}
	if err := ValidateProfile(AgentConfig{Name: "p", Bin: "bash", Box: BoxConfig{Image: ""}}, nil); err != nil {
		t.Fatalf("an empty box.image must stay valid, got %v", err)
	}
}

// A config-file agent carrying a legacy box.image is ignored (and logged by the
// profile manager), so even an unusable ref no longer fails the whole config.
func TestValidateIgnoresAgentBoxImage(t *testing.T) {
	c := Config{BindAddr: "127.0.0.1:8080", Agents: []AgentConfig{{Name: "a", Bin: "a", Box: BoxConfig{Image: "--privileged"}}}}
	if err := c.Validate(); err != nil {
		t.Fatalf("legacy agent box.image must not fail Validate, got %v", err)
	}
}

func TestResolvedProfilesPath(t *testing.T) {
	if got := (Config{}).ResolvedProfilesPath("/srv/ab"); got != "/srv/ab/profiles.json" {
		t.Errorf("default: got %q", got)
	}
	if got := (Config{ProfilesPath: "/x/p.json"}).ResolvedProfilesPath("/srv/ab"); got != "/x/p.json" {
		t.Errorf("explicit: got %q", got)
	}
}

// writeCert writes a self-signed certificate (a CA when isCA) as a PEM file.
func writeCert(t *testing.T, isCA bool) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		BasicConstraintsValid: true,
		IsCA:                  isCA,
	}
	if isCA {
		tmpl.KeyUsage = x509.KeyUsageCertSign
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestValidate_ExtraCACerts(t *testing.T) {
	base := func() Config {
		return Config{BindAddr: "127.0.0.1:8080", Agents: []AgentConfig{{Name: "c", Bin: "claude"}}}
	}
	c := base()
	if err := c.Validate(); err != nil {
		t.Fatalf("absent extra_ca_certs must stay valid, got %v", err)
	}
	c.ExtraCACerts = []string{writeCert(t, true)}
	if err := c.Validate(); err != nil {
		t.Fatalf("a valid CA refused: %v", err)
	}
	set, err := c.ExtraCA()
	if err != nil || len(set.CAs) != 1 {
		t.Fatalf("ExtraCA() = %d CAs, %v; want 1, nil", len(set.CAs), err)
	}
	c.ExtraCACerts = []string{writeCert(t, false)}
	if err := c.Validate(); err == nil || !strings.HasPrefix(err.Error(), "config: extra_ca_certs:") {
		t.Fatalf("a non-CA cert must fail with the config: extra_ca_certs: prefix, got %v", err)
	}
}

func TestExtraCACertsParses(t *testing.T) {
	var c Config
	if err := json.Unmarshal([]byte(`{"extra_ca_certs":["/a.pem","/b.pem"]}`), &c); err != nil {
		t.Fatal(err)
	}
	if len(c.ExtraCACerts) != 2 || c.ExtraCACerts[0] != "/a.pem" {
		t.Fatalf("extra_ca_certs not parsed: %v", c.ExtraCACerts)
	}
}

func TestTokenEnvDeniedCoversCAEnv(t *testing.T) {
	for _, n := range extraca.DeniedEnvNames {
		if !TokenEnvDenied(n) {
			t.Errorf("TokenEnvDenied(%q) = false; a static token must not set a CA env var", n)
		}
	}
}

func TestValidateProfileRefusesCAEnv(t *testing.T) {
	if !slices.Contains(extraca.DeniedEnvNames, "SSL_CERT_DIR") || !slices.Contains(extraca.DeniedEnvNames, "GIT_SSL_CAPATH") {
		t.Fatalf("DeniedEnvNames must add the directory-based trust paths: %v", extraca.DeniedEnvNames)
	}
	for _, n := range extraca.DeniedEnvNames {
		err := ValidateProfile(AgentConfig{Name: "p", Bin: "bash", Env: map[string]string{n: "/x"}}, nil)
		if err == nil || !strings.Contains(err.Error(), n) || !strings.Contains(err.Error(), "extra_ca_certs") {
			t.Errorf("env %s: got %v, want a refusal naming the variable and extra_ca_certs", n, err)
		}
	}
	if err := ValidateProfile(AgentConfig{Name: "p", Bin: "bash", Env: map[string]string{"FOO": "1"}}, nil); err != nil {
		t.Fatalf("an unrelated env var must stay valid, got %v", err)
	}
}

// A stored profile that already carries a CA env name must still boot: only
// ValidateProfile (on save) refuses it, Config.Validate does not look at
// stored profiles, and the value is dropped at spawn.
func TestValidateKeepsConfigAgentWithCAEnvBootable(t *testing.T) {
	c := Config{BindAddr: "127.0.0.1:8080", Agents: []AgentConfig{{Name: "a", Bin: "a", Env: map[string]string{"NODE_EXTRA_CA_CERTS": "/x"}}}}
	if err := c.Validate(); err != nil {
		t.Fatalf("an existing agent with a CA env name must still validate at boot, got %v", err)
	}
}

// A config file replaces the built-in defaults, so a file that only sets
// extra_ca_certs has no agents. The error says why, not just what.
func TestValidate_NoAgentsExplainsFileReplacesDefaults(t *testing.T) {
	err := Config{BindAddr: "127.0.0.1:8899"}.Validate()
	if err == nil {
		t.Fatal("no agents must be refused")
	}
	for _, want := range []string{"config: at least one agent required", "a config file replaces the built-in defaults"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

// audit_egress_retention_days is a pointer on purpose: unset → 90, explicit 0 →
// keep forever (every other int key reads 0 as "default").
func TestEgressRetentionDays(t *testing.T) {
	iptr := func(n int) *int { return &n }
	if got := (Config{}).EgressRetentionDays(); got != 90 {
		t.Errorf("unset = %d, want 90", got)
	}
	if got := (Config{AuditEgressRetentionDays: iptr(0)}).EgressRetentionDays(); got != 0 {
		t.Errorf("explicit 0 = %d, want 0 (keep forever)", got)
	}
	if got := (Config{AuditEgressRetentionDays: iptr(30)}).EgressRetentionDays(); got != 30 {
		t.Errorf("30 = %d", got)
	}
	base := Config{BindAddr: "127.0.0.1:8080", Agents: []AgentConfig{{Name: "a", Bin: "a"}}}
	for _, ok := range []int{0, 1, 36500} {
		c := base
		c.AuditEgressRetentionDays = iptr(ok)
		if err := c.Validate(); err != nil {
			t.Errorf("%d should validate: %v", ok, err)
		}
	}
	for _, bad := range []int{-1, 40000} {
		c := base
		c.AuditEgressRetentionDays = iptr(bad)
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), "audit_egress_retention_days") {
			t.Errorf("%d: err = %v, want audit_egress_retention_days out of range", bad, err)
		}
	}
	// JSON: absent vs explicit 0 survive a round trip through Load.
	c, err := Load(writeTemp(t, `{"bind_addr":"127.0.0.1:8080","agents":[{"name":"a","bin":"a"}],"audit_egress_retention_days":0}`))
	if err != nil || c.AuditEgressRetentionDays == nil || c.EgressRetentionDays() != 0 {
		t.Errorf("explicit 0 from JSON: %+v, %v", c.AuditEgressRetentionDays, err)
	}
}

// OfferedBy (#35, review A-1): an option a profile sets a default for is always
// offered. An unset one is offered only when it opts in (offer_unset), which
// defaults on for model and effort alone: any other enum can be a capability
// (--permission-mode bypassPermissions), and a bool can never opt in.
func TestOptionOfferedBy(t *testing.T) {
	yes, no := true, false
	bare := AgentConfig{Name: "p", Bin: "claude"}
	set := AgentConfig{Name: "q", Bin: "claude", Options: map[string]any{"mode": "default"}}
	cases := []struct {
		def  OptionDef
		a    AgentConfig
		want bool
	}{
		{OptionDef{Key: "model", Type: OptEnum, Bins: []string{"claude"}}, bare, true},
		{OptionDef{Key: "effort", Type: OptEnum, Bins: []string{"claude"}}, bare, true},
		{OptionDef{Key: "effortLevel", Type: OptEnum}, bare, true},
		{OptionDef{Key: "model", Type: OptEnum, Bins: []string{"claude"}}, AgentConfig{Bin: "bash"}, false},
		{OptionDef{Key: "model", Type: OptEnum, OfferUnset: &no}, bare, false},
		{OptionDef{Key: "mode", Type: OptEnum}, bare, false}, // an unknown enum stays opt-in
		{OptionDef{Key: "mode", Type: OptEnum, OfferUnset: &yes}, bare, true},
		{OptionDef{Key: "mode", Type: OptEnum}, set, true},
		{OptionDef{Key: "skip_permissions", Type: OptBool}, bare, false},
		{OptionDef{Key: "skip_permissions", Type: OptBool, OfferUnset: &yes}, bare, false}, // never for a bool
	}
	for i, c := range cases {
		if got := c.def.OfferedBy(c.a); got != c.want {
			t.Errorf("case %d (%s on %s): OfferedBy = %v, want %v", i, c.def.Key, c.a.Bin, got, c.want)
		}
	}
}
