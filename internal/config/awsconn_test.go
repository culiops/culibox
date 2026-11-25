package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/awsprobe"
)

func goodConn(name string) AWSConnection {
	return AWSConnection{Name: name, AWSBinding: *goodBinding()}
}

func TestAWSConnectionValidate(t *testing.T) {
	if err := goodConn("acme-prod-read").Validate(); err != nil {
		t.Fatalf("acme-prod-read should be accepted: %v", err)
	}
	for _, n := range []string{"Acme", "-x", strings.Repeat("a", 64), "default", "agentbox", ""} {
		if err := goodConn(n).Validate(); err == nil {
			t.Errorf("name %q must be refused", n)
		}
	}
	for _, n := range []string{"default", "agentbox"} {
		err := ValidateAWSConnections([]AWSConnection{goodConn(n)})
		want := fmt.Sprintf("aws_connections[0]: name %q is reserved", n)
		if err == nil || err.Error() != want {
			t.Errorf("reserved %q: got %v, want %q", n, err, want)
		}
	}
	bad := goodConn("x")
	bad.RoleARN = "not-an-arn"
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "aws.") {
		t.Errorf("an invalid role ARN must give the AWSBinding error, got %v", err)
	}
	d := goodConn("x")
	d.Description = strings.Repeat("d", 200)
	if err := d.Validate(); err != nil {
		t.Errorf("200-char description is fine: %v", err)
	}
	d.Description = strings.Repeat("d", 201)
	if err := d.Validate(); err == nil || !strings.Contains(err.Error(), "description") {
		t.Errorf("201-char description must be refused, got %v", err)
	}
	d.Description = "two\nlines"
	if err := d.Validate(); err == nil {
		t.Error("a multi-line description must be refused")
	}
}

func TestAWSConnectionJSONFlat(t *testing.T) {
	in := `{"name":"a","source_profile":"p","role_arn":"arn:aws:iam::123456789012:role/r","region":"us-east-1"}`
	var c AWSConnection
	if err := json.Unmarshal([]byte(in), &c); err != nil {
		t.Fatal(err)
	}
	if c.Name != "a" || c.SourceProfile != "p" || c.Region != "us-east-1" {
		t.Fatalf("flattened fields not read: %+v", c)
	}
	out, _ := json.Marshal(c)
	if string(out) != in {
		t.Errorf("round trip = %s, want %s", out, in)
	}
}

func TestValidateAWSConnectionsUnique(t *testing.T) {
	if err := ValidateAWSConnections([]AWSConnection{goodConn("a"), goodConn("b")}); err != nil {
		t.Fatal(err)
	}
	err := ValidateAWSConnections([]AWSConnection{goodConn("a"), goodConn("a")})
	if err == nil || !strings.Contains(err.Error(), "aws_connections[1]") {
		t.Errorf("duplicate must be refused naming index 1, got %v", err)
	}
	bad := goodConn("b")
	bad.Region = "nope"
	err = ValidateAWSConnections([]AWSConnection{goodConn("a"), bad})
	if err == nil || !strings.HasPrefix(err.Error(), "aws_connections[1]: ") {
		t.Errorf("per-entry error must carry the index prefix, got %v", err)
	}
}

func TestValidateAWSConnBinding(t *testing.T) {
	ok := AgentConfig{Name: "p", Bin: "claude", Runtime: RuntimeBox, AWSConnections: []string{"a", "b"}}
	if err := validateAWSConnBinding(ok); err != nil {
		t.Fatal(err)
	}
	if err := ValidateAWSConnBinding(ok); err != nil {
		t.Fatalf("exported wrapper: %v", err)
	}
	if err := validateAWSConnBinding(AgentConfig{Runtime: ""}); err != nil {
		t.Errorf("no names is always fine: %v", err)
	}
	for _, rt := range []string{"", RuntimeHost} {
		a := ok
		a.Runtime = rt
		err := validateAWSConnBinding(a)
		if err == nil || !strings.Contains(err.Error(), "runtime: box") {
			t.Errorf("runtime %q must be refused, got %v", rt, err)
		}
		if err := ValidateAWSConnBinding(a); err == nil {
			t.Errorf("exported wrapper must refuse runtime %q", rt)
		}
	}
	many := ok
	many.AWSConnections = nil
	for i := 0; i < 17; i++ {
		many.AWSConnections = append(many.AWSConnections, fmt.Sprintf("c%d", i))
	}
	if err := validateAWSConnBinding(many); err == nil || !strings.Contains(err.Error(), "at most 16") {
		t.Errorf("17 names must be refused, got %v", err)
	}
	many.AWSConnections = many.AWSConnections[:16]
	if err := validateAWSConnBinding(many); err != nil {
		t.Errorf("16 names are fine: %v", err)
	}
	dup := ok
	dup.AWSConnections = []string{"a", "a"}
	if err := validateAWSConnBinding(dup); err == nil || !strings.Contains(err.Error(), "listed twice") {
		t.Errorf("duplicate must be refused, got %v", err)
	}
	badName := ok
	badName.AWSConnections = []string{"Not A Name"}
	if err := validateAWSConnBinding(badName); err == nil {
		t.Error("a malformed name must be refused")
	}
	both := ok
	both.AWS = goodBinding()
	err := validateAWSConnBinding(both)
	if err == nil || err.Error() != "use either the inline aws binding or aws_connections, not both" {
		t.Errorf("inline + connections: got %v", err)
	}
	// reached through the profile validator too
	if err := ValidateProfile(both, nil); err == nil {
		t.Error("ValidateProfile must refuse inline + connections")
	}
	hostRt := ok
	hostRt.Runtime = RuntimeHost
	if err := ValidateProfile(hostRt, nil); err == nil {
		t.Error("ValidateProfile must refuse connections on the host tier")
	}
}

func TestEmptyConnectionsIsLegacy(t *testing.T) {
	a := AgentConfig{Name: "p", Bin: "claude", Runtime: RuntimeBox, AWS: goodBinding(), AWSConnections: []string{}}
	if err := ValidateProfile(a, nil); err != nil {
		t.Fatalf("empty aws_connections next to inline aws is legacy: %v", err)
	}
	if err := validateAWSConnBinding(a); err != nil {
		t.Fatal(err)
	}
	if got := (Bindings{AWS: a.AWS, AWSConns: []AWSConnection{}}).Hash(); got != "e7e58e9b264b" {
		t.Errorf("hash = %s, want legacy e7e58e9b264b", got)
	}
}

func TestCheckAWSConnectionsUnknown(t *testing.T) {
	a := AgentConfig{Runtime: RuntimeBox, AWSConnections: []string{"a", "ghost"}}
	err := CheckAWSConnections(a, []AWSConnection{goodConn("a")})
	if err == nil || !strings.Contains(err.Error(), `"ghost"`) || !strings.Contains(err.Error(), "is not defined in aws_connections") {
		t.Fatalf("got %v", err)
	}
	if err := CheckAWSConnections(a, []AWSConnection{goodConn("a"), goodConn("ghost")}); err != nil {
		t.Fatal(err)
	}
	if err := CheckAWSConnections(AgentConfig{}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestConfigValidateAWSConnections(t *testing.T) {
	c := Config{BindAddr: "127.0.0.1:8080", Agents: []AgentConfig{{Name: "p", Bin: "claude", Runtime: RuntimeBox, AWSConnections: []string{"a"}}},
		AWSConnections: []AWSConnection{goodConn("a")}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.AWSConnections = nil
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "is not defined in aws_connections") {
		t.Errorf("undefined name must fail Config.Validate, got %v", err)
	}
	c.AWSConnections = []AWSConnection{goodConn("a"), goodConn("a")}
	if err := c.Validate(); err == nil {
		t.Error("duplicate connections must fail Config.Validate")
	}
}

func TestAWSConnByName(t *testing.T) {
	list := []AWSConnection{goodConn("a")}
	list[0].S3Read = []string{"b1"}
	got, ok := AWSConnByName(list, "a")
	if !ok {
		t.Fatal("a not found")
	}
	got.S3Read[0] = "changed"
	if list[0].S3Read[0] != "b1" {
		t.Error("AWSConnByName must return a deep copy")
	}
	if _, ok := AWSConnByName(list, "zz"); ok {
		t.Error("unknown name found")
	}
}

func twoConns() []AWSConnection {
	a := goodConn("sbx-a")
	a.Label = "A"
	a.Region = "ap-southeast-1"
	a.EgressAllow = []string{"api.example.com"}
	a.Description = "first"
	b := goodConn("sbx-b")
	b.Label = "B"
	b.Region = "us-east-1"
	return []AWSConnection{a, b}
}

func TestBindingsHashAWSConns(t *testing.T) {
	two := Bindings{AWSConns: twoConns()}
	const golden = "6a46c3fec026"
	t.Logf("two-connection hash = %s", two.Hash())
	if two.Hash() != golden {
		t.Errorf("two-connection Hash = %s, want %s", two.Hash(), golden)
	}
	if len(two.Hash()) != 12 {
		t.Errorf("hash must be 12 hex, got %q", two.Hash())
	}
	if !two.Any() {
		t.Error("Any must be true with connections")
	}
	if (Bindings{}).Any() {
		t.Error("empty Bindings has no binding")
	}
	rev := twoConns()
	rev[0], rev[1] = rev[1], rev[0]
	if (Bindings{AWSConns: rev}).Hash() != two.Hash() {
		t.Error("connection order must not change the hash")
	}
	if (Bindings{AWSConns: twoConns()[:1]}).Hash() == two.Hash() {
		t.Error("a second connection must change the hash")
	}
	edits := map[string]func(c *AWSConnection){
		"label":        func(c *AWSConnection) { c.Label = "other" },
		"region":       func(c *AWSConnection) { c.Region = "eu-west-1" },
		"egress_allow": func(c *AWSConnection) { c.EgressAllow = []string{"other.example.com"} },
		"description":  func(c *AWSConnection) { c.Description = "changed" },
	}
	for name, edit := range edits {
		for i := range 2 {
			l := twoConns()
			edit(&l[i])
			if (Bindings{AWSConns: l}).Hash() == two.Hash() {
				t.Errorf("editing %s of connection %d must change the hash", name, i)
			}
		}
	}
	// the set-like fields are normalised like the inline binding
	l := twoConns()
	l[0].EgressAllow = []string{"b.example.com", "a.example.com"}
	m := twoConns()
	m[0].EgressAllow = []string{"a.example.com", "b.example.com"}
	if (Bindings{AWSConns: l}).Hash() != (Bindings{AWSConns: m}).Hash() {
		t.Error("egress_allow order must not change the hash")
	}
	// connections fold after the GitHub binding and before the tokens
	withGH := Bindings{GitHub: goodGitHub(), AWSConns: twoConns()}
	if withGH.Hash() == two.Hash() || withGH.Hash() == (Bindings{GitHub: goodGitHub()}).Hash() {
		t.Error("GitHub and connections must both contribute")
	}
	withTok := Bindings{AWSConns: twoConns(), Tokens: []StaticToken{tokenByNameT(t, "sentry-corp")}}
	if withTok.Hash() == two.Hash() {
		t.Error("tokens must still fold in on top of connections")
	}
}

// DefHash uses the same normalisation as AWSBinding.Hash: set-like lists are
// order-independent and elevate's 0 and 60 minutes are one window.
func TestAWSConnectionDefHash(t *testing.T) {
	a := goodConn("a")
	a.Elevate = &AWSElevate{RoleARN: "arn:aws:iam::123456789012:role/agentbox-write"}
	b := goodConn("a")
	b.Elevate = &AWSElevate{RoleARN: "arn:aws:iam::123456789012:role/agentbox-write", MaxMinutes: 60}
	if a.DefHash() != b.DefHash() {
		t.Error("elevate max 0 and 60 must hash alike")
	}
	if len(a.DefHash()) != 12 {
		t.Errorf("DefHash must be 12 hex, got %q", a.DefHash())
	}
	c := goodConn("a")
	if c.DefHash() == a.DefHash() {
		t.Error("elevate must change DefHash")
	}
	d := goodConn("a")
	d.S3Read = []string{"x", "y"}
	e := goodConn("a")
	e.S3Read = []string{"y", "x"}
	if d.DefHash() != e.DefHash() {
		t.Error("s3_read order must not change DefHash")
	}
	if goodConn("a").DefHash() == goodConn("b").DefHash() {
		t.Error("name must change DefHash")
	}
}

func TestAWSConnectionClone(t *testing.T) {
	a := goodConn("a")
	a.EgressAllow = []string{"x.example.com"}
	a.Elevate = &AWSElevate{RoleARN: "arn:aws:iam::123456789012:role/w"}
	c := a.Clone()
	c.S3Read[0] = "changed"
	c.EgressAllow[0] = "changed"
	c.Elevate.RoleARN = "changed"
	if a.S3Read[0] == "changed" || a.EgressAllow[0] == "changed" || a.Elevate.RoleARN == "changed" {
		t.Error("Clone must be deep")
	}
}

func TestAWSConnectionsRegion(t *testing.T) {
	a, b := goodConn("a"), goodConn("b")
	if got := AWSConnectionsRegion([]AWSConnection{a, b}); got != "ap-southeast-1" {
		t.Errorf("same region: got %q", got)
	}
	b.Region = "us-east-1"
	if got := AWSConnectionsRegion([]AWSConnection{a, b}); got != "" {
		t.Errorf("mixed regions: got %q", got)
	}
	if got := AWSConnectionsRegion(nil); got != "" {
		t.Errorf("empty: got %q", got)
	}
	if got := AWSConnectionsRegion([]AWSConnection{a}); got != "ap-southeast-1" {
		t.Errorf("single: got %q", got)
	}
}

func ssoConnWithAck() AWSConnection {
	c := AWSConnection{Name: "wide", Description: "d", AWSBinding: ssoBinding()}
	c.FullAccessAck = &awsprobe.Ack{
		State:   awsprobe.GateNotReadOnly,
		Caller:  "arn:aws:sts::123456789012:assumed-role/AWSReservedSSO_Example_0000000000000000",
		Refused: []string{"iam:*", "s3:putobject"},
		At:      time.Unix(1700000000, 0).UTC(),
		Actor:   "web",
	}
	return c
}

func TestAWSConnectionAckOnlyForSSODirect(t *testing.T) {
	c := ssoConnWithAck()
	if err := c.Validate(); err != nil {
		t.Fatalf("sso_direct with a valid ack: %v", err)
	}
	if !c.FullAccess() {
		t.Error("FullAccess() = false with an ack")
	}
	plain := AWSConnection{Name: "wide", AWSBinding: ssoBinding()}
	if plain.FullAccess() {
		t.Error("FullAccess() = true without an ack")
	}
	am := goodConn("acme")
	am.FullAccessAck = c.FullAccessAck
	if err := am.Validate(); err == nil || !strings.Contains(err.Error(), "full_access_ack") {
		t.Errorf("assume_role with an ack: %v", err)
	}
	if am.FullAccess() {
		t.Error("FullAccess() = true on assume_role")
	}
	bad := ssoConnWithAck()
	bad.FullAccessAck.Caller = ""
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "full_access_ack:") {
		t.Errorf("invalid ack: %v", err)
	}
}

func TestAWSConnectionCloneCopiesAck(t *testing.T) {
	c := ssoConnWithAck()
	cl := c.Clone()
	cl.FullAccessAck.Refused[0] = "changed"
	cl.FullAccessAck.MissingPerms = append(cl.FullAccessAck.MissingPerms, "x")
	cl.FullAccessAck.Actor = "other"
	if c.FullAccessAck.Refused[0] != "iam:*" || len(c.FullAccessAck.MissingPerms) != 0 || c.FullAccessAck.Actor != "web" {
		t.Errorf("clone shares the ack: %+v", c.FullAccessAck)
	}
	if got, ok := AWSConnByName([]AWSConnection{c}, "wide"); !ok || got.FullAccessAck == c.FullAccessAck {
		t.Error("AWSConnByName must return a deep copy of the ack")
	}
}

// The literal was computed on main before FullAccessAck existed.
func TestDefHashUnchangedWithoutAck(t *testing.T) {
	c := AWSConnection{Name: "wide", Description: "d", AWSBinding: ssoBinding()}
	if got := c.DefHash(); got != "06f54ebbd8c0" {
		t.Errorf("DefHash = %s, want 06f54ebbd8c0", got)
	}
}

func TestDefHashChangesWithAck(t *testing.T) {
	plain := AWSConnection{Name: "wide", Description: "d", AWSBinding: ssoBinding()}
	acked := ssoConnWithAck()
	if plain.DefHash() == acked.DefHash() {
		t.Error("an acknowledgement must change the DefHash")
	}
	re := ssoConnWithAck()
	re.FullAccessAck.At = re.FullAccessAck.At.Add(time.Hour)
	if re.DefHash() == acked.DefHash() {
		t.Error("a re-acknowledgement must change the DefHash")
	}
	if AWSConnsHash([]AWSConnection{plain}) == AWSConnsHash([]AWSConnection{acked}) {
		t.Error("AWSConnsHash ignores the ack")
	}
}
