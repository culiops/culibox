package awsprobe

import (
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func lowerSorted(in []string) []string {
	var out []string
	for _, s := range in {
		out = append(out, strings.ToLower(s))
	}
	slices.Sort(out)
	return out
}

func TestMatchAction(t *testing.T) {
	for _, c := range []struct {
		pattern, action string
		want            bool
	}{
		{"s3:Get*", "S3:GETOBJECT", true},
		{"sqs:Receive*", "sqs:ReceiveMessage", true},
		{"s3:G?t*", "s3:GetObject", true},
		{"ec2:Describe*", "ec2:RunInstances", false},
		{"*", "iam:CreateUser", true},
		{"s3:GetObject", "s3:GetObjectAcl", false},
		{"s3:Get?", "s3:Get", false},
		{"ec2:*Instances", "ec2:RunInstances", true},
	} {
		if got := MatchAction(c.pattern, c.action); got != c.want {
			t.Errorf("MatchAction(%q, %q) = %v, want %v", c.pattern, c.action, got, c.want)
		}
	}
}

// guardrailResidual is the analysis output (analyze.py on ReadOnlyAccess +
// the company guardrail): the non-read-verb patterns no Deny removes.
var guardrailResidual = []string{
	"apigateway:GET", "cloudwatch:GenerateQuery", "cloudwatch:GenerateQueryResultsSummary", "ecr:BatchCheck*",
	"events:Test*", "iam:Generate*", "iam:Simulate*", "logs:FilterLogEvents", "logs:StartQuery",
	"logs:StopLiveTail", "logs:StopQuery", "logs:TestMetricFilter", "rds:Download*",
	"servicediscovery:DiscoverInstances", "servicediscovery:DiscoverInstancesRevision", "route53:Test*",
	"servicequotas:StartQuotaUtilizationReport", "sns:Check*", "sqs:Receive*", "wafv2:CheckCapacity",
}

// nonRead keeps the patterns whose literal verb is not a read verb.
func nonRead(patterns []string) []string {
	var out []string
	for _, p := range patterns {
		if !isReadVerb(p) {
			out = append(out, p)
		}
	}
	return out
}

// analysisNonRead reproduces the analysis script's filter, which compared verb
// prefixes case-sensitively and so reported apigateway:GET (a read verb to the
// gate) among the non-read patterns.
func analysisNonRead(patterns []string) []string {
	var out []string
	for _, p := range patterns {
		_, verb, _ := strings.Cut(literal(p), ":")
		read := false
		for _, v := range []string{"Get", "List", "Describe", "BatchGet", "BatchDescribe", "Search", "Lookup",
			"View", "Query", "Scan", "Select"} {
			read = read || strings.HasPrefix(verb, v)
		}
		if !read {
			out = append(out, p)
		}
	}
	return out
}

func TestResidualGuardrail(t *testing.T) {
	res, err := Residual([]string{fixture(t, "ReadOnlyAccess.json"), fixture(t, "guardrail.json")})
	if err != nil {
		t.Fatal(err)
	}
	// Residual returns every surviving pattern, read verbs included (later gate
	// steps expand them); the analysis counted only the non-read part.
	if got, want := lowerSorted(analysisNonRead(res)), lowerSorted(guardrailResidual); !reflect.DeepEqual(got, want) {
		t.Errorf("non-read residual =\n%v\nwant\n%v", got, want)
	}
	if got := len(nonRead(res)); got != 19 || !isReadVerb("apigateway:GET") {
		t.Errorf("gate's non-read residual has %d patterns, want 19 (apigateway:GET is a read)", got)
	}
	// Read patterns of allowlisted services survive; denied services do not.
	low := lowerSorted(res)
	for _, p := range []string{"ec2:describe*", "s3:get*"} {
		if !slices.Contains(low, p) {
			t.Errorf("residual lacks %s", p)
		}
	}
	for _, p := range res {
		svc, _, _ := strings.Cut(strings.ToLower(p), ":")
		if svc == "dynamodb" || svc == "athena" || svc == "redshift" {
			t.Errorf("residual keeps %s, whose service the NotAction deny excludes", p)
		}
	}
	for _, p := range res {
		if strings.EqualFold(p, "lambda:GetFunction") || strings.EqualFold(p, "ec2:GetPasswordData") {
			t.Errorf("residual keeps %s, which the guardrail denies concretely", p)
		}
	}
}

const allowDoc = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["sqs:Receive*","lambda:Get*","s3:Get*"],"Resource":"*"}]}`

func residualOf(t *testing.T, docs ...string) []string {
	t.Helper()
	res, err := Residual(docs)
	if err != nil {
		t.Fatal(err)
	}
	return lowerSorted(res)
}

func TestResidualConservative(t *testing.T) {
	all := []string{"lambda:get*", "s3:get*", "sqs:receive*"}
	for _, c := range []struct {
		name, deny string
		want       []string
	}{
		{"concrete deny leaves the broader allow",
			`{"Statement":{"Effect":"Deny","Action":"sqs:ReceiveMessage","Resource":"*"}}`, all},
		{"conditional deny removes nothing",
			`{"Statement":{"Effect":"Deny","Action":"*","Resource":"*","Condition":{"Bool":{"aws:SecureTransport":"false"}}}}`, all},
		{"NotResource deny removes nothing",
			`{"Statement":{"Effect":"Deny","Action":"*","NotResource":"arn:aws:s3:::example-waf-logs/*"}}`, all},
		{"narrower resource removes nothing",
			`{"Statement":{"Effect":"Deny","Action":"*","Resource":"arn:aws:s3:::example-cloudfront-logs/*"}}`, all},
		{"service wildcard deny covers a narrower allow",
			`{"Statement":{"Effect":"Deny","Action":"lambda:*","Resource":"*"}}`, []string{"s3:get*", "sqs:receive*"}},
		{"equal pattern, any case",
			`{"Statement":{"Effect":"Deny","Action":"SQS:RECEIVE*","Resource":"*"}}`, []string{"lambda:get*", "s3:get*"}},
		{"star covers everything",
			`{"Statement":{"Effect":"Deny","Action":"*","Resource":["*"]}}`, nil},
		{"a deny with a middle wildcard is not trusted",
			`{"Statement":{"Effect":"Deny","Action":"s3:G*t*","Resource":"*"}}`, all},
		{"a deny narrower than the allow keeps it",
			`{"Statement":{"Effect":"Deny","Action":"s3:GetObject*","Resource":"*"}}`, all},
		{"NotAction deny: listed service survives, others go",
			`{"Statement":{"Effect":"Deny","NotAction":["s3:*","SQS:ReceiveMessage"],"Resource":"*"}}`, []string{"s3:get*", "sqs:receive*"}},
		{"conditional NotAction deny removes nothing",
			`{"Statement":{"Effect":"Deny","NotAction":"s3:*","Resource":"*","Condition":{"Bool":{"aws:ViaAWSService":"false"}}}}`, all},
		{"an empty Condition still counts as conditional",
			`{"Statement":{"Effect":"Deny","Action":"*","Resource":"*","Condition":{}}}`, all},
		{"a NotAction entry with a service wildcard shares every matching service",
			`{"Statement":{"Effect":"Deny","NotAction":"s*:Get*","Resource":"*"}}`, []string{"s3:get*", "sqs:receive*"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := residualOf(t, allowDoc, c.deny); !reflect.DeepEqual(got, c.want) {
				t.Errorf("residual = %v, want %v", got, c.want)
			}
		})
	}
}

func TestResidualServiceWildcardAllowSurvivesNotActionDeny(t *testing.T) {
	allow := `{"Statement":{"Effect":"Allow","Action":["*","s*:Get*"],"Resource":"*"}}`
	deny := `{"Statement":{"Effect":"Deny","NotAction":"s3:*","Resource":"*"}}`
	if got, want := residualOf(t, allow, deny), []string{"*", "s*:get*"}; !reflect.DeepEqual(got, want) {
		t.Errorf("residual = %v, want %v", got, want)
	}
}

func TestResidualRefusesAllowNotAction(t *testing.T) {
	doc := `{"Statement":[{"Effect":"Allow","NotAction":"iam:*","Resource":"*"}]}`
	if _, err := Residual([]string{doc}); err == nil {
		t.Fatal("an Allow with NotAction must be an error")
	}
}

func TestResidualRefusesMalformed(t *testing.T) {
	for _, doc := range []string{
		`not json`,
		`{"Statement":[{"Effect":"Allow","Action":["s3:Get*",7]}]}`,
		`{"Statement":[{"Effect":"Allow","Resource":"*"}]}`,
		`{"Statement":[{"Effect":"Permit","Action":"s3:Get*"}]}`,
		`{}`,
		`{"Statement":"s3:Get*"}`,
		`{"Statement":[{"Effect":"Deny","Action":"*","NotAction":"s3:*","Resource":"*"}]}`,
		`{"Statement":[{"Effect":"Allow","Action":"s3:Get*","action":"s3:*"}]}`,
	} {
		if _, err := Residual([]string{doc}); err == nil {
			t.Errorf("Residual(%s) must be an error", doc)
		}
	}
}

func TestResidualDecodesURLEncoded(t *testing.T) {
	enc := url.PathEscape(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"logs:StartQuery","Resource":"*"}]}`)
	if !strings.HasPrefix(enc, "%7B") {
		t.Fatalf("test encoding is not URL-encoded: %s", enc)
	}
	if got, want := residualOf(t, enc), []string{"logs:startquery"}; !reflect.DeepEqual(got, want) {
		t.Errorf("residual = %v, want %v", got, want)
	}
	// The guardrail as GetRolePolicy returns it.
	enc = url.QueryEscape(fixture(t, "guardrail.json"))
	enc = strings.ReplaceAll(enc, "+", "%20")
	res, err := Residual([]string{fixture(t, "ReadOnlyAccess.json"), enc})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := lowerSorted(analysisNonRead(res)), lowerSorted(guardrailResidual); !reflect.DeepEqual(got, want) {
		t.Errorf("non-read residual from encoded guardrail = %v", got)
	}
}

func TestResidualShapes(t *testing.T) {
	// Statement as an object, Action as a string, duplicates in mixed case.
	doc := `{"Statement":{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::example-waf-logs/*"}}`
	doc2 := `{"Statement":[{"Effect":"Allow","Action":["S3:GETOBJECT","sns:Check*"]}]}`
	if got, want := residualOf(t, doc, doc2), []string{"s3:getobject", "sns:check*"}; !reflect.DeepEqual(got, want) {
		t.Errorf("residual = %v, want %v", got, want)
	}
}

func TestClassifyGuardrailPasses(t *testing.T) {
	res, err := Residual([]string{fixture(t, "ReadOnlyAccess.json"), fixture(t, "guardrail.json")})
	if err != nil {
		t.Fatal(err)
	}
	c := ClassifyResidual(res)
	if len(c.Refused) != 0 {
		t.Errorf("Refused = %v, want none", c.Refused)
	}
	if want := map[string][]string{"sqs:Receive*": {"sqs:ReceiveMessage"}}; !reflect.DeepEqual(c.SideEffect, want) {
		t.Errorf("SideEffect = %v, want %v", c.SideEffect, want)
	}
}

func TestClassifyRefuses(t *testing.T) {
	in := []string{"*", "s3:*", "ssm:SendCommand", "lambda:InvokeFunction", "foo:Frobnicate",
		"*:Get*", "s3*:Get*", "s3:G?t*", "ec2", "ec2:Desc*", "ec2:Describe*", "apigateway:GET"}
	c := ClassifyResidual(in)
	refused := map[string]string{}
	for _, v := range c.Refused {
		if v.Why == "" {
			t.Errorf("%s refused without a reason", v.Pattern)
		}
		refused[v.Pattern] = v.Why
	}
	for _, p := range in[:10] {
		if _, ok := refused[p]; !ok {
			t.Errorf("%s not refused", p)
		}
	}
	for _, p := range in[10:] {
		if why, ok := refused[p]; ok {
			t.Errorf("%s refused: %s", p, why)
		}
	}
	if len(c.Refused) != 10 {
		t.Errorf("Refused = %v, want 10 entries", c.Refused)
	}
	if len(c.SideEffect) != 0 {
		t.Errorf("SideEffect = %v, want none", c.SideEffect)
	}
}

func TestClassifySideEffectAndBenign(t *testing.T) {
	c := ClassifyResidual([]string{"logs:StopQuery", "IAM:GENERATE*", "s3express:CreateSession", "sqs:receive*"})
	if len(c.Refused) != 0 {
		t.Errorf("Refused = %v", c.Refused)
	}
	want := map[string][]string{
		"s3express:CreateSession": {"s3express:CreateSession"},
		"sqs:receive*":            {"sqs:ReceiveMessage"},
	}
	if !reflect.DeepEqual(c.SideEffect, want) {
		t.Errorf("SideEffect = %v, want %v", c.SideEffect, want)
	}
}

func TestNonStandardCoversReadOnlyAccess(t *testing.T) {
	for _, name := range []string{"ReadOnlyAccess.json", "ViewOnlyAccess.json"} {
		res, err := Residual([]string{fixture(t, name)})
		if err != nil {
			t.Fatal(err)
		}
		if len(res) < 100 {
			t.Fatalf("%s: only %d patterns; fixture not read", name, len(res))
		}
		for _, p := range nonRead(res) {
			e, ok := NonStandard[strings.ToLower(p)]
			if !ok {
				t.Errorf("%s: %s has no NonStandard entry", name, p)
				continue
			}
			if e.Reason == "" {
				t.Errorf("%s: %s has no reason", name, p)
			}
		}
	}
}

func TestBenignRuleNamedVerbs(t *testing.T) {
	for _, p := range []string{"logs:StopQuery", "logs:StopLiveTail", "cloudwatch:GenerateQuery",
		"cloudwatch:GenerateQueryResultsSummary", "iam:Generate*", "servicequotas:StartQuotaUtilizationReport",
		"logs:StartQuery", "iam:Simulate*", "route53:Test*"} {
		e, ok := NonStandard[strings.ToLower(p)]
		if !ok || len(e.SideEffect) != 0 {
			t.Errorf("%s must be a benign entry, got %+v (present %v)", p, e, ok)
		}
	}
}

func TestNonStandardEntriesWellFormed(t *testing.T) {
	for k, e := range NonStandard {
		if k != strings.ToLower(k) {
			t.Errorf("key %q is not lowercase", k)
		}
		if isReadVerb(k) {
			t.Errorf("key %q has a read verb; the table is for non-read verbs only", k)
		}
		if !strings.HasPrefix(e.Reason, "benign rule: ") {
			t.Errorf("%s: reason does not cite the benign rule: %q", k, e.Reason)
		}
		benign := strings.HasPrefix(e.Reason, ruleBenign)
		if benign != (len(e.SideEffect) == 0) {
			t.Errorf("%s: reason clause %q disagrees with SideEffect %v", k, e.Reason, e.SideEffect)
		}
		for _, a := range e.SideEffect {
			if strings.ContainsAny(a, "*?") || !strings.Contains(a, ":") {
				t.Errorf("%s: side-effect action %q is not concrete", k, a)
			}
			if !MatchAction(k, a) {
				t.Errorf("%s: side-effect action %q is not covered by the pattern", k, a)
			}
		}
	}
}

func TestUnconditionalDenies(t *testing.T) {
	ds, err := UnconditionalDenies([]string{
		`{"Statement":[
			{"Effect":"Allow","Action":"*","Resource":"*"},
			{"Effect":"Deny","Action":["ec2:GetPasswordData","lambda:Get*"],"Resource":"*"},
			{"Effect":"Deny","NotAction":["s3:*","ec2:*","lambda:*","sqs:*","kms:*"],"Resource":["*"]},
			{"Effect":"Deny","Action":"sqs:ReceiveMessage","Resource":"*","Condition":{"Bool":{"aws:SecureTransport":"false"}}},
			{"Effect":"Deny","Action":"s3:GetObject","NotResource":"arn:aws:s3:::public/*"},
			{"Effect":"Deny","Action":"kms:Decrypt","Resource":"arn:aws:kms:*:*:key/*"}]}`,
		url.PathEscape(`{"Statement":{"Effect":"Deny","Action":"s3:PutObject","Resource":"*"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	for a, want := range map[string]bool{
		"ec2:GetPasswordData":   true,
		"EC2:getpassworddata":   true,
		"lambda:GetFunction":    true,
		"iam:CreateUser":        true, // NotAction: iam is not exempt
		"s3:PutObject":          true, // second (URL-encoded) document
		"ec2:DescribeInstances": false,
		"sqs:ReceiveMessage":    false, // conditional
		"s3:GetObject":          false, // NotResource
		"kms:Decrypt":           false, // narrower Resource
	} {
		if got := ds.Denies(a); got != want {
			t.Errorf("Denies(%s) = %v, want %v", a, got, want)
		}
	}
	if _, err := UnconditionalDenies([]string{"{nope"}); err == nil {
		t.Error("unparseable document: want error")
	}
	// A NotAction Deny with an exempt entry without a service exempts all.
	ds, _ = UnconditionalDenies([]string{`{"Statement":{"Effect":"Deny","NotAction":["*"],"Resource":"*"}}`})
	if ds.Denies("iam:CreateUser") {
		t.Error(`NotAction ["*"] denies nothing`)
	}
}
