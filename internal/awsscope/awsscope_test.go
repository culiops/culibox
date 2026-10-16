package awsscope

import (
	"encoding/json"
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
