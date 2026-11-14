package config

import (
	"encoding/json"
	"fmt"
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

func elevBinding() *AWSBinding {
	b := goodBinding()
	b.Elevate = &AWSElevate{RoleARN: "arn:aws:iam::123456789012:role/agentbox-agent-write"}
	return b
}

func TestElevateValidate(t *testing.T) {
	if err := elevBinding().Validate(); err != nil {
		t.Fatalf("good elevate refused: %v", err)
	}
	cases := map[string]func(*AWSBinding){
		"bad arn":         func(b *AWSBinding) { b.Elevate.RoleARN = "arn:aws:iam::123456789012:user/x" },
		"other account":   func(b *AWSBinding) { b.Elevate.RoleARN = "arn:aws:iam::999999999999:role/agentbox-agent-write" },
		"other partition": func(b *AWSBinding) { b.Elevate.RoleARN = "arn:aws-cn:iam::123456789012:role/agentbox-agent-write" },
		"same role":       func(b *AWSBinding) { b.Elevate.RoleARN = b.RoleARN },
		"max 45":          func(b *AWSBinding) { b.Elevate.MaxMinutes = 45 },
		"max 90":          func(b *AWSBinding) { b.Elevate.MaxMinutes = 90 },
		"prod no label":   func(b *AWSBinding) { b.Prod, b.Label = true, "" },
		"prefix star":     func(b *AWSBinding) { b.Elevate.BoxBucketPrefix = "*" },
		"prefix wildcard": func(b *AWSBinding) { b.Elevate.BoxBucketPrefix = "culibox-*" },
		"prefix no dash":  func(b *AWSBinding) { b.Elevate.BoxBucketPrefix = "culibox" },
		"prefix too long": func(b *AWSBinding) { b.Elevate.BoxBucketPrefix = strings.Repeat("c", 40) + "-" },
		"prefix quote":    func(b *AWSBinding) { b.Elevate.BoxBucketPrefix = `x"],"Effect":"Allow-` },
	}
	for name, mut := range cases {
		b := elevBinding()
		mut(b)
		if err := b.Validate(); err == nil || !strings.Contains(err.Error(), "aws.") {
			t.Errorf("%s: want an aws.* error, got %v", name, err)
		}
	}
	for _, m := range []int{0, 30, 60} {
		b := elevBinding()
		b.Elevate.MaxMinutes = m
		if err := b.Validate(); err != nil {
			t.Errorf("max_minutes %d refused: %v", m, err)
		}
	}
	b := elevBinding()
	b.Prod, b.Label = true, "prod-acct"
	if err := b.Validate(); err != nil {
		t.Errorf("prod with a label refused: %v", err)
	}
	b = elevBinding()
	b.Elevate.BoxBucketPrefix = "culibox-"
	if err := b.Validate(); err != nil {
		t.Errorf("box_bucket_prefix culibox- refused: %v", err)
	}
	if err := elevBinding().Validate(); err != nil || elevBinding().Elevate.BoxBucketPrefix != "" {
		t.Errorf("box_bucket_prefix must be optional and off by default: %v", err)
	}
}

func TestElevateScopeCarriesTheElevatedPolicyInputs(t *testing.T) {
	e := AWSElevate{RoleARN: "arn:aws:iam::123456789012:role/w", AllowIAM: true, BoxBucketPrefix: "culibox-"}
	if got := e.Scope(); !got.AllowIAM || got.BoxBucketPrefix != "culibox-" {
		t.Fatalf("Scope %+v", got)
	}
}

func TestElevateMaxDefaultsToSixty(t *testing.T) {
	if (AWSElevate{}).Max() != 60 || (AWSElevate{MaxMinutes: 30}).Max() != 30 {
		t.Fatal("Max")
	}
}

// elevate is part of the scope, so part of the hash and the box key. The
// pre-M3b golden values (TestHashGoldenWithoutEgressAllow) must still hold.
func TestHashCoversElevate(t *testing.T) {
	base := goodBinding().Hash()
	if base != "e7e58e9b264b" {
		t.Fatalf("pre-M3b hash moved: %s", base)
	}
	seen := map[string]string{"none": base}
	for name, e := range map[string]*AWSElevate{
		"elevate":    {RoleARN: "arn:aws:iam::123456789012:role/agentbox-agent-write"},
		"other role": {RoleARN: "arn:aws:iam::123456789012:role/other-write"},
		"max 30":     {RoleARN: "arn:aws:iam::123456789012:role/agentbox-agent-write", MaxMinutes: 30},
		"allow iam":  {RoleARN: "arn:aws:iam::123456789012:role/agentbox-agent-write", AllowIAM: true},
		"box prefix": {RoleARN: "arn:aws:iam::123456789012:role/agentbox-agent-write", BoxBucketPrefix: "culibox-"},
	} {
		b := goodBinding()
		b.Elevate = e
		h := b.Hash()
		for other, oh := range seen {
			if oh == h {
				t.Errorf("%s hashes like %s", name, other)
			}
		}
		seen[name] = h
	}
	a, b := elevBinding(), elevBinding()
	b.Elevate.MaxMinutes = 60
	if a.Hash() != b.Hash() {
		t.Error("max_minutes 0 and 60 mean the same window and must share a box key")
	}
	if a.Elevate.MaxMinutes != 0 {
		t.Error("Hash must not modify the binding")
	}
	// An elevate block without the #211 prefix must hash as it did before
	// the field existed: the key is left out of the hashed JSON entirely.
	if j, _ := json.Marshal(elevBinding().normalized()); strings.Contains(string(j), "box_bucket_prefix") {
		t.Errorf("an unset box_bucket_prefix reached the hash input: %s", j)
	}
}

func TestCloneIsDeep(t *testing.T) {
	a := elevBinding()
	a.EgressAllow = []string{"registry.npmjs.org"}
	c := a.Clone()
	c.S3Read[0], c.EgressAllow[0], c.Elevate.RoleARN = "x", "y", "z"
	if a.S3Read[0] == "x" || a.EgressAllow[0] == "y" || a.Elevate.RoleARN == "z" {
		t.Fatal("Clone shares memory with the original")
	}
	if (&AWSBinding{}).Clone().Elevate != nil {
		t.Fatal("a nil elevate must stay nil")
	}
}

func TestElevateJSONRoundTrip(t *testing.T) {
	in := `{"source_profile":"sandbox-admin","role_arn":"arn:aws:iam::123456789012:role/agentbox-agent","region":"ap-southeast-1","elevate":{"role_arn":"arn:aws:iam::123456789012:role/agentbox-agent-write","max_minutes":30}}`
	var b AWSBinding
	if err := json.Unmarshal([]byte(in), &b); err != nil || b.Elevate == nil || b.Elevate.MaxMinutes != 30 || b.Elevate.AllowIAM {
		t.Fatalf("decode: %+v %v", b, err)
	}
	out, _ := json.Marshal(goodBinding())
	if strings.Contains(string(out), "elevate") {
		t.Fatalf("a binding without elevate must not marshal the key: %s", out)
	}
}

func ssoBinding() AWSBinding {
	return AWSBinding{SourceProfile: "co-ro", Region: "ap-southeast-1", Mode: AWSModeSSODirect}
}

func TestSSODirectValidates(t *testing.T) {
	b := ssoBinding()
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	if !b.SSODirect() {
		t.Fatal("SSODirect() must be true for mode sso_direct")
	}
	if (AWSBinding{}).SSODirect() {
		t.Fatal("SSODirect() must be false for the default mode")
	}
}

func TestSSODirectRefuses(t *testing.T) {
	cases := []struct {
		name string
		mod  func(*AWSBinding)
		want string
	}{
		{"role_arn", func(b *AWSBinding) { b.RoleARN = "arn:aws:iam::123456789012:role/x" }, "role_arn"},
		{"elevate", func(b *AWSBinding) { b.Elevate = &AWSElevate{RoleARN: "arn:aws:iam::123456789012:role/w"} }, "elevate"},
		{"s3_read", func(b *AWSBinding) { b.S3Read = []string{"bucket"} }, "s3_read"},
		{"upper-case mode", func(b *AWSBinding) { b.Mode = "SSO_DIRECT" }, "mode"},
		{"bogus mode", func(b *AWSBinding) { b.Mode = "bogus" }, "mode"},
		{"empty region", func(b *AWSBinding) { b.Region = "" }, "region"},
		{"bad profile", func(b *AWSBinding) { b.SourceProfile = "bad profile" }, "source_profile"},
		{"long label", func(b *AWSBinding) { b.Label = strings.Repeat("x", 41) }, "label"},
		{"bad egress", func(b *AWSBinding) { b.EgressAllow = []string{"not a host"} }, "egress_allow"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := ssoBinding()
			c.mod(&b)
			err := b.Validate()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want an error mentioning %q, got %v", c.want, err)
			}
			if c.want != "mode" && c.want != "region" && c.want != "source_profile" && c.want != "label" && c.want != "egress_allow" &&
				!strings.Contains(err.Error(), "not used in sso_direct mode") {
				t.Fatalf("error must say the field is not used in sso_direct mode, got %v", err)
			}
		})
	}
}

func TestAssumeRoleStillNeedsRoleARN(t *testing.T) {
	b := AWSBinding{SourceProfile: "co-ro", Region: "ap-southeast-1"}
	err := b.Validate()
	if err == nil || !strings.Contains(err.Error(), "role_arn") {
		t.Fatalf("an assume_role binding without role_arn must fail, got %v", err)
	}
}

func TestModeKeepsOldHashes(t *testing.T) {
	b := AWSBinding{SourceProfile: "co-ro", Region: "ap-southeast-1",
		RoleARN: "arn:aws:iam::123456789012:role/agentbox-agent", S3Read: []string{"b", "a"}, Label: "x", Prod: true}
	// Golden computed on the code before Mode existed.
	if got := b.Hash(); got != "f13569b44e35" {
		t.Fatalf("a mode-less binding's hash changed: %s", got)
	}
	plain := AWSBinding{SourceProfile: "co-ro", Region: "ap-southeast-1"}
	if got := plain.Hash(); got != "7511859a28d4" {
		t.Fatalf("a mode-less binding's hash changed: %s", got)
	}
	sso := plain
	sso.Mode = AWSModeSSODirect
	if sso.Hash() == plain.Hash() {
		t.Fatal("sso_direct and assume_role bindings with otherwise equal fields must hash differently")
	}
}

// The config-file path must enforce the same egress policy rules as
// ValidateProfile: an explicit box runtime, valid patterns, a bounded list.
func TestConfigValidateChecksAgentEgressPolicy(t *testing.T) {
	cfg := func(a AgentConfig) Config {
		return Config{BindAddr: "127.0.0.1:0", PasswordEnv: "X", Agents: []AgentConfig{a}}
	}
	good := AgentConfig{Name: "c", Bin: "claude", Runtime: RuntimeBox, EgressAllow: []string{"pkg.example.com"}, EgressOpen: true}
	if err := cfg(good).Validate(); err != nil {
		t.Fatal(err)
	}
	host := good
	host.Runtime = RuntimeHost
	badURL := good
	badURL.EgressAllow = []string{"https://x"}
	comma := good
	comma.EgressAllow = []string{"a.example.com,b.example.com"}
	many := good
	many.EgressAllow = make([]string, maxEgressAllow+1)
	for i := range many.EgressAllow {
		many.EgressAllow[i] = fmt.Sprintf("h%d.example.com", i)
	}
	for name, a := range map[string]AgentConfig{"host runtime": host, "url entry": badURL, "comma entry": comma, "too many": many} {
		if err := cfg(a).Validate(); err == nil || !strings.Contains(err.Error(), `agent "c"`) {
			t.Errorf("%s accepted in config file: %v", name, err)
		}
	}
}
