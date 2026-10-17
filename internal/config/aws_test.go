package config

import (
	"strings"
	"testing"
)

func goodBinding() *AWSBinding {
	return &AWSBinding{SourceProfile: "sandbox-admin", RoleARN: "arn:aws:iam::123456789012:role/agentbox-agent",
		Region: "ap-southeast-1", S3Read: []string{"example-tfstate"}, Label: "sandbox"}
}

func TestProfileWithAWSBindingMustBeBoxed(t *testing.T) {
	p := AgentConfig{Name: "c", Bin: "claude", Runtime: RuntimeHost, AWS: goodBinding()}
	err := ValidateProfile(p, nil)
	if err == nil || !strings.Contains(err.Error(), "box") {
		t.Fatalf("host-tier binding must be refused, got %v", err)
	}
	// An empty runtime means "the instance default", which may be host.
	p.Runtime = ""
	err = ValidateProfile(p, nil)
	if err == nil || !strings.Contains(err.Error(), "runtime: box") {
		t.Fatalf("empty runtime with a binding must be refused, got %v", err)
	}
	p.Runtime = RuntimeBox
	if err := ValidateProfile(p, nil); err != nil {
		t.Fatal(err)
	}
}

func TestProfileRejectsInvalidAWSBinding(t *testing.T) {
	p := AgentConfig{Name: "c", Bin: "claude", Runtime: RuntimeBox, AWS: goodBinding()}
	p.AWS.Region = "nope"
	err := ValidateProfile(p, nil)
	if err == nil || !strings.Contains(err.Error(), `profile "c"`) {
		t.Fatalf("invalid binding must be refused with the profile name, got %v", err)
	}
}

func TestConfigValidateChecksAgentAWSBinding(t *testing.T) {
	cfg := func(a AgentConfig) Config {
		return Config{BindAddr: "127.0.0.1:0", PasswordEnv: "X", Agents: []AgentConfig{a}}
	}
	good := AgentConfig{Name: "c", Bin: "claude", Runtime: RuntimeBox, AWS: goodBinding()}
	if err := cfg(good).Validate(); err != nil {
		t.Fatal(err)
	}
	hostTier := good
	hostTier.Runtime = RuntimeHost
	if err := cfg(hostTier).Validate(); err == nil || !strings.Contains(err.Error(), `agent "c"`) {
		t.Fatalf("host-tier binding in config.json must be refused, got %v", err)
	}
	implicit := good
	implicit.Runtime = ""
	if err := cfg(implicit).Validate(); err == nil {
		t.Fatal("empty runtime with a binding in config.json must be refused")
	}
	bad := good
	bad.AWS = goodBinding()
	bad.AWS.RoleARN = "arn:aws:iam::1:role/x"
	if err := cfg(bad).Validate(); err == nil {
		t.Fatal("invalid binding in config.json must be refused")
	}
}

func TestAWSBindingValidate(t *testing.T) {
	cases := map[string]func(b *AWSBinding){
		"no source profile":           func(b *AWSBinding) { b.SourceProfile = "" },
		"source profile with newline": func(b *AWSBinding) { b.SourceProfile = "a\nb" },
		"bad arn":                     func(b *AWSBinding) { b.RoleARN = "arn:aws:iam::1:role/x" },
		"region injection":            func(b *AWSBinding) { b.Region = "ap-southeast-1\n[profile x]" },
		"wildcard bucket":             func(b *AWSBinding) { b.S3Read = []string{"logs/*"} },
		"label too long":              func(b *AWSBinding) { b.Label = strings.Repeat("x", 41) },
		"policy too large": func(b *AWSBinding) {
			b.S3Read = nil
			for i := 0; i < 60; i++ {
				b.S3Read = append(b.S3Read, "example-bucket-"+strings.Repeat("x", 10)+string(rune('a'+i%26))+string(rune('a'+i/26)))
			}
		},
	}
	for name, mut := range cases {
		b := goodBinding()
		mut(b)
		if b.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := goodBinding().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestAWSBindingHashIsOrderIndependentAndCoversEveryField(t *testing.T) {
	a, b := goodBinding(), goodBinding()
	a.S3Read = []string{"x-bucket", "y-bucket"}
	b.S3Read = []string{"y-bucket", "x-bucket"}
	if a.Hash() != b.Hash() || len(a.Hash()) != 12 {
		t.Fatal("hash must ignore allowlist order and be 12 hex chars")
	}
	if a.S3Read[0] != "x-bucket" || b.S3Read[0] != "y-bucket" {
		t.Fatal("Hash must not reorder the caller's slice")
	}
	base := a.Hash()
	mutations := map[string]func(*AWSBinding){
		"source_profile": func(x *AWSBinding) { x.SourceProfile = "other" },
		"role_arn":       func(x *AWSBinding) { x.RoleARN = "arn:aws:iam::123456789012:role/other" },
		"region":         func(x *AWSBinding) { x.Region = "us-east-1" },
		"s3_read":        func(x *AWSBinding) { x.S3Read = []string{"x-bucket"} },
		"label":          func(x *AWSBinding) { x.Label = "other" },
		"prod":           func(x *AWSBinding) { x.Prod = true },
	}
	for name, mut := range mutations {
		c := goodBinding()
		c.S3Read = []string{"x-bucket", "y-bucket"}
		mut(c)
		if c.Hash() == base {
			t.Errorf("hash must change with %s", name)
		}
	}
}

func TestBrokerPortDefault(t *testing.T) {
	if (Config{}).BrokerPortOrDefault() != 9071 || (Config{BrokerPort: 9100}).BrokerPortOrDefault() != 9100 {
		t.Fatal("broker port default")
	}
}

// BrokerPortOrDefault would turn a negative port into 9071 without a word, so
// Validate is where a bad broker_port has to be refused.
func TestValidateRefusesABadBrokerPort(t *testing.T) {
	for _, bad := range []int{-1, 65536, 100000} {
		c := validCfg()
		c.BrokerPort = bad
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "broker_port") {
			t.Errorf("broker_port %d: want a broker_port error, got %v", bad, err)
		}
	}
	for _, ok := range []int{0, 1, 9071, 65535} {
		c := validCfg()
		c.BrokerPort = ok
		if err := c.Validate(); err != nil {
			t.Errorf("broker_port %d refused: %v", ok, err)
		}
	}
}

func TestEgressAllowValidated(t *testing.T) {
	b := goodBinding()
	b.EgressAllow = []string{"registry.npmjs.org", "*.githubusercontent.com"}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]string{{"*.com"}, {"1.1.1.1"}, {"Example.com"}, {"a.com\nb"}, make([]string, 33)} {
		c := goodBinding()
		c.EgressAllow = bad
		if c.Validate() == nil {
			t.Errorf("accepted egress_allow %q", bad)
		}
	}
}

// A binding without egress_allow must hash byte-identically to the pre-egress
// code: a changed hash renames the box and orphans every existing bound box.
// The hex values were computed on the code before egress_allow existed.
func TestHashGoldenWithoutEgressAllow(t *testing.T) {
	a := goodBinding()
	b := goodBinding()
	b.S3Read = []string{"y-bucket", "x-bucket"}
	b.Prod = true
	if got := a.Hash(); got != "e7e58e9b264b" {
		t.Errorf("hash a = %s, want e7e58e9b264b", got)
	}
	if got := b.Hash(); got != "d9a3e8ba3e7b" {
		t.Errorf("hash b = %s, want d9a3e8ba3e7b", got)
	}
	a.EgressAllow = []string{}
	if got := a.Hash(); got != "e7e58e9b264b" {
		t.Errorf("empty egress_allow hash = %s, want e7e58e9b264b", got)
	}
}

// An empty egress_allow must hash exactly as before, so the M3a boxes keep
// their names; a non-empty one is a set (order-insensitive) and changes the hash.
func TestHashWithEgressAllow(t *testing.T) {
	b := goodBinding()
	before := b.Hash()
	b.EgressAllow = []string{}
	if b.Hash() != before {
		t.Fatal("empty egress_allow changed the hash")
	}
	b.EgressAllow = []string{"a.example.com", "b.example.com"}
	c := goodBinding()
	c.EgressAllow = []string{"b.example.com", "a.example.com"}
	if b.Hash() == before || b.Hash() != c.Hash() {
		t.Fatalf("hash: before %s, ab %s, ba %s", before, b.Hash(), c.Hash())
	}
	if c.EgressAllow[0] != "b.example.com" {
		t.Fatal("Hash must not reorder the caller's slice")
	}
}

func TestEgressPort(t *testing.T) {
	if (Config{}).EgressPortOrDefault() != 9072 || (Config{EgressPort: 9100}).EgressPortOrDefault() != 9100 {
		t.Fatal("egress_port default/override")
	}
	for _, bad := range []int{-1, 70000, 9071} {
		c := validCfg()
		c.EgressPort = bad
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "egress_port") {
			t.Errorf("egress_port %d: err = %v", bad, err)
		}
	}
	c := validCfg()
	c.BrokerPort, c.EgressPort = 9200, 9200
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "egress_port") {
		t.Errorf("equal ports: err = %v", err)
	}
	for _, ok := range []int{0, 1, 9072, 65535} {
		c := validCfg()
		c.EgressPort = ok
		if err := c.Validate(); err != nil {
			t.Errorf("egress_port %d refused: %v", ok, err)
		}
	}
}
