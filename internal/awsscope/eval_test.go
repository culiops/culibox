package awsscope

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// A small IAM evaluator for the policies this package renders, so the tests
// can ask "is this request allowed?" of the JSON itself. It implements only
// what the policies use: Action and Resource/NotResource globs, and the
// String(Not)Equals/String(Not)Like operators. Where IAM is subtler it errs
// towards matching MORE: its "*" crosses ":" and "/" anywhere (AWS: a "*" that
// ends an ARN segment can expand past the colon), so a NotResource carve-out
// that excludes a resource here also excludes it in AWS. A missing condition
// key makes a negated operator true and a positive one false (IAM condition
// operators page).
type evalReq struct {
	action, resource string
	ctx              map[string]string
}

type evalStmt struct {
	Effect      string
	Action      any
	Resource    any
	NotResource []string
	Condition   map[string]map[string]any
}

func strs(v any) []string {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		return []string{x}
	case []any:
		out := make([]string, len(x))
		for i, e := range x {
			out[i] = e.(string)
		}
		return out
	}
	panic("unexpected JSON shape")
}

func glob(pattern, s string, fold bool) bool {
	re := "^" + strings.NewReplacer(`\*`, ".*", `\?`, ".").Replace(regexp.QuoteMeta(pattern)) + "$"
	if fold {
		re = "(?i)" + re
	}
	return regexp.MustCompile(re).MatchString(s)
}

func anyGlob(pats []string, s string, fold bool) bool {
	for _, p := range pats {
		if glob(p, s, fold) {
			return true
		}
	}
	return false
}

func condHolds(cond map[string]map[string]any, ctx map[string]string) bool {
	for op, kv := range cond {
		for key, want := range kv {
			v, ok := ctx[key]
			ws := strs(want)
			var match bool
			switch op {
			case "StringEquals", "StringNotEquals":
				for _, w := range ws {
					match = match || (ok && v == w)
				}
			case "StringLike", "StringNotLike":
				match = ok && anyGlob(ws, v, false)
			default:
				panic("evaluator does not know operator " + op)
			}
			if strings.Contains(op, "Not") {
				match = !match // a missing key: no match, so the negation holds
			}
			if !match {
				return false
			}
		}
	}
	return true
}

// evaluate returns "allow", "implicit" (no Allow applies) or "deny" (an
// explicit Deny applies; it wins over every Allow).
func evaluate(t *testing.T, policy string, r evalReq) string {
	t.Helper()
	var doc struct{ Statement []evalStmt }
	if err := json.Unmarshal([]byte(policy), &doc); err != nil {
		t.Fatal(err)
	}
	allowed := false
	for _, s := range doc.Statement {
		if !anyGlob(strs(s.Action), r.action, true) {
			continue
		}
		if s.NotResource != nil {
			if anyGlob(s.NotResource, r.resource, false) {
				continue
			}
		} else if !anyGlob(strs(s.Resource), r.resource, false) {
			continue
		}
		if !condHolds(s.Condition, r.ctx) {
			continue
		}
		if s.Effect == "Deny" {
			return "deny"
		}
		allowed = true
	}
	if allowed {
		return "allow"
	}
	return "implicit"
}

func TestEvaluatorSelfCheck(t *testing.T) {
	p := `{"Statement":[{"Effect":"Allow","Action":"*","Resource":"*"},
	{"Effect":"Deny","Action":"kms:Decrypt","Resource":"*","Condition":{"StringNotLike":{"kms:ViaService":"s3.*.amazonaws.com"}}}]}`
	for _, c := range []struct {
		r    evalReq
		want string
	}{
		{evalReq{"kms:Decrypt", "arn:aws:kms:us-east-1:1:key/k", nil}, "deny"}, // missing key: negation holds
		{evalReq{"kms:Decrypt", "arn:aws:kms:us-east-1:1:key/k", map[string]string{"kms:ViaService": "s3.us-east-1.amazonaws.com"}}, "allow"},
		{evalReq{"KMS:decrypt", "arn:aws:kms:us-east-1:1:key/k", nil}, "deny"}, // action names are case-insensitive
		{evalReq{"s3:ListBucket", "arn:aws:s3:::b", nil}, "allow"},
	} {
		if got := evaluate(t, p, c.r); got != c.want {
			t.Errorf("%+v: got %s want %s", c.r, got, c.want)
		}
	}
}

const (
	pubParam   = "arn:aws:ssm:ap-southeast-1::parameter/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-x86_64"
	pubPath    = "arn:aws:ssm:us-east-1::parameter/aws/service/ecs/optimized-ami/amazon-linux-2023/recommended"
	acctParam  = "arn:aws:ssm:ap-southeast-1:123456789012:parameter/culishop/db/password"
	acctAWSish = "arn:aws:ssm:ap-southeast-1:123456789012:parameter/aws/service/fake" // SSM refuses such names; denied anyway
	sharedPub  = "arn:aws:ssm:ap-southeast-1:999999999999:parameter/aws/service/x"
)

func TestElevatedPolicyPublicParametersReadable(t *testing.T) {
	el, err := ElevatedPolicy("aws", nil, Elevation{})
	if err != nil {
		t.Fatal(err)
	}
	inv, _ := SessionPolicy("aws", nil)
	for _, c := range []struct {
		action, res string
		elev, inv   string
	}{
		{"ssm:GetParameter", pubParam, "allow", "deny"},
		{"ssm:GetParameters", pubParam, "allow", "deny"},
		{"ssm:GetParametersByPath", pubPath, "allow", "deny"},
		{"ssm:GetParameterHistory", pubParam, "allow", "deny"},
		{"ssm:GetParameterHistory", acctParam, "deny", "deny"},
		// fail closed (#211): if AWS spelled a public parameter's ARN any other
		// way than an empty account field, the read is denied, never opened
		{"ssm:GetParameter", "arn:aws:ssm:us-east-1:aws:parameter/aws/service/x", "deny", "deny"},
		{"ssm:GetParameter", "arn:aws:ssm:us-east-1:*:parameter/aws/service/x", "deny", "deny"},
		{"ssm:GetParameter", "arn:aws:ssm:us-east-1::parameter/aws/serviceX", "deny", "deny"},
		{"ssm:GetParameter", "arn:aws:ssm:us-east-1::parameter/x/aws/service/y", "deny", "deny"},
		{"ssm:GetParameter", "arn:aws:ssm:us-east-1::parameter/culishop/aws/service", "deny", "deny"},
		{"ssm:GetParameter", acctParam, "deny", "deny"},
		{"ssm:GetParameters", acctParam, "deny", "deny"},
		{"ssm:GetParametersByPath", "arn:aws:ssm:ap-southeast-1:123456789012:parameter/culishop", "deny", "deny"},
		{"ssm:GetParameter", acctAWSish, "deny", "deny"},
		{"ssm:GetParameter", sharedPub, "deny", "deny"},
		{"ssm:GetParameter", "arn:aws:ssm:ap-southeast-1::parameter/aws/other", "deny", "deny"},
		// the carve-out is SSM only: other data reads are untouched
		{"secretsmanager:GetSecretValue", "arn:aws:secretsmanager:ap-southeast-1:123456789012:secret:x", "deny", "deny"},
		{"dynamodb:GetItem", "arn:aws:dynamodb:ap-southeast-1:123456789012:table/t", "deny", "deny"},
	} {
		if got := evaluate(t, el, evalReq{c.action, c.res, nil}); got != c.elev {
			t.Errorf("elevated %s %s: got %s want %s", c.action, c.res, got, c.elev)
		}
		if got := evaluate(t, inv, evalReq{c.action, c.res, nil}); got != c.inv {
			t.Errorf("investigate %s %s: got %s want %s", c.action, c.res, got, c.inv)
		}
	}
	cn, _ := ElevatedPolicy("aws-cn", nil, Elevation{})
	if got := evaluate(t, cn, evalReq{"ssm:GetParameter", "arn:aws-cn:ssm:cn-north-1::parameter/aws/service/x", nil}); got != "allow" {
		t.Errorf("aws-cn public parameter: %s", got)
	}
}

func TestElevatedPolicyBoxBucketPrefix(t *testing.T) {
	on, err := ElevatedPolicy("aws", []string{"culishop-tfstate"}, Elevation{BoxBucketPrefix: "culibox-"})
	if err != nil {
		t.Fatal(err)
	}
	off, _ := ElevatedPolicy("aws", []string{"culishop-tfstate"}, Elevation{})
	none, _ := ElevatedPolicy("aws", nil, Elevation{BoxBucketPrefix: "culibox-"})
	for _, c := range []struct {
		action, res   string
		on, off, none string
	}{
		{"s3:GetObject", "arn:aws:s3:::culibox-lab-1/terraform.tfstate", "allow", "deny", "allow"},
		{"s3:GetObjectVersion", "arn:aws:s3:::culibox-x/a/b/c.json", "allow", "deny", "allow"},
		{"s3:GetObject", "arn:aws:s3:::culishop-tfstate/env/prod.tfstate", "allow", "allow", "deny"},
		{"s3:GetObject", "arn:aws:s3:::human-data/culibox-x/y", "deny", "deny", "deny"},
		{"s3:GetObject", "arn:aws:s3:::culiboxer/y", "deny", "deny", "deny"},
		{"s3:GetObject", "arn:aws:s3:::x-culibox-y/z", "deny", "deny", "deny"},
		// the laundering paths: copies into a box bucket that never call
		// s3:GetObject as the session (a table export, or a job run by a passed role)
		{"dynamodb:ExportTableToPointInTime", "arn:aws:dynamodb:us-east-1:123456789012:table/orders", "deny", "allow", "deny"},
		{"rds:StartExportTask", "arn:aws:rds:us-east-1:123456789012:snapshot:prod-db", "deny", "allow", "deny"},
		{"s3:CreateJob", "*", "deny", "allow", "deny"},
		{"datasync:StartTaskExecution", "arn:aws:datasync:us-east-1:123456789012:task/task-1", "deny", "allow", "deny"},
		{"backup:StartRestoreJob", "arn:aws:backup:us-east-1:123456789012:recovery-point:rp-1", "deny", "allow", "deny"},
		{"s3:PutReplicationConfiguration", "arn:aws:s3:::human-data", "deny", "allow", "deny"},
		// replication FROM a box bucket only moves box data: allowed
		{"s3:PutReplicationConfiguration", "arn:aws:s3:::culibox-lab-1", "allow", "allow", "allow"},
		// everything else the elevated policy denies stays denied
		{"secretsmanager:GetSecretValue", "arn:aws:secretsmanager:us-east-1:123456789012:secret:culibox-x", "deny", "deny", "deny"},
		{"kms:Decrypt", "arn:aws:kms:us-east-1:123456789012:key/k", "deny", "deny", "deny"},
		{"s3:PutObject", "arn:aws:s3:::human-data/x", "allow", "allow", "allow"},
	} {
		for _, pc := range []struct{ name, pol, want string }{{"on", on, c.on}, {"off", off, c.off}, {"none", none, c.none}} {
			if got := evaluate(t, pc.pol, evalReq{c.action, c.res, nil}); got != pc.want {
				t.Errorf("%s: %s %s: got %s want %s", pc.name, c.action, c.res, got, pc.want)
			}
		}
	}
	// S3 decrypts box-bucket objects through the existing kms:ViaService exception.
	if got := evaluate(t, on, evalReq{"kms:Decrypt", "arn:aws:kms:us-east-1:123456789012:key/k",
		map[string]string{"kms:ViaService": "s3.us-east-1.amazonaws.com"}}); got != "allow" {
		t.Errorf("kms via s3: %s", got)
	}
	inv, _ := SessionPolicy("aws", []string{"culishop-tfstate"})
	if strings.Contains(inv, "culibox-") || strings.Contains(inv, "/aws/service/") || strings.Contains(inv, "Export") {
		t.Fatalf("the investigate policy must not carry any #211 carve-out: %s", inv)
	}
}

// The copy-path denies above are needed because iam:PassRole is allowed in the
// window on every role except agentbox-*: ECS and Lambda applies need it. If a
// later change denies PassRole, this test fails and the denies can be revisited.
func TestElevatedPolicyPassRoleIsWhyCopyPathsAreDenied(t *testing.T) {
	for _, iam := range []bool{false, true} {
		s, _ := ElevatedPolicy("aws", nil, Elevation{AllowIAM: iam, BoxBucketPrefix: "culibox-"})
		if got := evaluate(t, s, evalReq{"iam:PassRole", "arn:aws:iam::123456789012:role/s3-replication", nil}); got != "allow" {
			t.Errorf("allow_iam %v: PassRole on a non-agentbox role is %s; the copy-path reasoning assumes allow", iam, got)
		}
		if got := evaluate(t, s, evalReq{"iam:PassRole", "arn:aws:iam::123456789012:role/agentbox-agent-write", nil}); got != "deny" {
			t.Errorf("allow_iam %v: PassRole on the agent's own role is %s", iam, got)
		}
	}
}

func TestValidBoxBucketPrefix(t *testing.T) {
	for _, s := range []string{"culibox-", "lab-", "a1-", "culiops-sandbox-box-" + strings.Repeat("x", 19) + "-"} {
		if err := ValidBoxBucketPrefix(s); err != nil {
			t.Errorf("%q refused: %v", s, err)
		}
	}
	for _, s := range []string{"", "*", "-", "a-", "culibox", "culibox*", "culibox-*", "culibox?-", "Culibox-", "culibox.-",
		"-culibox-", "culibox/", "culibox-/x-", `culibox"-`, "culibox -", strings.Repeat("x", 40) + "-", "culibox-\n"} {
		if ValidBoxBucketPrefix(s) == nil {
			t.Errorf("%q accepted", s)
		}
		if _, err := ElevatedPolicy("aws", nil, Elevation{BoxBucketPrefix: s}); s != "" && err == nil {
			t.Errorf("ElevatedPolicy accepted prefix %q", s)
		}
	}
}

// The largest realistic elevated policy must fit STS's 2,048-character limit:
// the longest partition, IAM writes denied (more statements), the longest box
// prefix allowed, and an s3_read list of three long entries.
func TestElevatedPolicyLargestRealisticFits(t *testing.T) {
	prefix := strings.Repeat("p", 39) + "-" // 40, the maximum
	s3Read := []string{
		strings.Repeat("s", 63),                            // a max-length state bucket
		strings.Repeat("l", 50) + "/AWSLogs/123456789012/", // a log bucket, account prefix
		"culishop-artifacts-ap-southeast-1/releases/",
	}
	s, err := ElevatedPolicy("aws-us-gov", s3Read, Elevation{BoxBucketPrefix: prefix})
	if err != nil {
		t.Fatalf("largest realistic policy refused: %v", err)
	}
	t.Logf("largest realistic elevated policy: %d of %d characters", len(s), MaxPolicyChars)
	if len(s) > MaxPolicyChars {
		t.Fatalf("%d > %d", len(s), MaxPolicyChars)
	}
	// and the limit is still enforced with the prefix on
	var many []string
	for i := 0; i < 12; i++ {
		many = append(many, strings.Repeat("b", 60)+"/x/")
	}
	if _, err := ElevatedPolicy("aws", many, Elevation{BoxBucketPrefix: "culibox-"}); err == nil || !strings.Contains(err.Error(), "2048") {
		t.Fatalf("oversized policy must be refused, got %v", err)
	}
}
