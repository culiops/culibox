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
