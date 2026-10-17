package awsscope

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestValidRoleARN(t *testing.T) {
	good := []string{"arn:aws:iam::123456789012:role/agentbox-agent", "arn:aws-cn:iam::123456789012:role/path/x"}
	bad := []string{"", "arn:aws:iam::22619881380:role/x", "arn:aws:iam::123456789012:user/x",
		"arn:aws:iam::123456789012:role/", "arn:aws:iam::123456789012:role/a b", "arn:aws:iam::123456789012:role/x\n"}
	for _, s := range good {
		if err := ValidRoleARN(s); err != nil {
			t.Errorf("%q rejected: %v", s, err)
		}
	}
	for _, s := range bad {
		if ValidRoleARN(s) == nil {
			t.Errorf("%q accepted", s)
		}
	}
	if AccountOf("arn:aws:iam::123456789012:role/agentbox-agent") != "123456789012" {
		t.Fatal("AccountOf")
	}
	if PartitionOf("arn:aws-cn:iam::123456789012:role/x") != "aws-cn" {
		t.Fatal("PartitionOf")
	}
}

func TestValidRegionRefusesIniInjection(t *testing.T) {
	for _, s := range []string{"ap-southeast-1", "us-gov-west-1", "eu-central-2"} {
		if err := ValidRegion(s); err != nil {
			t.Errorf("%q rejected: %v", s, err)
		}
	}
	for _, s := range []string{"", "ap-southeast-1\ncredential_process = /bin/sh", "[x]", "AP-SOUTHEAST-1", "ap southeast 1"} {
		if ValidRegion(s) == nil {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestValidS3Entry(t *testing.T) {
	for _, s := range []string{"culishop-tfstate", "culishop-logs/AWSLogs/", "a.b-c/x/y=1/"} {
		if err := ValidS3Entry(s); err != nil {
			t.Errorf("%q rejected: %v", s, err)
		}
	}
	for _, s := range []string{"", "*", "bucket/*", "bucket/logs", "bucket/", "bucket/a?b", `bucket/"x`, "Bucket", "ab", "-bucket", "bucket/\n", "bucket//", "../x"} {
		if ValidS3Entry(s) == nil {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestSessionName(t *testing.T) {
	if got := SessionName("claude-3"); got != "agentbox-claude-3" {
		t.Fatalf("got %q", got)
	}
	if got := SessionName("a b/c:d"); got != "agentbox-a-b-c-d" {
		t.Fatalf("got %q", got)
	}
	if got := SessionName(strings.Repeat("x", 100)); len(got) != 64 {
		t.Fatalf("len %d", len(got))
	}
}

type stmt struct {
	Effect      string
	Action      any
	Resource    any            `json:",omitempty"`
	NotResource []string       `json:",omitempty"`
	Condition   map[string]any `json:",omitempty"`
}

func TestSessionPolicyDeniesDataReadsExceptAllowlist(t *testing.T) {
	p, err := SessionPolicy("aws", []string{"culishop-tfstate", "culishop-logs/AWSLogs/"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(p, "\n ") {
		t.Fatalf("policy not compact: %s", p)
	}
	var doc struct {
		Version   string
		Statement []stmt
	}
	if err := json.Unmarshal([]byte(p), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Statement) != 4 || doc.Statement[0].Effect != "Allow" {
		t.Fatalf("unexpected shape: %s", p)
	}
	s3 := doc.Statement[1]
	want := []string{"arn:aws:s3:::culishop-tfstate/*", "arn:aws:s3:::culishop-logs/AWSLogs/*"}
	if s3.Effect != "Deny" || strings.Join(s3.NotResource, ",") != strings.Join(want, ",") {
		t.Fatalf("s3 statement: %+v", s3)
	}
	if !strings.Contains(p, `"secretsmanager:GetSecretValue"`) || !strings.Contains(p, `"kms:ViaService":"s3.*.amazonaws.com"`) {
		t.Fatalf("missing denies: %s", p)
	}
}

func TestSessionPolicyEmptyAllowlistDeniesAllObjects(t *testing.T) {
	p, err := SessionPolicy("aws", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p, "NotResource") || !strings.Contains(p, `"Action":["s3:GetObject","s3:GetObjectVersion"],"Resource":"*"`) {
		t.Fatalf("empty allowlist must deny every object: %s", p)
	}
}

func TestSessionPolicyBudget(t *testing.T) {
	var many []string
	for i := 0; i < 60; i++ {
		many = append(many, "bucket-number-"+strings.Repeat("x", 20)+"/prefix/")
	}
	if _, err := SessionPolicy("aws", many); err == nil || !strings.Contains(err.Error(), "2048") {
		t.Fatalf("over-budget allowlist must be refused with the limit in the message, got %v", err)
	}
	if _, err := SessionPolicy("aws", []string{"bad*"}); err == nil {
		t.Fatal("invalid entry accepted by SessionPolicy")
	}
}

func TestSessionPolicyRefusesBadPartition(t *testing.T) {
	for _, part := range []string{"", "aws:s3:::*", "*", "aws\n", "AWS", "aws-iso", "aws-cn/x", " aws"} {
		if p, err := SessionPolicy(part, []string{"bucket"}); err == nil {
			t.Errorf("partition %q accepted: %s", part, p)
		}
	}
	for _, part := range []string{"aws", "aws-cn", "aws-us-gov"} {
		if _, err := SessionPolicy(part, []string{"bucket"}); err != nil {
			t.Errorf("partition %q rejected: %v", part, err)
		}
	}
}

// The investigate policy must not change by a byte: the broker has minted with
// it since M3a, and the refactor shares its statements with ElevatedPolicy.
// The literal was produced by the pre-M3b code (cb76a77).
func TestSessionPolicyBytesUnchanged(t *testing.T) {
	got, err := SessionPolicy("aws", []string{"culishop-tfstate", "logs-bucket/AWSLogs/"})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"},{"Effect":"Deny","Action":["s3:GetObject","s3:GetObjectVersion"],"NotResource":["arn:aws:s3:::culishop-tfstate/*","arn:aws:s3:::logs-bucket/AWSLogs/*"]},{"Effect":"Deny","Action":["secretsmanager:GetSecretValue","ssm:GetParameter","ssm:GetParameters","ssm:GetParametersByPath","dynamodb:GetItem","dynamodb:BatchGetItem","dynamodb:Query","dynamodb:Scan","dynamodb:PartiQLSelect"],"Resource":"*"},{"Effect":"Deny","Action":"kms:Decrypt","Resource":"*","Condition":{"StringNotLike":{"kms:ViaService":"s3.*.amazonaws.com"}}}]}`
	if got != want {
		t.Fatalf("SessionPolicy changed:\n got  %s\n want %s", got, want)
	}
}

type pol struct {
	Statement []struct {
		Effect      string
		Action      any
		Resource    any
		NotResource []string
	}
}

func parsePol(t *testing.T, s string) pol {
	t.Helper()
	var p pol
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, s)
	}
	return p
}

func actions(a any) []string {
	switch v := a.(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, len(v))
		for i, x := range v {
			out[i] = x.(string)
		}
		return out
	}
	return nil
}

// denied reports whether some Deny statement names action exactly (wildcards
// are compared as written: the test pins the policy text, AWS evaluates it).
func denied(p pol, action string, resource string) bool {
	for _, s := range p.Statement {
		if s.Effect != "Deny" {
			continue
		}
		for _, a := range actions(s.Action) {
			if a == action && (resource == "" || s.Resource == resource) {
				return true
			}
		}
	}
	return false
}

func TestElevatedPolicyKeepsTheInvestigateDeniesAndAddsTheGuards(t *testing.T) {
	s, err := ElevatedPolicy("aws", []string{"culishop-tfstate"}, false)
	if err != nil {
		t.Fatal(err)
	}
	p := parsePol(t, s)
	if p.Statement[0].Effect != "Allow" || p.Statement[0].Action != "*" || p.Statement[0].Resource != "*" {
		t.Fatalf("first statement must be Allow * *: %s", s)
	}
	inv, _ := SessionPolicy("aws", []string{"culishop-tfstate"})
	invP := parsePol(t, inv)
	for i := range invP.Statement { // D4: every investigate statement, unchanged and in order
		a, _ := json.Marshal(invP.Statement[i])
		b, _ := json.Marshal(p.Statement[i])
		if string(a) != string(b) {
			t.Fatalf("statement %d differs from the investigate policy:\n %s\n %s", i, a, b)
		}
	}
	for _, a := range []string{"sts:AssumeRole*", "sts:GetFederationToken", "sts:GetSessionToken",
		"iam:CreateAccessKey", "iam:CreateLoginProfile", "iam:UpdateLoginProfile",
		"iam:CreateServiceSpecificCredential", "iam:UploadSSHPublicKey",
		"cloudtrail:StopLogging", "cloudtrail:DeleteTrail", "cloudtrail:UpdateTrail", "cloudtrail:PutEventSelectors"} {
		if !denied(p, a, "*") {
			t.Errorf("guard deny missing: %s", a)
		}
	}
	if !denied(p, "iam:*", "arn:aws:iam::*:role/agentbox-*") {
		t.Error("the agent's own roles must be off limits")
	}
	for _, a := range []string{"iam:Create*", "iam:Delete*", "iam:Put*", "iam:Attach*", "iam:Detach*", "iam:Update*",
		"iam:Add*", "iam:Remove*", "iam:Tag*", "iam:Untag*", "iam:Set*", "iam:Change*"} {
		if !denied(p, a, "*") {
			t.Errorf("IAM write deny missing with allow_iam off: %s", a)
		}
	}
	if strings.Contains(s, "PassRole") {
		t.Error("iam:PassRole must stay allowed (ECS/Lambda applies need it)")
	}
}

func TestElevatedPolicyAllowIAMDropsOnlyTheIAMWriteDenies(t *testing.T) {
	s, err := ElevatedPolicy("aws", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	p := parsePol(t, s)
	if denied(p, "iam:Create*", "*") {
		t.Fatal("allow_iam must drop the IAM write denies")
	}
	if !denied(p, "iam:CreateAccessKey", "*") || !denied(p, "iam:*", "arn:aws:iam::*:role/agentbox-*") || !denied(p, "sts:AssumeRole*", "*") {
		t.Fatal("allow_iam must keep every guard deny")
	}
}

func TestElevatedPolicyPartitionAndInjection(t *testing.T) {
	s, err := ElevatedPolicy("aws-cn", nil, false)
	if err != nil || !strings.Contains(s, "arn:aws-cn:iam::*:role/agentbox-*") {
		t.Fatalf("partition not carried: %v %s", err, s)
	}
	for _, bad := range []string{`x"],"Effect":"Allow`, "b*", "a/../b/", "bucket/pre fix/"} {
		if _, err := ElevatedPolicy("aws", []string{bad}, false); err == nil {
			t.Errorf("s3_read %q accepted", bad)
		}
	}
	if _, err := ElevatedPolicy("aws\"", nil, false); err == nil {
		t.Error("a bad partition was accepted")
	}
}

func TestElevatedPolicySizeBudget(t *testing.T) {
	// Entries carry a prefix ("bucket-NN/logs/…/"): a bare trailing "/" would be
	// refused by ValidS3Entry before the size check is ever reached.
	var entries []string
	for i := 0; len(entries) < 40; i++ {
		entries = append(entries, fmt.Sprintf("bucket-%02d/logs/%s/", i, strings.Repeat("p", 30)))
	}
	if _, err := ElevatedPolicy("aws", entries, false); err == nil || !strings.Contains(err.Error(), "2048") {
		t.Fatalf("an oversized policy must be refused naming the limit, got %v", err)
	}
	s, err := ElevatedPolicy("aws", []string{"culishop-tfstate"}, false)
	if err != nil || len(s) > MaxPolicyChars {
		t.Fatalf("the sandbox binding's policy must fit: %d chars, %v", len(s), err)
	}
}
