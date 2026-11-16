package awsscope

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
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

// The equivalence test asks millions of times, so parsed policies are cached.
var parsedCache sync.Map

// glob matches s against an IAM pattern: "*" is any run of characters
// (crossing ":" and "/"), "?" is one character, everything else literal; fold
// ignores ASCII case. Iterative, with one backtrack point per "*".
func glob(pattern, s string, fold bool) bool {
	lower := func(b byte) byte {
		if fold && 'A' <= b && b <= 'Z' {
			return b + 'a' - 'A'
		}
		return b
	}
	p, i, star, mark := 0, 0, -1, 0
	for i < len(s) {
		switch {
		case p < len(pattern) && pattern[p] != '*' && (pattern[p] == '?' || lower(pattern[p]) == lower(s[i])):
			p++
			i++
		case p < len(pattern) && pattern[p] == '*':
			star, mark = p, i
			p++
		case star >= 0:
			p = star + 1
			mark++
			i = mark
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

func TestGlobMatchesTheRegexpForm(t *testing.T) {
	for _, c := range []struct{ p, s string }{
		{"s3:Put*Policy*", "s3:PutBucketPolicy"}, {"s3:Put*Policy*", "s3:PutAccessPointPolicyForObjectLambda"},
		{"s3:Put*Policy*", "s3:GetBucketPolicy"}, {"arn:aws:s3:::b/*", "arn:aws:s3:::b/x/y"}, {"arn:aws:s3:::b/*", "arn:aws:s3:::b"},
		{"*", ""}, {"a*b*c", "abc"}, {"a*b*c", "aXbYbZc"}, {"a*b*c", "acb"}, {"a?c", "abc"}, {"a?c", "ac"},
		{"arn:aws:ssm:*::parameter/aws/service/*", "arn:aws:ssm:us-east-1::parameter/aws/service/x"},
		{"arn:aws:ssm:*::parameter/aws/service/*", "arn:aws:ssm:us-east-1:1:parameter/aws/service/x"},
		{"x.y", "xzy"}, {"S3:GETOBJECT", "s3:GetObject"},
	} {
		for _, fold := range []bool{false, true} {
			re := "^" + strings.NewReplacer(`\*`, ".*", `\?`, ".").Replace(regexp.QuoteMeta(c.p)) + "$"
			if fold {
				re = "(?i)" + re
			}
			if want := regexp.MustCompile(re).MatchString(c.s); glob(c.p, c.s, fold) != want {
				t.Errorf("glob(%q, %q, %v) != %v", c.p, c.s, fold, want)
			}
		}
	}
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

// parseEvalPolicy decodes a policy for the evaluator and refuses any key it
// does not implement (NotAction, Principal, NotPrincipal, Sid, ...), so a
// future statement of another shape fails the tests instead of being
// evaluated wrongly. NotResource is implemented.
func parseEvalPolicy(policy string) ([]evalStmt, error) {
	dec := json.NewDecoder(strings.NewReader(policy))
	dec.DisallowUnknownFields()
	var doc struct {
		Version   string
		Statement []evalStmt
	}
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	for _, s := range doc.Statement {
		if s.Effect != "Allow" && s.Effect != "Deny" {
			return nil, fmt.Errorf("evaluator: Effect %q", s.Effect)
		}
		if s.Resource != nil && s.NotResource != nil {
			return nil, errors.New("evaluator: both Resource and NotResource")
		}
	}
	return doc.Statement, nil
}

func TestEvaluatorRefusesStatementsItDoesNotImplement(t *testing.T) {
	for _, p := range []string{
		`{"Statement":[{"Effect":"Deny","NotAction":"s3:*","Resource":"*"}]}`,
		`{"Statement":[{"Effect":"Deny","Action":"s3:*","Resource":"*","NotPrincipal":{"AWS":"x"}}]}`,
		`{"Statement":[{"Effect":"Deny","Action":"s3:*","Resource":"*","Principal":"*"}]}`,
		`{"Statement":[{"Sid":"x","Effect":"Deny","Action":"s3:*","Resource":"*"}]}`,
		`{"Statement":[{"Effect":"deny","Action":"s3:*","Resource":"*"}]}`,
		`{"Statement":[{"Effect":"Deny","Action":"s3:*","Resource":"*","NotResource":["x"]}]}`,
	} {
		if _, err := parseEvalPolicy(p); err == nil {
			t.Errorf("accepted %s", p)
		}
	}
	if _, err := parseEvalPolicy(`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"s3:*","NotResource":["x"]}]}`); err != nil {
		t.Error(err)
	}
}

// evaluate returns "allow", "implicit" (no Allow applies) or "deny" (an
// explicit Deny applies; it wins over every Allow).
func evaluate(t *testing.T, policy string, r evalReq) string {
	t.Helper()
	type parsed struct {
		evalStmt
		actions, resources []string
	}
	var st []parsed
	if v, ok := parsedCache.Load(policy); ok {
		st = v.([]parsed)
	} else {
		doc, err := parseEvalPolicy(policy)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range doc {
			st = append(st, parsed{s, strs(s.Action), strs(s.Resource)})
		}
		parsedCache.Store(policy, st)
	}
	allowed := false
	for _, s := range st {
		if !anyGlob(s.actions, r.action, true) {
			continue
		}
		if s.NotResource != nil {
			if anyGlob(s.NotResource, r.resource, false) {
				continue
			}
		} else if !anyGlob(s.resources, r.resource, false) {
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
		// the laundering paths: copies into a readable bucket that never call
		// s3:GetObject as the session (a table export, or a job run by a passed
		// role). "off" has an s3_read bucket, which is readable too (#301).
		{"dynamodb:ExportTableToPointInTime", "arn:aws:dynamodb:us-east-1:123456789012:table/orders", "deny", "deny", "deny"},
		{"rds:StartExportTask", "arn:aws:rds:us-east-1:123456789012:snapshot:prod-db", "deny", "deny", "deny"},
		{"s3:CreateJob", "*", "deny", "deny", "deny"},
		{"datasync:StartTaskExecution", "arn:aws:datasync:us-east-1:123456789012:task/task-1", "deny", "deny", "deny"},
		{"backup:StartRestoreJob", "arn:aws:backup:us-east-1:123456789012:recovery-point:rp-1", "deny", "deny", "deny"},
		{"s3:PutReplicationConfiguration", "arn:aws:s3:::human-data", "deny", "deny", "deny"},
		// replication FROM a box bucket only moves box data: allowed
		{"s3:PutReplicationConfiguration", "arn:aws:s3:::culibox-lab-1", "allow", "deny", "allow"},
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

// The copy-path denies above are needed because, with pass_role_prefix set,
// iam:PassRole stays allowed on roles named <prefix>* (ECS and Lambda applies
// need it) and a service running as such a role is not bound by the session
// policy. Without the prefix PassRole is denied (#302) and this reasoning does
// not apply, but the denies stay: removing them is a separate decision.
func TestElevatedPolicyPassRoleIsWhyCopyPathsAreDenied(t *testing.T) {
	for _, iam := range []bool{false, true} {
		s, _ := ElevatedPolicy("aws", nil, Elevation{AllowIAM: iam, BoxBucketPrefix: "culibox-", PassRolePrefix: "lab-"})
		if got := evaluate(t, s, evalReq{"iam:PassRole", "arn:aws:iam::123456789012:role/lab-s3-replication", nil}); got != "allow" {
			t.Errorf("allow_iam %v: PassRole on a prefixed role is %s; the copy-path reasoning assumes allow", iam, got)
		}
		if got := evaluate(t, s, evalReq{"iam:PassRole", "arn:aws:iam::123456789012:role/s3-replication", nil}); got != "deny" {
			t.Errorf("allow_iam %v: PassRole on another role is %s", iam, got)
		}
		if got := evaluate(t, s, evalReq{"iam:PassRole", "arn:aws:iam::123456789012:role/agentbox-agent-write", nil}); got != "deny" {
			t.Errorf("allow_iam %v: PassRole on the agent's own role is %s", iam, got)
		}
	}
}

// #301: the managed-copy denies are unconditional. An s3_read bucket alone is
// enough for an export into it to be read back, and even with nothing readable
// an export (no PassRole needed for DynamoDB) can target an outside or
// self-made public bucket that the agent then fetches unsigned.
func TestElevatedPolicyCopyDeniesFollowReadableBuckets(t *testing.T) {
	copies := []evalReq{
		{"dynamodb:ExportTableToPointInTime", "arn:aws:dynamodb:us-east-1:123456789012:table/orders", nil},
		{"rds:StartExportTask", "arn:aws:rds:us-east-1:123456789012:snapshot:prod-db", nil},
		{"s3:CreateJob", "*", nil},
		{"datasync:StartTaskExecution", "arn:aws:datasync:us-east-1:123456789012:task/task-1", nil},
		{"backup:StartRestoreJob", "arn:aws:backup:us-east-1:123456789012:recovery-point:rp-1", nil},
		{"s3:PutReplicationConfiguration", "arn:aws:s3:::human-data", nil},
	}
	for _, c := range []struct {
		name string
		s3   []string
		el   Elevation
		want string
	}{
		{"neither", nil, Elevation{}, "deny"},
		{"s3_read only", []string{"culishop-tfstate"}, Elevation{}, "deny"},
		{"s3_read prefix entry only", []string{"culishop-artifacts/releases/"}, Elevation{AllowIAM: true}, "deny"},
		{"prefix only", nil, Elevation{BoxBucketPrefix: "culibox-"}, "deny"},
		{"both", []string{"culishop-tfstate"}, Elevation{BoxBucketPrefix: "culibox-"}, "deny"},
	} {
		s, err := ElevatedPolicy("aws", c.s3, c.el)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range copies {
			if got := evaluate(t, s, r); got != c.want {
				t.Errorf("%s: %s = %s want %s", c.name, r.action, got, c.want)
			}
		}
	}
	// Replication FROM a bucket is excepted only for the buckets named by
	// box_bucket_prefix; an s3_read entry may be a prefix of a bucket, so without
	// the prefix every replication change is denied.
	s, _ := ElevatedPolicy("aws", []string{"culishop-tfstate"}, Elevation{})
	if got := evaluate(t, s, evalReq{"s3:PutReplicationConfiguration", "arn:aws:s3:::culishop-tfstate", nil}); got != "deny" {
		t.Errorf("replication from an s3_read bucket with no prefix: %s", got)
	}
	s, _ = ElevatedPolicy("aws", []string{"culishop-tfstate"}, Elevation{BoxBucketPrefix: "culibox-"})
	if got := evaluate(t, s, evalReq{"s3:PutReplicationConfiguration", "arn:aws:s3:::culibox-x", nil}); got != "allow" {
		t.Errorf("replication from a box bucket: %s", got)
	}
	if got := evaluate(t, s, evalReq{"s3:PutReplicationConfiguration", "arn:aws:s3:::culishop-tfstate", nil}); got != "deny" {
		t.Errorf("replication from an s3_read bucket with a prefix set: %s", got)
	}
}

func TestValidBoxBucketPrefixRefusesSharedStems(t *testing.T) {
	for _, s := range []string{"aws-", "awsx-", "cdk-", "cdk-x-", "sagemaker-", "sagemaker-eu-", "elasticbeanstalk-", "amplify-", "amplify-app-",
		"cf-templates-", "cf-templates-abc-", "codepipeline-", "config-bucket-", "agentbox-", "agentbox-lab-"} {
		err := ValidBoxBucketPrefix(s)
		if err == nil || !strings.Contains(err.Error(), "overlaps") {
			t.Errorf("%q: want an overlap refusal, got %v", s, err)
		}
	}
	// upper case never reaches the stem check: the shape check refuses it
	if ValidBoxBucketPrefix("AWS-") == nil || ValidBoxBucketPrefix("CDK-") == nil {
		t.Error("upper-case reserved prefix accepted")
	}
	for _, s := range []string{"culibox-", "lab-", "ampl-", "cdx-", "cf-x-"} {
		if err := ValidBoxBucketPrefix(s); err != nil {
			t.Errorf("%q refused: %v", s, err)
		}
	}
}

func TestElevatedPolicySizeErrorNamesTheElevatedPolicyAndWhatGrowsIt(t *testing.T) {
	var many []string
	for i := 0; i < 12; i++ {
		many = append(many, strings.Repeat("b", 60)+"/x/")
	}
	_, err := ElevatedPolicy("aws", many, Elevation{})
	if err == nil {
		t.Fatal("oversized policy accepted")
	}
	for _, w := range []string{"elevated session policy", "2048", "s3_read", "box_bucket_prefix", "pass_role_prefix", "allow_iam"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("size error lacks %q: %v", w, err)
		}
	}
	// #329: it names the size, the limit, and the part the config does not
	// control, and says an upgrade can be the cause (a newer CuliBox may deny more).
	base, _ := ElevatedPolicy("aws", nil, Elevation{})
	big := fmt.Sprintf("the elevated session policy is %d characters; STS allows 2048", sizeOf(t, "aws", many, Elevation{}))
	for _, w := range []string{big, fmt.Sprintf("CuliBox's own denies take %d of them (with allow_iam off)", len(base)), "upgrade"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("size error lacks %q: %v", w, err)
		}
	}
	baseIAM, _ := ElevatedPolicy("aws-us-gov", nil, Elevation{AllowIAM: true})
	_, err = ElevatedPolicy("aws-us-gov", many, Elevation{AllowIAM: true, BoxBucketPrefix: "culibox-"})
	if w := fmt.Sprintf("CuliBox's own denies take %d of them (with allow_iam on)", len(baseIAM)); err == nil || !strings.Contains(err.Error(), w) {
		t.Errorf("size error lacks %q: %v", w, err)
	}
}

// sizeOf renders the policy without the size limit, to name its size.
func sizeOf(t *testing.T, partition string, s3Read []string, el Elevation) int {
	t.Helper()
	_, err := ElevatedPolicy(partition, s3Read, el)
	var se *sizeError
	if !errors.As(err, &se) {
		t.Fatalf("not a size error: %v", err)
	}
	return se.n
}

// #329: each action pack's pattern matches exactly the catalog actions its
// covers match, so writing the pattern changes no decision.
func TestActionPacksAreExact(t *testing.T) {
	for _, p := range actionPacks {
		var want []string
		for _, c := range p.covers {
			want = append(want, expandCatalog(t, c)...)
		}
		slices.Sort(want)
		got := expandCatalog(t, p.pattern)
		if len(got) == 0 || !slices.Equal(got, want) {
			t.Errorf("%s expands to %v; its covers %v to %v", p.pattern, got, p.covers, want)
		}
	}
}

func TestPackActions(t *testing.T) {
	in := []string{"a:X", "dynamodb:BatchGetItem", "b:Y", "dynamodb:GetItem", "iam:Attach*", "c:Z"}
	got := packActions(in)
	if want := []string{"a:X", "dynamodb:*GetItem", "b:Y", "iam:Attach*", "c:Z"}; !slices.Equal(got, want) {
		t.Fatalf("got %v want %v (a pack applies only when all its covers are there)", got, want)
	}
	if !slices.Equal(in, []string{"a:X", "dynamodb:BatchGetItem", "b:Y", "dynamodb:GetItem", "iam:Attach*", "c:Z"}) {
		t.Fatal("packActions changed its input")
	}
}

// #329: the elevated policy writes the four SSM parameter reads as one
// pattern. AWS's catalog has exactly these four actions under it, so the
// pattern denies nothing more.
func TestSSMParameterReadPatternIsExactlyTheFourReads(t *testing.T) {
	got := expandCatalog(t, ssmParamReadsPattern)
	want := []string{"ssm:GetParameter", "ssm:GetParameterHistory", "ssm:GetParameters", "ssm:GetParametersByPath"}
	if !slices.Equal(got, want) {
		t.Fatalf("%s expands to %v in the catalog, want %v", ssmParamReadsPattern, got, want)
	}
	for _, a := range ssmParamReads {
		if !slices.Contains(got, a) {
			t.Errorf("ssmParamReads has %s, which the pattern misses", a)
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

// The largest realistic elevated policy must fit STS's 2,048-character limit.
// Each size is pinned, so a change that grows the policy says by how much, and
// the headroom left for the next deny is visible here (#329).
//   - smallest: no s3_read, no options, IAM writes denied;
//   - one: one 63-character s3_read bucket, both prefixes at their 40-character
//     maximum, the longest partition;
//   - realistic: three long s3_read entries, 8-character prefixes, the longest
//     partition;
//   - all-maximum: three long entries AND both prefixes at 40 characters.
//
// History (all-maximum / realistic): 2,069 (refused) / 1,973 before #307;
// 2,024 / 1,928 after #307; 1,821 / 1,725 after #329 packed the statements;
// 1,860 / 1,764 with the Access Grants denies (#322); 1,940 / 1,844 with the
// S3 Express denies (#323); 1,880 / 1,784 with the action packs (#329);
// 2,036 / 1,940 with the resource-policy denies (#324); 2,034 / 1,938 with the
// guard written sts:AssumeRo* (AssumeRoot, #329 review). 14 characters are left
// at the all-maximum config: the next deny does not fit there.
func TestElevatedPolicyLargestRealisticFits(t *testing.T) {
	maxEl := Elevation{BoxBucketPrefix: longBoxPrefix, PassRolePrefix: longRolePrefix}
	realEl := Elevation{BoxBucketPrefix: "culibox-", PassRolePrefix: "culibox-"}
	for _, c := range []struct {
		name      string
		partition string
		s3Read    []string
		el        Elevation
		want      int
	}{
		{"smallest", "aws", nil, Elevation{}, 1523},
		{"smallest, allow_iam on", "aws", nil, Elevation{AllowIAM: true}, 1487},
		{"one", "aws-us-gov", longS3Read[:1], maxEl, 1871},
		{"realistic", "aws-us-gov", longS3Read, realEl, 1938},
		{"all-maximum", "aws-us-gov", longS3Read, maxEl, 2034},
	} {
		s, err := ElevatedPolicy(c.partition, c.s3Read, c.el)
		if err != nil {
			t.Errorf("%s: refused: %v", c.name, err)
			continue
		}
		t.Logf("%s: %d of %d characters (%d left)", c.name, len(s), MaxPolicyChars, MaxPolicyChars-len(s))
		if len(s) != c.want {
			t.Errorf("%s: %d characters, pinned %d: update the pin and the history above", c.name, len(s), c.want)
		}
		for _, d := range []string{`"s3:Put*Acl"`, `"s3:PutAccountPublicAccessBlock"`, `"dynamodb:Export*"`} {
			if !strings.Contains(s, d) {
				t.Errorf("%s: %s missing", c.name, d)
			}
		}
	}
	// Long entries on top of the all-maximum: refused once over, naming the
	// elevated policy.
	more := slices.Clone(longS3Read)
	for i := 0; ; i++ {
		more = append(more, strings.Repeat(string(rune('a'+i)), 63))
		_, err := ElevatedPolicy("aws-us-gov", more, maxEl)
		if err != nil {
			if !strings.Contains(err.Error(), "elevated session policy") || !strings.Contains(err.Error(), "2048") {
				t.Fatalf("an over-limit policy must be refused with the elevated-policy size error, got %v", err)
			}
			break
		}
		if i == 10 {
			t.Fatal("ten more long entries still fit: the size check is not reached")
		}
	}
	// and the limit is still enforced with the prefix on
	var many []string
	for i := 0; i < 20; i++ {
		many = append(many, strings.Repeat("b", 60)+"/x/")
	}
	if _, err := ElevatedPolicy("aws", many, Elevation{BoxBucketPrefix: "culibox-"}); err == nil || !strings.Contains(err.Error(), "2048") {
		t.Fatalf("oversized policy must be refused, got %v", err)
	}
}

// #307: in a write window the session may not open S3 data to the internet or
// to another account. Bucket and object ACLs, bucket (and access point) policies,
// the bucket's public access block and its object-ownership setting are denied,
// except on box buckets (elevate.box_bucket_prefix), which hold what the box
// made. The account-level public access block has no bucket, so it is denied
// with or without the prefix.
func TestElevatedPolicyDeniesS3ExposureExceptOnBoxBuckets(t *testing.T) {
	on, err := ElevatedPolicy("aws", []string{"culishop-tfstate"}, Elevation{BoxBucketPrefix: "culibox-"})
	if err != nil {
		t.Fatal(err)
	}
	off, _ := ElevatedPolicy("aws", []string{"culishop-tfstate"}, Elevation{})
	none, _ := ElevatedPolicy("aws", nil, Elevation{BoxBucketPrefix: "culibox-", PassRolePrefix: "lab-", AllowIAM: true})
	neither, _ := ElevatedPolicy("aws", nil, Elevation{AllowIAM: true})
	const (
		human    = "arn:aws:s3:::human-data"
		humanObj = "arn:aws:s3:::human-data/reports/q3.csv"
		box      = "arn:aws:s3:::culibox-lab-1"
		boxObj   = "arn:aws:s3:::culibox-lab-1/site/index.html"
		readable = "arn:aws:s3:::culishop-tfstate"
		ap       = "arn:aws:s3:us-east-1:123456789012:accesspoint/culibox-ap"
		mrap     = "arn:aws:s3::123456789012:accesspoint/abc123.mrap"
	)
	type row struct {
		action, res string
		box         bool // allowed when a box_bucket_prefix covers res
	}
	rows := []row{
		{"s3:PutBucketPolicy", human, false},
		{"s3:PutBucketPolicy", readable, false}, // an s3_read entry is not an exception
		{"s3:PutBucketPolicy", box, true},
		{"s3:DeleteBucketPolicy", human, false},
		{"s3:DeleteBucketPolicy", box, true},
		{"s3:PutBucketAcl", human, false},
		{"s3:PutBucketAcl", box, true},
		{"s3:PutObjectAcl", humanObj, false},
		{"s3:PutObjectAcl", boxObj, true},
		{"s3:PutObjectVersionAcl", humanObj, false},
		{"s3:PutObjectVersionAcl", boxObj, true},
		// PutPublicAccessBlock and DeletePublicAccessBlock both need this action
		{"s3:PutBucketPublicAccessBlock", human, false},
		{"s3:PutBucketPublicAccessBlock", box, true},
		// Put and Delete of ownership controls both need this action; either can re-enable ACLs
		{"s3:PutBucketOwnershipControls", human, false},
		{"s3:PutBucketOwnershipControls", box, true},
		// access point, Multi-Region Access Point and Access Grants policies: no bucket name, never excepted
		{"s3:PutAccessPointPolicy", ap, false},
		{"s3:PutAccessPointPolicyForObjectLambda", "arn:aws:s3-object-lambda:us-east-1:123456789012:accesspoint/culibox-olap", false},
		{"s3:PutMultiRegionAccessPointPolicy", mrap, false},
		{"s3:PutAccessGrantsInstanceResourcePolicy", "arn:aws:s3:us-east-1:123456789012:access-grants/default", false},
		// the account-level block has no resource: denied whatever the prefix
		{"s3:PutAccountPublicAccessBlock", "*", false},
		// look-alike names stay denied
		{"s3:PutBucketPolicy", "arn:aws:s3:::culiboxer", false},
		{"s3:PutBucketPolicy", "arn:aws:s3:::x-culibox-y", false},
		{"s3:PutObjectAcl", "arn:aws:s3:::human-data/culibox-x/y", false},
	}
	for _, r := range rows {
		for _, pc := range []struct {
			name, pol string
			prefix    bool
		}{{"on", on, true}, {"off", off, false}, {"none", none, true}, {"neither", neither, false}} {
			want := "deny"
			if r.box && pc.prefix {
				want = "allow"
			}
			if got := evaluate(t, pc.pol, evalReq{r.action, r.res, nil}); got != want {
				t.Errorf("%s: %s %s: got %s want %s", pc.name, r.action, r.res, got, want)
			}
		}
	}
	// The wildcards (s3:Put*Policy*, s3:Put*Acl) must not catch reads or the
	// ordinary writes a Terraform apply makes on any bucket.
	for _, a := range []string{"s3:GetBucketPolicy", "s3:GetBucketPolicyStatus", "s3:GetBucketAcl", "s3:GetObjectAcl",
		"s3:GetObjectVersionAcl", "s3:GetBucketPublicAccessBlock", "s3:GetAccountPublicAccessBlock",
		"s3:GetBucketOwnershipControls", "s3:GetAccessPointPolicy", "s3:ListBucket", "s3:CreateBucket", "s3:DeleteBucket",
		"s3:PutObject", "s3:DeleteObject", "s3:PutBucketTagging", "s3:PutBucketVersioning", "s3:PutEncryptionConfiguration",
		"s3:PutLifecycleConfiguration", "s3:PutBucketLogging", "s3:PutBucketCORS", "s3:PutBucketWebsite",
		"s3:PutBucketNotification", "s3:PutObjectTagging", "s3:PutBucketObjectLockConfiguration",
		"s3:PutX", // not a real action: guards against a wildcard swallowing Put* (s3:Put*Replication* would catch a PutReplication...X, #329)
		"s3:PutBucketRequestPayment", "s3:PutIntelligentTieringConfiguration", "s3:PutAccelerateConfiguration",
		"s3:PutMetricsConfiguration", "s3:PutInventoryConfiguration", "s3:PutAnalyticsConfiguration"} {
		for _, pc := range []struct{ name, pol string }{{"on", on}, {"off", off}, {"none", none}, {"neither", neither}} {
			if got := evaluate(t, pc.pol, evalReq{a, human, nil}); got != "allow" {
				t.Errorf("%s: %s on a human bucket: got %s want allow", pc.name, a, got)
			}
		}
	}
	// The investigate policy is not touched: the read role cannot write anyway.
	inv, _ := SessionPolicy("aws", []string{"culishop-tfstate"})
	if strings.Contains(inv, "Acl") || strings.Contains(inv, "PublicAccessBlock") || strings.Contains(inv, "Policy\"") {
		t.Fatalf("the investigate policy must not carry the #307 denies: %s", inv)
	}
}

// #307: the policy text names each exposure action (or the wildcard that
// covers it) exactly once per mode, so a later edit cannot drop one silently.
func TestElevatedPolicyS3ExposureActionsPinned(t *testing.T) {
	want := []string{`"s3:Put*Policy*"`, `"s3:DeleteBucketPolicy"`, `"s3:Put*Acl"`, `"s3:PutBucketPublicAccessBlock"`,
		`"s3:PutBucketOwnershipControls"`, `"s3:PutAccountPublicAccessBlock"`}
	for _, el := range []Elevation{{}, {BoxBucketPrefix: "culibox-"}, {AllowIAM: true, PassRolePrefix: "lab-"}} {
		s, err := ElevatedPolicy("aws", nil, el)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range want {
			if strings.Count(s, w) != 1 {
				t.Errorf("%+v: %s appears %d times: %s", el, w, strings.Count(s, w), s)
			}
		}
	}
}
