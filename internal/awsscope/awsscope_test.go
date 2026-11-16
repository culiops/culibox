package awsscope

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestValidRoleARN(t *testing.T) {
	good := []string{"arn:aws:iam::123456789012:role/agentbox-agent", "arn:aws-cn:iam::123456789012:role/path/x"}
	bad := []string{"", "arn:aws:iam::12345678901:role/x", "arn:aws:iam::123456789012:user/x",
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
	const want = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"},{"Effect":"Deny","Action":["s3:GetObject","s3:GetObjectVersion"],"NotResource":["arn:aws:s3:::culishop-tfstate/*","arn:aws:s3:::logs-bucket/AWSLogs/*"]},{"Effect":"Deny","Action":["secretsmanager:GetSecretValue","ssm:GetParameter","ssm:GetParameters","ssm:GetParametersByPath","ssm:GetParameterHistory","dynamodb:GetItem","dynamodb:BatchGetItem","dynamodb:Query","dynamodb:Scan","dynamodb:PartiQLSelect","dynamodb:GetRecords"],"Resource":"*"},{"Effect":"Deny","Action":"kms:Decrypt","Resource":"*","Condition":{"StringNotLike":{"kms:ViaService":"s3.*.amazonaws.com"}}}]}`
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

// elevatedBefore211 is ElevatedPolicy("aws", ["culishop-tfstate"], allow_iam off)
// as the pre-#211 code (1d7ed12) rendered it.
const elevatedBefore211 = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"},{"Effect":"Deny","Action":["s3:GetObject","s3:GetObjectVersion"],"NotResource":["arn:aws:s3:::culishop-tfstate/*"]},{"Effect":"Deny","Action":["secretsmanager:GetSecretValue","ssm:GetParameter","ssm:GetParameters","ssm:GetParametersByPath","dynamodb:GetItem","dynamodb:BatchGetItem","dynamodb:Query","dynamodb:Scan","dynamodb:PartiQLSelect"],"Resource":"*"},{"Effect":"Deny","Action":"kms:Decrypt","Resource":"*","Condition":{"StringNotLike":{"kms:ViaService":"s3.*.amazonaws.com"}}},{"Effect":"Deny","Action":["sts:AssumeRole*","sts:GetFederationToken","sts:GetSessionToken","iam:CreateAccessKey","iam:CreateLoginProfile","iam:UpdateLoginProfile","iam:CreateServiceSpecificCredential","iam:UploadSSHPublicKey","cloudtrail:StopLogging","cloudtrail:DeleteTrail","cloudtrail:UpdateTrail","cloudtrail:PutEventSelectors"],"Resource":"*"},{"Effect":"Deny","Action":"iam:*","Resource":"arn:aws:iam::*:role/agentbox-*"},{"Effect":"Deny","Action":["iam:Create*","iam:Delete*","iam:Put*","iam:Attach*","iam:Detach*","iam:Update*","iam:Add*","iam:Remove*","iam:Tag*","iam:Untag*","iam:Set*","iam:Change*"],"Resource":"*"}]}`

// #211 acceptance: with box_bucket_prefix unset, the elevated policy is the
// pre-#211 one plus only the public-parameter carve-out (and #302's PassRole
// deny, #301's copy denies, #307's exposure denies). Since #329 the policy is
// written in fewer statements, so the literal is written out; that it still
// decides as before is checked by equiv_test.go, and against the pre-#211
// literal here through the evaluator.
func TestElevatedPolicyOptInOffIsTodayPlusPublicParameters(t *testing.T) {
	got, err := ElevatedPolicy("aws", []string{"culishop-tfstate"}, Elevation{})
	if err != nil {
		t.Fatal(err)
	}
	// In order: Allow *; the read deny (S3 objects outside s3_read, the four
	// SSM parameter reads outside AWS's public parameters, #211); one Resource
	// "*" deny with the other data reads, the guards minus the four that
	// iam:Create*/iam:Update* already deny (CreateAccessKey, CreateLoginProfile,
	// UpdateLoginProfile, CreateServiceSpecificCredential), iam:PassRole (#302,
	// prefix unset), the IAM writes, the managed copies (#301), the account
	// public access block and, with no box prefix, replication and the S3
	// exposure calls (#307); KMS; then the agent's own roles.
	want := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"},` +
		`{"Effect":"Deny","Action":["s3:GetObject","s3:GetObjectVersion","ssm:GetParameter*"],"NotResource":["arn:aws:s3:::culishop-tfstate/*","arn:aws:ssm:*::parameter/aws/service/*"]},` +
		`{"Effect":"Deny","Action":["secretsmanager:GetSecretValue","dynamodb:GetItem","dynamodb:BatchGetItem","dynamodb:Query","dynamodb:Scan","dynamodb:PartiQLSelect","dynamodb:GetRecords",` +
		`"sts:AssumeRole*","sts:GetFederationToken","sts:GetSessionToken",` +
		`"iam:UploadSSHPublicKey","cloudtrail:StopLogging","cloudtrail:DeleteTrail","cloudtrail:UpdateTrail","cloudtrail:PutEventSelectors",` +
		`"iam:PassRole",` +
		`"iam:Create*","iam:Delete*","iam:Put*","iam:Attach*","iam:Detach*","iam:Update*","iam:Add*","iam:Remove*","iam:Tag*","iam:Untag*","iam:Set*","iam:Change*",` +
		`"dynamodb:ExportTableToPointInTime","rds:StartExportTask","s3:CreateJob","datasync:StartTaskExecution","backup:StartRestoreJob",` +
		`"s3:PutAccountPublicAccessBlock","s3:GetDataAccess","s3:*eAccessGrant*",` +
		`"s3:PutReplicationConfiguration","s3:Put*Policy*","s3:DeleteBucketPolicy","s3:Put*Acl","s3:PutBucketPublicAccessBlock","s3:PutBucketOwnershipControls"],"Resource":"*"},` +
		`{"Effect":"Deny","Action":"kms:Decrypt","Resource":"*","Condition":{"StringNotLike":{"kms:ViaService":"s3.*.amazonaws.com"}}},` +
		`{"Effect":"Deny","Action":"iam:*","Resource":"arn:aws:iam::*:role/agentbox-*"}]}`
	if got != want {
		t.Fatalf("ElevatedPolicy (opt-in off) changed:\n got  %s\n want %s", got, want)
	}
	// Everything the pre-#211 policy denied is still denied, apart from the
	// public parameters #211 opened.
	for _, r := range []evalReq{
		{"s3:GetObject", "arn:aws:s3:::human-data/x", nil}, {"ssm:GetParameter", acctParam, nil},
		{"ssm:GetParametersByPath", acctParam, nil}, {"secretsmanager:GetSecretValue", "arn:aws:secretsmanager:us-east-1:123456789012:secret:x", nil},
		{"dynamodb:Query", "arn:aws:dynamodb:us-east-1:123456789012:table/t", nil}, {"kms:Decrypt", "arn:aws:kms:us-east-1:123456789012:key/k", nil},
		{"iam:CreateAccessKey", "arn:aws:iam::123456789012:user/x", nil}, {"iam:PutRolePolicy", "arn:aws:iam::123456789012:role/x", nil},
		{"iam:GetRole", "arn:aws:iam::123456789012:role/agentbox-agent", nil}, {"sts:AssumeRole", "arn:aws:iam::123456789012:role/x", nil},
	} {
		if evaluate(t, elevatedBefore211, r) != "deny" || evaluate(t, got, r) != "deny" {
			t.Errorf("%s on %s: before %s, now %s", r.action, r.resource, evaluate(t, elevatedBefore211, r), evaluate(t, got, r))
		}
	}
	if evaluate(t, got, evalReq{"ssm:GetParameter", pubParam, nil}) != "allow" || evaluate(t, got, evalReq{"s3:GetObject", "arn:aws:s3:::culishop-tfstate/k", nil}) != "allow" {
		t.Error("the public parameters and the s3_read bucket must stay readable")
	}
}

func TestElevatedPolicyKeepsTheInvestigateDeniesAndAddsTheGuards(t *testing.T) {
	s, err := ElevatedPolicy("aws", []string{"culishop-tfstate"}, Elevation{})
	if err != nil {
		t.Fatal(err)
	}
	p := parsePol(t, s)
	if p.Statement[0].Effect != "Allow" || p.Statement[0].Action != "*" || p.Statement[0].Resource != "*" {
		t.Fatalf("first statement must be Allow * *: %s", s)
	}
	inv, _ := SessionPolicy("aws", []string{"culishop-tfstate"})
	invP := parsePol(t, inv)
	// Statements 0 and 3 of the investigate policy (Allow, KMS) are carried
	// unchanged. Statements 1 and 2 are carried and extended (#329 packs denies
	// that share an exception, or that have none, into them): the S3 read deny
	// keeps its actions and exceptions first, and the Resource "*" deny keeps
	// every action but the SSM reads, which move to the read deny (#211).
	for _, i := range []int{0, 3} {
		a, _ := json.Marshal(invP.Statement[i])
		b, _ := json.Marshal(p.Statement[i])
		if string(a) != string(b) {
			t.Fatalf("investigate statement %d differs in the elevated policy:\n %s\n %s", i, a, b)
		}
	}
	ia, ea := actions(invP.Statement[1].Action), actions(p.Statement[1].Action)
	if !slices.Equal(ea[:len(ia)], ia) || !slices.Equal(p.Statement[1].NotResource[:len(invP.Statement[1].NotResource)], invP.Statement[1].NotResource) {
		t.Fatalf("the S3 read deny must start with the investigate one:\n %+v\n %+v", invP.Statement[1], p.Statement[1])
	}
	for _, a := range actions(invP.Statement[2].Action) {
		if !slices.Contains(actions(p.Statement[2].Action), a) && !slices.Contains(ssmParamReads, a) {
			t.Errorf("the Resource \"*\" deny lost %s", a)
		}
	}
	// Each guard is denied with allow_iam on and off. With it off, four of them
	// are written only as the iam:Create*/iam:Update* write denies (#307 size),
	// so this asks the evaluator, not the text.
	withIAM, _ := ElevatedPolicy("aws", []string{"culishop-tfstate"}, Elevation{AllowIAM: true, PassRolePrefix: "lab-"})
	for _, a := range []string{"sts:AssumeRole", "sts:AssumeRoleWithWebIdentity", "sts:GetFederationToken", "sts:GetSessionToken",
		"iam:CreateAccessKey", "iam:CreateLoginProfile", "iam:UpdateLoginProfile",
		"iam:CreateServiceSpecificCredential", "iam:UploadSSHPublicKey",
		"cloudtrail:StopLogging", "cloudtrail:DeleteTrail", "cloudtrail:UpdateTrail", "cloudtrail:PutEventSelectors"} {
		for name, pol := range map[string]string{"allow_iam off": s, "allow_iam on": withIAM} {
			if got := evaluate(t, pol, evalReq{a, "arn:aws:iam::123456789012:user/x", nil}); got != "deny" {
				t.Errorf("%s: guard %s is %s", name, a, got)
			}
		}
	}
	if !denied(parsePol(t, withIAM), "iam:CreateAccessKey", "*") {
		t.Error("with allow_iam on, the credential guards must be written out")
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
	if !denied(p, "iam:PassRole", "*") {
		t.Error("iam:PassRole must be denied on every role when pass_role_prefix is unset (#302)")
	}
}

func TestElevatedPolicyAllowIAMDropsOnlyTheIAMWriteDenies(t *testing.T) {
	s, err := ElevatedPolicy("aws", nil, Elevation{AllowIAM: true})
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

// #302: with pass_role_prefix, iam:PassRole is denied on every role EXCEPT
// those named <prefix>*; unset, it is denied on every role.
func TestElevatedPolicyPassRolePrefix(t *testing.T) {
	for _, iam := range []bool{false, true} {
		off, err := ElevatedPolicy("aws", nil, Elevation{AllowIAM: iam})
		if err != nil {
			t.Fatal(err)
		}
		on, err := ElevatedPolicy("aws", nil, Elevation{AllowIAM: iam, PassRolePrefix: "culibox-"})
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct{ role, off, on string }{
			{"culibox-lambda", "deny", "allow"},
			{"culibox-", "deny", "allow"},
			{"other-lambda", "deny", "deny"},
			{"Culibox-lambda", "deny", "deny"}, // role-name match is case-sensitive here: fail closed
			{"x-culibox-lambda", "deny", "deny"},
			{"path/culibox-lambda", "deny", "deny"}, // a path that does not start with the prefix: fail closed
			// "*" crosses "/": a path that starts with the prefix matches (pinned, so the docs say so)
			{"culibox-x/Admin", "deny", "allow"},
			{"agentbox-agent-write", "deny", "deny"},
			{"AWSServiceRoleForX", "deny", "deny"},
		} {
			r := "arn:aws:iam::123456789012:role/" + c.role
			if got := evaluate(t, off, evalReq{"iam:PassRole", r, nil}); got != c.off {
				t.Errorf("allow_iam %v, unset: PassRole %s = %s want %s", iam, c.role, got, c.off)
			}
			if got := evaluate(t, on, evalReq{"iam:PassRole", r, nil}); got != c.on {
				t.Errorf("allow_iam %v, prefix: PassRole %s = %s want %s", iam, c.role, got, c.on)
			}
		}
		// every other action is untouched by the prefix
		if got := evaluate(t, on, evalReq{"lambda:CreateFunction", "arn:aws:lambda:us-east-1:123456789012:function:f", nil}); got != "allow" {
			t.Errorf("allow_iam %v: lambda:CreateFunction %s", iam, got)
		}
	}
	// the unset form is folded into the guard deny: one statement, no extra
	off, _ := ElevatedPolicy("aws", nil, Elevation{})
	if strings.Count(off, "iam:PassRole") != 1 || strings.Contains(off, "NotResource\":[\"arn:aws:iam") {
		t.Errorf("unset PassRole deny should be one folded action: %s", off)
	}
}

func TestElevatedPolicyPassRolePrefixPartitionAndInjection(t *testing.T) {
	s, err := ElevatedPolicy("aws-us-gov", nil, Elevation{PassRolePrefix: "lab_x.y@z+1="})
	if err != nil || !strings.Contains(s, `"arn:aws-us-gov:iam::*:role/lab_x.y@z+1=*"`) {
		t.Fatalf("partition or prefix not carried: %v %s", err, s)
	}
	for _, bad := range []string{"", "ab", "*", "lab*", "lab?", `lab"],"Effect":"Allow`, "lab/x", "lab ", "lab\n",
		"agentbox-", "AgentBox-x", "agentbox", "agent", "aws", "AWSServiceRole", "awsx-", "service-role", "service-", "serv",
		strings.Repeat("x", 41)} {
		if ValidPassRolePrefix(bad) == nil {
			t.Errorf("pass_role_prefix %q accepted", bad)
		}
		if bad != "" {
			if _, err := ElevatedPolicy("aws", nil, Elevation{PassRolePrefix: bad}); err == nil {
				t.Errorf("ElevatedPolicy accepted pass_role_prefix %q", bad)
			}
		}
	}
	for _, ok := range []string{"culibox-", "lab", "lab-x_1", "cul"} {
		if err := ValidPassRolePrefix(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
}

func TestElevatedPolicyPartitionAndInjection(t *testing.T) {
	s, err := ElevatedPolicy("aws-cn", nil, Elevation{})
	if err != nil || !strings.Contains(s, "arn:aws-cn:iam::*:role/agentbox-*") {
		t.Fatalf("partition not carried: %v %s", err, s)
	}
	for _, bad := range []string{`x"],"Effect":"Allow`, "b*", "a/../b/", "bucket/pre fix/"} {
		if _, err := ElevatedPolicy("aws", []string{bad}, Elevation{}); err == nil {
			t.Errorf("s3_read %q accepted", bad)
		}
	}
	if _, err := ElevatedPolicy("aws\"", nil, Elevation{}); err == nil {
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
	if _, err := ElevatedPolicy("aws", entries, Elevation{}); err == nil || !strings.Contains(err.Error(), "2048") {
		t.Fatalf("an oversized policy must be refused naming the limit, got %v", err)
	}
	s, err := ElevatedPolicy("aws", []string{"culishop-tfstate"}, Elevation{})
	if err != nil || len(s) > MaxPolicyChars {
		t.Fatalf("the sandbox binding's policy must fit: %d chars, %v", len(s), err)
	}
}

// matchesAny decides which guards the IAM write denies already cover (#307);
// a false "covered" would drop a guard, so it must match only what IAM would.
func TestMatchesAny(t *testing.T) {
	for _, c := range []struct {
		action string
		want   bool
	}{
		{"iam:CreateAccessKey", true}, {"IAM:createaccesskey", true}, {"iam:UpdateLoginProfile", true},
		{"iam:UploadSSHPublicKey", false}, {"iam:PassRole", false}, {"sts:AssumeRole*", false},
		{"cloudtrail:UpdateTrail", false}, {"iamx:CreateRole", false},
	} {
		if got := matchesAny(iamWriteDenies, c.action); got != c.want {
			t.Errorf("%s: got %v want %v", c.action, got, c.want)
		}
	}
	if !matchesAny([]string{"s3:GetObject"}, "s3:getobject") || matchesAny([]string{"s3:Get*Acl"}, "s3:GetBucketAcl") {
		t.Error("literal patterns match exactly; a pattern with an inner * is never treated as covering")
	}
}

func TestDataReadDeniesCoverEveryValueRead(t *testing.T) {
	sess, err := SessionPolicy("aws", nil)
	if err != nil {
		t.Fatal(err)
	}
	elev, err := ElevatedPolicy("aws", nil, Elevation{})
	if err != nil {
		t.Fatal(err)
	}
	// The elevated policy writes the SSM reads as ssm:GetParameter* (#329), so
	// this asks the evaluator.
	for name, p := range map[string]string{"session": sess, "elevated": elev} {
		for _, r := range []evalReq{{"ssm:GetParameterHistory", "arn:aws:ssm:us-east-1:123456789012:parameter/p", nil},
			{"dynamodb:GetRecords", "arn:aws:dynamodb:us-east-1:123456789012:table/t/stream/s", nil}} {
			if evaluate(t, p, r) != "deny" {
				t.Errorf("%s policy does not deny %s: %s", name, r.action, p)
			}
		}
	}
	got := DataReadDenies()
	for _, a := range []string{"ssm:GetParameter", "ssm:GetParameters", "ssm:GetParametersByPath", "ssm:GetParameterHistory"} {
		if !slices.Contains(got, a) {
			t.Errorf("DataReadDenies lacks %s", a)
		}
	}
}
