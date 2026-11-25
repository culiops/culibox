package awsprobe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

const (
	gateAcct   = "123456789012"
	gateRole   = "AWSReservedSSO_ReadOnly_0123456789abcdef"
	gateCaller = "arn:aws:sts::" + gateAcct + ":assumed-role/" + gateRole + "/someone@example.com"
	gateARN    = "arn:aws:iam::" + gateAcct + ":role/aws-reserved/sso.amazonaws.com/ap-southeast-1/" + gateRole
)

// fakeGate is a GateAPI whose simulation answers every requested action with
// def unless dec names it (keys lowercased).
type fakeGate struct {
	caller, role  string
	docs          []string
	callerErr     error
	roleErr       error
	docsErr       error
	simErr        error
	def           SimDecision
	dec           map[string]SimDecision
	omit          map[string]bool // lowercased actions left out of the reply
	simulated     []string
	simulatedRole string
	gotRoleName   string
}

func newFakeGate(t *testing.T, fixtures ...string) *fakeGate {
	f := &fakeGate{caller: gateCaller, role: gateARN, def: SimDecision{Decision: "explicitDeny"}, dec: map[string]SimDecision{}}
	for _, n := range fixtures {
		f.docs = append(f.docs, fixture(t, n))
	}
	return f
}

func (f *fakeGate) set(d SimDecision, actions ...string) {
	for _, a := range actions {
		f.dec[strings.ToLower(a)] = d
	}
}

func (f *fakeGate) CallerARN(ctx context.Context) (string, error) {
	if f.callerErr != nil {
		return "", f.callerErr
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return f.caller, nil
}

func (f *fakeGate) RoleARN(_ context.Context, name string) (string, error) {
	f.gotRoleName = name
	return f.role, f.roleErr
}

func (f *fakeGate) PolicyDocs(context.Context, string) ([]string, error) {
	return f.docs, f.docsErr
}

func (f *fakeGate) Simulate(_ context.Context, roleARN string, actions []string) (map[string]SimDecision, error) {
	f.simulatedRole = roleARN
	f.simulated = actions
	if f.simErr != nil {
		return nil, f.simErr
	}
	out := map[string]SimDecision{}
	for _, a := range actions {
		k := strings.ToLower(a)
		if f.omit[k] {
			continue
		}
		d, ok := f.dec[k]
		if !ok {
			d = f.def
		}
		out[a] = d
	}
	return out, nil
}

var (
	gateAllowed  = SimDecision{Decision: "allowed"}
	gateImplicit = SimDecision{Decision: "implicitDeny"}
)

func refusedPatterns(r GateResult) []string {
	var out []string
	for _, v := range r.Refused {
		out = append(out, strings.ToLower(v.Pattern))
	}
	return out
}

// denyDoc is an unconditional Resource "*" Deny of actions: the only kind of
// statement that backs a simulated explicitDeny.
func denyDoc(actions ...string) string {
	b, _ := json.Marshal(map[string]any{"Statement": map[string]any{"Effect": "Deny", "Action": actions, "Resource": "*"}})
	return string(b)
}

// guardrailExtended is everything the gate needs denied for ReadOnlyAccess
// plus the anonymised guardrail: sqs:ReceiveMessage, the minting screen, the
// WriteActions and the data reads.
func guardrailExtended() []string {
	return slices.Concat([]string{"sqs:ReceiveMessage"}, guardrailMustDeny, WriteActions, DataReadActions)
}

// newExtendedGuardrailGate is ReadOnlyAccess + the anonymised guardrail +
// an in-test unconditional Deny of guardrailExtended(), with a simulation that
// explicitDenies all of it. The committed guardrail fixture does not deny most
// of guardrailMustDeny, so in AWS the real permission set is refused until its
// guardrail is extended this way.
func newExtendedGuardrailGate(t *testing.T) *fakeGate {
	f := newFakeGate(t, "ReadOnlyAccess.json", "guardrail.json")
	f.docs = append(f.docs, denyDoc(guardrailExtended()...))
	f.set(SimDecision{Decision: "explicitDeny"}, guardrailExtended()...)
	return f
}

// TestGateGuardrailExtendedToDenyMintingScreenPasses: ReadOnlyAccess plus the
// anonymised guardrail passes ONLY once the guardrail is extended (in this
// test, not in the fixture) with an unconditional Deny of sqs:ReceiveMessage,
// every credential-minting action in guardrailMustDeny, every WriteAction and
// the data reads, and the simulation agrees. The real guardrail does not deny
// most of guardrailMustDeny, so in AWS that permission set is refused. This is
// not proof the real policy passes.
func TestGateGuardrailExtendedToDenyMintingScreenPasses(t *testing.T) {
	f := newExtendedGuardrailGate(t)
	f.def = gateAllowed // anything the gate did not need denied would show up
	now := time.Date(2026, 10, 30, 1, 2, 3, 0, time.UTC)
	r := Gate(context.Background(), f, gateAcct, now)
	if r.State != GateReadOnly {
		t.Fatalf("state = %s reason %s refused %v", r.State, r.Reason, r.Refused)
	}
	if r.RoleARN != gateARN || f.simulatedRole != gateARN || f.gotRoleName != gateRole {
		t.Errorf("RoleARN = %q simulated on %q GetRole(%q)", r.RoleARN, f.simulatedRole, f.gotRoleName)
	}
	if !r.CheckedAt.Equal(now) {
		t.Errorf("CheckedAt = %v", r.CheckedAt)
	}
	if len(r.DataReads) != len(DataReadActions) {
		t.Fatalf("DataReads = %v", r.DataReads)
	}
	// Order follows the simulation: s3:GetDataAccess is also a minting action,
	// so it is simulated (and reported) earlier than its place in the list.
	for i, d := range r.DataReads {
		if !slices.Contains(DataReadActions, d.Action) || !d.Denied {
			t.Errorf("DataReads[%d] = %+v", i, d)
		}
	}
	// One simulation, each action once. The in-test Deny names some concrete
	// actions ReadOnlyAccess also grants by name (apigateway:GET, ...), so
	// Residual drops those Allows and they are not simulated: the simulated
	// set is a subset of the union, holding every WriteAction, data read and
	// side-effect action.
	got := lowerSorted(f.simulated)
	if !slices.Equal(got, slices.Compact(slices.Clone(got))) {
		t.Errorf("simulated has duplicates: %v", got)
	}
	union := lowerSet(guardrailExtended())
	for _, a := range got {
		if !union[a] {
			t.Errorf("simulated %s, outside the union", a)
		}
	}
	for _, a := range slices.Concat(WriteActions, []string{"sqs:ReceiveMessage"}, DataReadActions) {
		if !slices.Contains(got, strings.ToLower(a)) {
			t.Errorf("did not simulate %s", a)
		}
	}
}

// TestGateGuardrailSimulatesTheUnion: without the in-test Deny, the one
// simulation for ReadOnlyAccess + guardrail is exactly the union of
// WriteActions, sqs:ReceiveMessage, guardrailMustDeny and the data reads.
func TestGateGuardrailSimulatesTheUnion(t *testing.T) {
	f := newFakeGate(t, "ReadOnlyAccess.json", "guardrail.json")
	Gate(context.Background(), f, gateAcct, time.Now())
	want := slices.Compact(lowerSorted(guardrailExtended()))
	if got := lowerSorted(f.simulated); !slices.Equal(got, want) {
		t.Errorf("simulated:\n got %v\nwant %v", got, want)
	}
}

func TestGateReadOnlyAccessRefused(t *testing.T) {
	f := newFakeGate(t, "ReadOnlyAccess.json")
	f.set(gateAllowed, "sqs:ReceiveMessage", "ec2:GetPasswordData")
	r := Gate(context.Background(), f, gateAcct, time.Now())
	if r.State != GateNotReadOnly {
		t.Fatalf("state = %s reason %s", r.State, r.Reason)
	}
	// Both simulated refusals are listed (ReadOnlyAccess also grants services
	// retired from the catalog, which refuse too; see TestGateUnknownActionRefuses).
	got := refusedPatterns(r)
	for _, w := range []string{"ec2:getpassworddata", "sqs:receivemessage"} {
		if !slices.Contains(got, w) {
			t.Errorf("Refused lacks %s: %v", w, r.Refused)
		}
	}
	if r.RoleARN != gateARN {
		t.Errorf("RoleARN = %q", r.RoleARN)
	}
}

func TestGateRefusedListsEverythingSorted(t *testing.T) {
	// A static refusal (a non-read verb) and simulated ones are all reported.
	f := newFakeGate(t, "ReadOnlyAccess.json")
	f.docs = append(f.docs, `{"Statement":{"Effect":"Allow","Action":["s3:PutObject","ec2:Frobnicate*"],"Resource":"*"}}`)
	f.set(gateAllowed, "sqs:ReceiveMessage", "s3:PutObject", "ec2:GetPasswordData")
	r := Gate(context.Background(), f, gateAcct, time.Now())
	if r.State != GateNotReadOnly {
		t.Fatalf("state = %s", r.State)
	}
	got := refusedPatterns(r)
	for _, w := range []string{"ec2:frobnicate*", "s3:putobject", "sqs:receivemessage", "ec2:getpassworddata"} {
		if !slices.Contains(got, w) {
			t.Errorf("Refused %v lacks %s", got, w)
		}
	}
	if !slices.IsSorted(got) {
		t.Errorf("Refused not sorted: %v", got)
	}
}

func TestGateImplicitDenyUnderCoveringAllow(t *testing.T) {
	// redshift:GetClusterCredentials is covered by redshift:Get* (a read verb)
	// and screened as credential-minting.
	f := newFakeGate(t)
	f.docs = []string{`{"Statement":{"Effect":"Allow","Action":"redshift:Get*","Resource":"*"}}`}
	f.set(gateImplicit, "redshift:GetClusterCredentials")
	r := Gate(context.Background(), f, gateAcct, time.Now())
	if r.State != GateNotReadOnly || !slices.Contains(refusedPatterns(r), "redshift:getclustercredentials") {
		t.Fatalf("covered implicitDeny: state %s refused %v", r.State, r.Refused)
	}
	for _, v := range r.Refused {
		if strings.EqualFold(v.Pattern, "redshift:GetClusterCredentials") && !strings.Contains(v.Why, "redshift:Get*") {
			t.Errorf("Why does not name the covering Allow: %q", v.Why)
		}
	}

	// implicitDeny with no covering residual counts as denied: nothing grants
	// the WriteActions, and the simulation says implicitDeny for everything.
	f = newFakeGate(t)
	f.docs = []string{`{"Statement":{"Effect":"Allow","Action":"ec2:DescribeInstances","Resource":"*"}}`}
	f.def = gateImplicit
	r = Gate(context.Background(), f, gateAcct, time.Now())
	if r.State != GateReadOnly {
		t.Fatalf("uncovered implicitDeny: state %s reason %s refused %v", r.State, r.Reason, r.Refused)
	}
	for _, d := range r.DataReads {
		if !d.Denied {
			t.Errorf("uncovered data read %s should be denied", d.Action)
		}
	}
}

func TestGateDataReadsDescriptive(t *testing.T) {
	// An allowed data read never blocks; an implicitDeny under a covering Allow
	// (ReadOnlyAccess grants dynamodb:Get*) is reported as still readable.
	f := newFakeGate(t)
	f.docs = []string{`{"Statement":{"Effect":"Allow","Action":["dynamodb:Get*","s3:Get*","ec2:DescribeInstances"],"Resource":"*"}}`}
	minters, _ := MintingScreen([]string{"dynamodb:Get*", "s3:Get*", "ec2:DescribeInstances"})
	f.docs = append(f.docs, denyDoc(slices.Concat(WriteActions, minters, DataReadActions)...))
	f.set(gateAllowed, "s3:GetObject")
	f.set(gateImplicit, "dynamodb:GetItem")
	r := Gate(context.Background(), f, gateAcct, time.Now())
	if r.State != GateReadOnly {
		t.Fatalf("state = %s refused %v", r.State, r.Refused)
	}
	for _, d := range r.DataReads {
		want := d.Action != "s3:GetObject" && d.Action != "dynamodb:GetItem"
		if d.Denied != want {
			t.Errorf("%s Denied = %v, want %v", d.Action, d.Denied, want)
		}
	}
}

// conditionalAllowDocs grants ec2:DescribeInstances, and ec2:GetPasswordData
// only under a condition: what the simulator reports as implicitDeny with
// MissingContextValues when it is not given the key.
var conditionalAllowDocs = []string{`{"Statement":[
	{"Effect":"Allow","Action":"ec2:DescribeInstances","Resource":"*"},
	{"Effect":"Allow","Action":"ec2:GetPasswordData","Resource":"*","Condition":{"StringEquals":{"aws:ResourceTag/env":"dev"}}}]}`}

func TestGateMissingContextIsUnverified(t *testing.T) {
	f := newFakeGate(t)
	f.docs = conditionalAllowDocs
	f.def = gateImplicit // nothing else is granted
	f.set(SimDecision{Decision: "implicitDeny", MissingContext: true}, "ec2:GetPasswordData")
	r := Gate(context.Background(), f, gateAcct, time.Now())
	if r.State != GateUnverified || r.Reason != ReasonMissingContext {
		t.Fatalf("state = %s reason %s refused %v", r.State, r.Reason, r.Refused)
	}
	if !slices.Equal(refusedPatterns(r), []string{"ec2:getpassworddata"}) {
		t.Errorf("Refused should name the action: %v", r.Refused)
	}

	// A definite refusal wins over missing context.
	f.set(gateAllowed, "s3:PutObject")
	if r := Gate(context.Background(), f, gateAcct, time.Now()); r.State != GateNotReadOnly {
		t.Fatalf("with a refusal: state = %s", r.State)
	}
}

// R18: allowed with missing context is allowed under some condition.
func TestGateAllowedWithMissingContextRefuses(t *testing.T) {
	f := newFakeGate(t)
	f.docs = conditionalAllowDocs
	f.def = gateImplicit
	f.set(SimDecision{Decision: "allowed", MissingContext: true}, "ec2:GetPasswordData")
	r := Gate(context.Background(), f, gateAcct, time.Now())
	if r.State != GateNotReadOnly || !slices.Equal(refusedPatterns(r), []string{"ec2:getpassworddata"}) ||
		!strings.Contains(r.Refused[0].Why, "condition") {
		t.Fatalf("state = %s refused %v", r.State, r.Refused)
	}
}

// R17: a simulated explicitDeny counts only when an unconditional Resource "*"
// Deny in the permission set's own documents covers the action. The
// simulator reports explicitDeny with empty MissingContextValues for a Deny
// with a Condition or NotResource, so the simulation alone proves nothing.
func TestGateExplicitDenyMustBeUnconditional(t *testing.T) {
	minters, _ := MintingScreen([]string{"ec2:Get*"})
	if !slices.Contains(minters, "ec2:GetPasswordData") {
		t.Fatalf("screen = %v", minters)
	}
	var others []string
	for _, a := range minters {
		if a != "ec2:GetPasswordData" {
			others = append(others, a)
		}
	}
	allow := `{"Statement":{"Effect":"Allow","Action":"ec2:Get*","Resource":"*"}}`
	for _, tc := range []struct {
		name, deny string // deny: the statement for ec2:GetPasswordData ("" = none in the docs)
		want       GateState
	}{
		{"unconditional", `{"Effect":"Deny","Action":"ec2:GetPasswordData","Resource":"*"}`, GateReadOnly},
		{"unconditional glob", `{"Effect":"Deny","Action":"EC2:getpassword*","Resource":["*"]}`, GateReadOnly},
		{"condition ResourceTag", `{"Effect":"Deny","Action":"ec2:GetPasswordData","Resource":"*","Condition":{"StringNotEquals":{"aws:ResourceTag/env":"dev"}}}`, GateNotReadOnly},
		{"condition PrincipalTag", `{"Effect":"Deny","Action":"ec2:GetPasswordData","Resource":"*","Condition":{"StringEquals":{"aws:PrincipalTag/team":"ops"}}}`, GateNotReadOnly},
		{"condition SourceIp", `{"Effect":"Deny","Action":"ec2:GetPasswordData","Resource":"*","Condition":{"NotIpAddress":{"aws:SourceIp":"10.0.0.0/8"}}}`, GateNotReadOnly},
		{"empty condition", `{"Effect":"Deny","Action":"ec2:GetPasswordData","Resource":"*","Condition":{}}`, GateNotReadOnly},
		{"NotResource", `{"Effect":"Deny","Action":"ec2:GetPasswordData","NotResource":"arn:aws:ec2:*:*:instance/i-dev"}`, GateNotReadOnly},
		{"narrow Resource", `{"Effect":"Deny","Action":"ec2:GetPasswordData","Resource":"arn:aws:ec2:*:*:instance/*"}`, GateNotReadOnly},
		{"NotAction exempts it", `{"Effect":"Deny","NotAction":["ec2:Describe*","ec2:GetPass*"],"Resource":"*"}`, GateNotReadOnly},
		{"absent (SCP or boundary)", "", GateNotReadOnly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeGate(t)
			f.docs = []string{allow, denyDoc(slices.Concat(WriteActions, others, DataReadActions)...)}
			if tc.deny != "" {
				f.docs = append(f.docs, `{"Statement":[`+tc.deny+`]}`)
			}
			// What the real simulator says in every case: explicitDeny, no
			// missing context.
			f.set(SimDecision{Decision: "explicitDeny"}, minters...)
			r := Gate(context.Background(), f, gateAcct, time.Now())
			if r.State != tc.want {
				t.Fatalf("state %s, want %s (refused %v)", r.State, tc.want, r.Refused)
			}
			if tc.want == GateNotReadOnly {
				if !slices.Equal(refusedPatterns(r), []string{"ec2:getpassworddata"}) || !strings.Contains(r.Refused[0].Why, "unconditional") {
					t.Errorf("Refused = %v", r.Refused)
				}
			}
		})
	}
}

func TestGateConditionalDenyDataReadStillReadable(t *testing.T) {
	// A data read the simulation explicitly denies through a conditional Deny
	// is reported as still readable.
	f := newExtendedGuardrailGate(t)
	f.docs[len(f.docs)-1] = denyDoc(slices.DeleteFunc(guardrailExtended(), func(a string) bool { return a == "s3:GetObject" })...)
	f.docs = append(f.docs, `{"Statement":{"Effect":"Deny","Action":"s3:GetObject","Resource":"*","Condition":{"Bool":{"aws:SecureTransport":"false"}}}}`)
	r := Gate(context.Background(), f, gateAcct, time.Now())
	if r.State != GateReadOnly {
		t.Fatalf("state %s refused %v", r.State, r.Refused)
	}
	for _, d := range r.DataReads {
		if d.Denied != (d.Action != "s3:GetObject") {
			t.Errorf("%s Denied = %v", d.Action, d.Denied)
		}
	}
}

func apiErr(code string) error {
	return fmt.Errorf("operation error: %w", &smithy.GenericAPIError{Code: code, Message: "nope"})
}

func TestGateUnverified(t *testing.T) {
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	policyReads := []string{"iam:GetPolicy", "iam:GetPolicyVersion", "iam:GetRolePolicy", "iam:ListAttachedRolePolicies", "iam:ListRolePolicies"}
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		mut     func(*fakeGate)
		reason  Reason
		missing []string
	}{
		{"sso expired", nil, func(f *fakeGate) { f.callerErr = fmt.Errorf("get creds: %w", ErrSSOExpired) }, ReasonSSOExpired, nil},
		{"session token expired", nil, func(f *fakeGate) { f.callerErr = apiErr("ExpiredToken") }, ReasonSSOExpired, nil},
		{"caller error", nil, func(f *fakeGate) { f.callerErr = errors.New("dial tcp: no route") }, ReasonError, nil},
		{"GetRole denied", nil, func(f *fakeGate) { f.roleErr = apiErr("AccessDenied") },
			ReasonMissingPerms, append([]string{"iam:GetRole"}, policyReads...)},
		{"GetRole error", nil, func(f *fakeGate) { f.roleErr = apiErr("ServiceFailure") }, ReasonError, nil},
		{"PolicyDocs denied", nil, func(f *fakeGate) { f.docsErr = apiErr("AccessDenied") }, ReasonMissingPerms, policyReads},
		{"PolicyDocs page failure", nil, func(f *fakeGate) { f.docsErr = apiErr("Throttling") }, ReasonError, nil},
		{"PolicyDocs expired token", nil, func(f *fakeGate) { f.docsErr = apiErr("ExpiredTokenException") }, ReasonSSOExpired, nil},
		{"unparseable doc", nil, func(f *fakeGate) { f.docs = append(f.docs, "{not json") }, ReasonError, nil},
		{"Simulate denied", nil, func(f *fakeGate) { f.simErr = apiErr("AccessDenied") }, ReasonSimulateDenied, nil},
		{"Simulate error", nil, func(f *fakeGate) { f.simErr = errors.New("boom") }, ReasonError, nil},
		{"Simulate deadline", nil, func(f *fakeGate) { f.simErr = fmt.Errorf("op: %w", context.DeadlineExceeded) }, ReasonTimeout, nil},
		{"decision missing", nil, func(f *fakeGate) { f.omit = map[string]bool{"s3:deleteobject": true} }, ReasonError, nil},
		{"data read missing", nil, func(f *fakeGate) { f.omit = map[string]bool{"kms:decrypt": true} }, ReasonError, nil},
		{"unknown decision", nil, func(f *fakeGate) { f.set(SimDecision{Decision: "maybe"}, "ec2:RunInstances") }, ReasonError, nil},
		{"ctx deadline", expired, func(*fakeGate) {}, ReasonTimeout, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newExtendedGuardrailGate(t)
			tc.mut(f)
			ctx := tc.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			r := Gate(ctx, f, gateAcct, time.Now())
			if r.State != GateUnverified || r.Reason != tc.reason {
				t.Fatalf("state %s reason %s, want unverified %s (refused %v)", r.State, r.Reason, tc.reason, r.Refused)
			}
			if !slices.Equal(r.MissingPerms, tc.missing) {
				t.Errorf("MissingPerms = %v, want %v", r.MissingPerms, tc.missing)
			}
		})
	}
}

func TestGateSimulateFailsAfterStaticRefusal(t *testing.T) {
	f := newFakeGate(t)
	f.docs = []string{`{"Statement":{"Effect":"Allow","Action":"*","Resource":"*"}}`}
	f.simErr = apiErr("AccessDenied")
	r := Gate(context.Background(), f, gateAcct, time.Now())
	if r.State != GateNotReadOnly || !slices.Contains(refusedPatterns(r), "*") {
		t.Fatalf("state %s refused %v", r.State, r.Refused)
	}
}

func TestGateAdminSimulatesOnlyNonRefusedMinters(t *testing.T) {
	// "*" refuses on its own; the screen does not expand it into thousands of
	// actions to simulate.
	f := newFakeGate(t)
	f.docs = []string{`{"Statement":{"Effect":"Allow","Action":"*","Resource":"*"}}`}
	f.def = gateAllowed
	r := Gate(context.Background(), f, gateAcct, time.Now())
	if r.State != GateNotReadOnly {
		t.Fatalf("state %s", r.State)
	}
	if len(f.simulated) != len(WriteActions)+len(DataReadActions) {
		t.Errorf("simulated %d actions: %v", len(f.simulated), f.simulated)
	}
	got := refusedPatterns(r)
	if !slices.Contains(got, "*") || !slices.Contains(got, "s3:putobject") {
		t.Errorf("Refused = %v", got)
	}
}

func TestGateAllowNotActionRefuses(t *testing.T) {
	f := newFakeGate(t)
	f.docs = []string{`{"Statement":{"Effect":"Allow","NotAction":"iam:*","Resource":"*"}}`}
	r := Gate(context.Background(), f, gateAcct, time.Now())
	if r.State != GateNotReadOnly || len(r.Refused) != 1 || !strings.Contains(r.Refused[0].Why, "NotAction") {
		t.Fatalf("state %s refused %v", r.State, r.Refused)
	}
	if f.simulated != nil {
		t.Errorf("simulated after a refused policy: %v", f.simulated)
	}
}

func TestGateUnknownActionRefuses(t *testing.T) {
	f := newFakeGate(t)
	f.docs = []string{`{"Statement":{"Effect":"Allow","Action":"ec2:DescribeSomethingNew2099","Resource":"*"}}`}
	f.def = gateImplicit
	r := Gate(context.Background(), f, gateAcct, time.Now())
	if r.State != GateNotReadOnly || len(r.Refused) != 1 || !strings.Contains(r.Refused[0].Why, "gencatalog") {
		t.Fatalf("state %s refused %v", r.State, r.Refused)
	}
}

func TestGateSideEffectAllowedRefuses(t *testing.T) {
	f := newFakeGate(t)
	f.docs = []string{`{"Statement":{"Effect":"Allow","Action":["sqs:Receive*","sqs:ListQueues"],"Resource":"*"}}`}
	f.def = gateImplicit
	f.set(gateAllowed, "sqs:ReceiveMessage")
	r := Gate(context.Background(), f, gateAcct, time.Now())
	if r.State != GateNotReadOnly || len(r.Refused) != 1 || !strings.Contains(r.Refused[0].Why, "sqs:Receive*") {
		t.Fatalf("state %s refused %v", r.State, r.Refused)
	}
}

func TestGateAccountMismatch(t *testing.T) {
	f := newFakeGate(t, "ReadOnlyAccess.json", "guardrail.json")
	for _, acct := range []string{"999999999999", ""} {
		r := Gate(context.Background(), f, acct, time.Now())
		if r.State != GateNotReadOnly || len(r.Refused) != 1 || !strings.Contains(r.Refused[0].Why, "account") {
			t.Fatalf("account %q: state %s refused %v", acct, r.State, r.Refused)
		}
	}
	if f.gotRoleName != "" {
		t.Errorf("GetRole called after an account mismatch")
	}
}

func TestGateCallerNotSSORole(t *testing.T) {
	for _, caller := range []string{
		"arn:aws:sts::" + gateAcct + ":assumed-role/SomeRole/x",
		"arn:aws:iam::" + gateAcct + ":user/alice",
		"arn:aws:sts::" + gateAcct + ":federated-user/bob",
		"arn:aws:sts::" + gateAcct + ":assumed-role/AWSReservedSSO_x",
		"arn:aws:sts::" + gateAcct + ":assumed-role/awsreservedsso_ReadOnly_1/x",
		"not an arn",
		"",
	} {
		f := newFakeGate(t, "ReadOnlyAccess.json", "guardrail.json")
		f.caller = caller
		r := Gate(context.Background(), f, gateAcct, time.Now())
		if r.State != GateNotReadOnly || len(r.Refused) != 1 {
			t.Errorf("caller %q: state %s refused %v", caller, r.State, r.Refused)
		}
	}
}

func TestGateRoleARNMustBeTheSSORole(t *testing.T) {
	for _, arn := range []string{
		"arn:aws:iam::" + gateAcct + ":role/" + gateRole,                                         // no aws-reserved path
		"arn:aws:iam::999999999999:role/aws-reserved/sso.amazonaws.com/" + gateRole,              // other account
		"arn:aws:iam::" + gateAcct + ":role/aws-reserved/sso.amazonaws.com/AWSReservedSSO_Other", // other role
		"",
	} {
		f := newFakeGate(t, "ReadOnlyAccess.json", "guardrail.json")
		f.role = arn
		r := Gate(context.Background(), f, gateAcct, time.Now())
		if r.State != GateNotReadOnly || len(r.Refused) != 1 {
			t.Errorf("role %q: state %s refused %v", arn, r.State, r.Refused)
		}
	}
}

func TestDataReadActions(t *testing.T) {
	want := []string{"secretsmanager:GetSecretValue", "ssm:GetParameter", "ssm:GetParameters", "ssm:GetParametersByPath",
		"ssm:GetParameterHistory", "dynamodb:GetItem", "dynamodb:BatchGetItem", "dynamodb:Query", "dynamodb:Scan", "dynamodb:PartiQLSelect",
		"dynamodb:GetRecords", "s3:GetDataAccess", "s3:GetObject", "kms:Decrypt"}
	if !slices.Equal(DataReadActions, want) {
		t.Errorf("DataReadActions = %v", DataReadActions)
	}
}

func TestGateReasonText(t *testing.T) {
	if ReasonText(ReasonMissingPerms) == "check failed" || ReasonText(ReasonMissingContext) == "check failed" {
		t.Error("new reasons need text")
	}
}

// fakeGateIAM is the SDK surface of NewIAMGate. List calls return pages[i]
// for the i-th call; fail names a method (and optional call index) to fail.
type fakeGateIAM struct {
	inline      []*iam.ListRolePoliciesOutput
	attached    []*iam.ListAttachedRolePoliciesOutput
	inlineDocs  map[string]string
	versions    map[string]string // policy ARN -> default version id
	versionDocs map[string]string // arn + "@" + version -> document
	failOn      string
	failErr     error
	sim         func(in *iam.SimulatePrincipalPolicyInput) (*iam.SimulatePrincipalPolicyOutput, error)
	nInline     int
	nAttached   int
	simCalls    []*iam.SimulatePrincipalPolicyInput
	roleName    string
}

func (f *fakeGateIAM) fail(m string) error {
	if f.failOn == m {
		return f.failErr
	}
	return nil
}

func (f *fakeGateIAM) ListRolePolicies(_ context.Context, in *iam.ListRolePoliciesInput, _ ...func(*iam.Options)) (*iam.ListRolePoliciesOutput, error) {
	f.nInline++
	if err := f.fail(fmt.Sprintf("ListRolePolicies#%d", f.nInline)); err != nil {
		return nil, err
	}
	if f.nInline > 1 && aws.ToString(in.Marker) == "" {
		return nil, errors.New("second page without marker")
	}
	return f.inline[f.nInline-1], nil
}

func (f *fakeGateIAM) ListAttachedRolePolicies(_ context.Context, in *iam.ListAttachedRolePoliciesInput, _ ...func(*iam.Options)) (*iam.ListAttachedRolePoliciesOutput, error) {
	f.nAttached++
	if err := f.fail(fmt.Sprintf("ListAttachedRolePolicies#%d", f.nAttached)); err != nil {
		return nil, err
	}
	if f.nAttached > 1 && aws.ToString(in.Marker) == "" {
		return nil, errors.New("second page without marker")
	}
	return f.attached[f.nAttached-1], nil
}

func (f *fakeGateIAM) SimulatePrincipalPolicy(_ context.Context, in *iam.SimulatePrincipalPolicyInput, _ ...func(*iam.Options)) (*iam.SimulatePrincipalPolicyOutput, error) {
	f.simCalls = append(f.simCalls, in)
	return f.sim(in)
}

func (f *fakeGateIAM) GetRole(_ context.Context, in *iam.GetRoleInput, _ ...func(*iam.Options)) (*iam.GetRoleOutput, error) {
	f.roleName = aws.ToString(in.RoleName)
	if err := f.fail("GetRole"); err != nil {
		return nil, err
	}
	return &iam.GetRoleOutput{Role: &iamtypes.Role{Arn: aws.String(gateARN)}}, nil
}

func (f *fakeGateIAM) GetRolePolicy(_ context.Context, in *iam.GetRolePolicyInput, _ ...func(*iam.Options)) (*iam.GetRolePolicyOutput, error) {
	if err := f.fail("GetRolePolicy"); err != nil {
		return nil, err
	}
	return &iam.GetRolePolicyOutput{PolicyDocument: aws.String(f.inlineDocs[aws.ToString(in.PolicyName)])}, nil
}

func (f *fakeGateIAM) GetPolicy(_ context.Context, in *iam.GetPolicyInput, _ ...func(*iam.Options)) (*iam.GetPolicyOutput, error) {
	if err := f.fail("GetPolicy"); err != nil {
		return nil, err
	}
	v, ok := f.versions[aws.ToString(in.PolicyArn)]
	if !ok {
		return &iam.GetPolicyOutput{Policy: &iamtypes.Policy{}}, nil
	}
	return &iam.GetPolicyOutput{Policy: &iamtypes.Policy{DefaultVersionId: aws.String(v)}}, nil
}

func (f *fakeGateIAM) GetPolicyVersion(_ context.Context, in *iam.GetPolicyVersionInput, _ ...func(*iam.Options)) (*iam.GetPolicyVersionOutput, error) {
	if err := f.fail("GetPolicyVersion"); err != nil {
		return nil, err
	}
	d := f.versionDocs[aws.ToString(in.PolicyArn)+"@"+aws.ToString(in.VersionId)]
	return &iam.GetPolicyVersionOutput{PolicyVersion: &iamtypes.PolicyVersion{Document: aws.String(d)}}, nil
}

type fakeGateSTS struct {
	arn string
	err error
}

func (f fakeGateSTS) GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &sts.GetCallerIdentityOutput{Arn: aws.String(f.arn)}, nil
}

const (
	polRO   = "arn:aws:iam::aws:policy/ReadOnlyAccess"
	polView = "arn:aws:iam::aws:policy/job-function/ViewOnlyAccess"
)

func newFakeGateIAM(t *testing.T) *fakeGateIAM {
	return &fakeGateIAM{
		inline: []*iam.ListRolePoliciesOutput{
			{PolicyNames: []string{"AwsSSOInlinePolicy"}, IsTruncated: true, Marker: aws.String("i1")},
			{PolicyNames: []string{"second"}},
		},
		attached: []*iam.ListAttachedRolePoliciesOutput{
			{AttachedPolicies: []iamtypes.AttachedPolicy{{PolicyArn: aws.String(polRO)}}, IsTruncated: true, Marker: aws.String("a1")},
			{AttachedPolicies: []iamtypes.AttachedPolicy{{PolicyArn: aws.String(polView)}}},
		},
		inlineDocs: map[string]string{
			"AwsSSOInlinePolicy": url.PathEscape(fixture(t, "guardrail.json")),
			"second":             `{"Statement":{"Effect":"Allow","Action":"ec2:DescribeInstances","Resource":"*"}}`,
		},
		versions: map[string]string{polRO: "v190", polView: "v47"},
		versionDocs: map[string]string{
			polRO + "@v190":  url.PathEscape(fixture(t, "ReadOnlyAccess.json")),
			polView + "@v47": url.PathEscape(fixture(t, "ViewOnlyAccess.json")),
		},
	}
}

func TestIAMGatePolicyDocsAllPages(t *testing.T) {
	f := newFakeGateIAM(t)
	g := iamGate{iam: f}
	docs, err := g.PolicyDocs(context.Background(), gateRole)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 4 || f.nInline != 2 || f.nAttached != 2 {
		t.Fatalf("docs %d, inline pages %d, attached pages %d", len(docs), f.nInline, f.nAttached)
	}
	// The URL-encoded documents decode through Residual.
	res, err := Residual(docs)
	if err != nil || !slices.Contains(res, "sqs:Receive*") || !slices.Contains(res, "ec2:DescribeInstances") {
		t.Fatalf("residual %d, err %v", len(res), err)
	}
}

func TestIAMGatePolicyDocsAnyFailureFails(t *testing.T) {
	denied := apiErr("AccessDenied")
	for _, m := range []string{"ListRolePolicies#1", "ListRolePolicies#2", "GetRolePolicy",
		"ListAttachedRolePolicies#1", "ListAttachedRolePolicies#2", "GetPolicy", "GetPolicyVersion"} {
		f := newFakeGateIAM(t)
		f.failOn, f.failErr = m, denied
		docs, err := iamGate{iam: f}.PolicyDocs(context.Background(), gateRole)
		if err == nil || docs != nil {
			t.Errorf("%s: docs %d err %v", m, len(docs), err)
			continue
		}
		if gateReason(context.Background(), err, ReasonMissingPerms) != ReasonMissingPerms {
			t.Errorf("%s: AccessDenied lost in wrapping: %v", m, err)
		}
	}
	// A missing default version or an empty document is a failure, not a skip.
	f := newFakeGateIAM(t)
	delete(f.versions, polView)
	if _, err := (iamGate{iam: f}).PolicyDocs(context.Background(), gateRole); err == nil {
		t.Error("no default version: want error")
	}
	f = newFakeGateIAM(t)
	f.inlineDocs["second"] = ""
	if _, err := (iamGate{iam: f}).PolicyDocs(context.Background(), gateRole); err == nil {
		t.Error("empty inline document: want error")
	}
}

func TestIAMGateSimulateBatches(t *testing.T) {
	var actions []string
	for i := range 2*simulateBatch + 7 {
		actions = append(actions, fmt.Sprintf("ec2:Describe%03d", i))
	}
	f := newFakeGateIAM(t)
	page := 0
	f.sim = func(in *iam.SimulatePrincipalPolicyInput) (*iam.SimulatePrincipalPolicyOutput, error) {
		if len(in.ActionNames) > simulateBatch {
			return nil, fmt.Errorf("batch of %d", len(in.ActionNames))
		}
		// Each batch answers in two pages.
		page++
		half := len(in.ActionNames) / 2
		if in.Marker == nil {
			return &iam.SimulatePrincipalPolicyOutput{IsTruncated: true, Marker: aws.String("m"),
				EvaluationResults: evals(in.ActionNames[:half], iamtypes.PolicyEvaluationDecisionTypeImplicitDeny)}, nil
		}
		rest := evals(in.ActionNames[half:], iamtypes.PolicyEvaluationDecisionTypeExplicitDeny)
		rest[0].MissingContextValues = []string{"aws:RequestedRegion"}
		rest[1].ResourceSpecificResults = []iamtypes.ResourceSpecificResult{{MissingContextValues: []string{"aws:SourceIp"}}}
		return &iam.SimulatePrincipalPolicyOutput{EvaluationResults: rest}, nil
	}
	got, err := iamGate{iam: f}.Simulate(context.Background(), gateARN, actions)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.simCalls) != 6 || len(got) != len(actions) {
		t.Fatalf("calls %d, decisions %d", len(f.simCalls), len(got))
	}
	for _, c := range f.simCalls {
		if aws.ToString(c.PolicySourceArn) != gateARN {
			t.Fatalf("PolicySourceArn = %q", aws.ToString(c.PolicySourceArn))
		}
	}
	if d := got["ec2:Describe000"]; d.Decision != "implicitDeny" || d.MissingContext {
		t.Errorf("first = %+v", d)
	}
	if d := got["ec2:Describe050"]; d.Decision != "explicitDeny" || !d.MissingContext {
		t.Errorf("missing context = %+v", d)
	}
	if d := got["ec2:Describe051"]; !d.MissingContext {
		t.Errorf("resource-specific missing context = %+v", d)
	}

	// Any failing batch fails the whole call.
	f = newFakeGateIAM(t)
	n := 0
	f.sim = func(in *iam.SimulatePrincipalPolicyInput) (*iam.SimulatePrincipalPolicyOutput, error) {
		n++
		if n == 2 {
			return nil, apiErr("AccessDenied")
		}
		return &iam.SimulatePrincipalPolicyOutput{EvaluationResults: evals(in.ActionNames, iamtypes.PolicyEvaluationDecisionTypeExplicitDeny)}, nil
	}
	if got, err := (iamGate{iam: f}).Simulate(context.Background(), gateARN, actions); err == nil || got != nil {
		t.Fatalf("second batch failed: got %d err %v", len(got), err)
	} else if gateReason(context.Background(), err, ReasonSimulateDenied) != ReasonSimulateDenied {
		t.Errorf("AccessDenied lost in wrapping: %v", err)
	}
}

func TestIAMGateEndToEnd(t *testing.T) {
	// The SDK-backed gate over fakes: four pages of documents (ReadOnlyAccess,
	// ViewOnlyAccess, the guardrail and one inline), a simulation that denies
	// everything explicitly, backed by an in-test unconditional Deny (so, like
	// the guardrail test, it passes only because the minting screen is
	// extended into the policy; it shows the plumbing, not that a real policy
	// passes).
	f := newFakeGateIAM(t)
	viewMinters, _ := MintingScreen(fixtureResidual(t, "ViewOnlyAccess.json"))
	var back struct{ Statement []map[string]any }
	back.Statement = []map[string]any{
		{"Effect": "Allow", "Action": "ec2:DescribeInstances", "Resource": "*"},
		{"Effect": "Deny", "Action": slices.Concat(guardrailExtended(), viewMinters), "Resource": "*"},
	}
	b, _ := json.Marshal(back)
	f.inlineDocs["second"] = url.PathEscape(string(b))
	f.sim = func(in *iam.SimulatePrincipalPolicyInput) (*iam.SimulatePrincipalPolicyOutput, error) {
		return &iam.SimulatePrincipalPolicyOutput{EvaluationResults: evals(in.ActionNames, iamtypes.PolicyEvaluationDecisionTypeExplicitDeny)}, nil
	}
	g := iamGate{iam: f, sts: fakeGateSTS{arn: gateCaller}}
	r := Gate(context.Background(), g, gateAcct, time.Now())
	if r.State != GateReadOnly {
		t.Fatalf("state %s reason %s", r.State, r.Reason)
	}
	if f.roleName != gateRole || r.RoleARN != gateARN {
		t.Errorf("GetRole(%q) RoleARN %q", f.roleName, r.RoleARN)
	}

	// An STS failure is unverified, never read-only.
	g.sts = fakeGateSTS{err: apiErr("ExpiredToken")}
	if r := Gate(context.Background(), g, gateAcct, time.Now()); r.State != GateUnverified || r.Reason != ReasonSSOExpired {
		t.Fatalf("sts expired: %s %s", r.State, r.Reason)
	}
}

func TestNewIAMGate(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAENVENVENVENVENV0") // the static provider must win over the environment
	t.Setenv("AWS_SECRET_ACCESS_KEY", "env")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "none"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "none"))
	t.Setenv("AWS_PROFILE", "")
	api, err := NewIAMGate(context.Background(), aws.Credentials{AccessKeyID: "AK", SecretAccessKey: "SK", SessionToken: "TK"}, "ap-southeast-1", "")
	g, ok := api.(iamGate)
	if err != nil || !ok || g.iam == nil || g.sts == nil {
		t.Fatalf("NewIAMGate = %#v, %v", api, err)
	}
	opts := g.iam.(*iam.Client).Options()
	cr, err := opts.Credentials.Retrieve(context.Background())
	if err != nil || cr.AccessKeyID != "AK" || cr.SessionToken != "TK" || opts.Region != "ap-southeast-1" {
		t.Fatalf("iam options: %+v %v", cr, err)
	}
}

// The gate's client config is the SDK's default load: retry settings and the
// CA bundle from the environment apply, like every other host-side client.
func TestNewIAMGateLoadsTheDefaultConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "none"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "none"))
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_MAX_ATTEMPTS", "7")
	creds := aws.Credentials{AccessKeyID: "AK", SecretAccessKey: "SK", SessionToken: "TK"}
	cfg, err := gateConfig(context.Background(), creds, "eu-west-1", "")
	if err != nil || cfg.RetryMaxAttempts != 7 || cfg.Region != "eu-west-1" {
		t.Fatalf("cfg retry=%d region=%q err=%v", cfg.RetryMaxAttempts, cfg.Region, err)
	}
	t.Setenv("AWS_CA_BUNDLE", filepath.Join(dir, "no-such-bundle.pem"))
	if _, err := NewIAMGate(context.Background(), creds, "eu-west-1", ""); err == nil {
		t.Fatal("a missing AWS_CA_BUNDLE was not read: the gate ignores the host's CA settings")
	}
}

// The gate reads the binding's source profile, so a setting on that profile
// reaches it, as it reaches the SSO and STS clients. A missing bundle file is
// how the load shows the setting was read.
func TestGateConfigUsesTheSourceProfile(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "config")
	marker := filepath.Join(dir, "credential-process-ran")
	// plain names a credential_process and assume a role_arn: the static
	// credentials must still win over both, and the process must never run.
	body := "[profile src]\nca_bundle = " + filepath.Join(dir, "no-such-bundle.pem") +
		"\n[profile plain]\nmax_attempts = 5\ncredential_process = /usr/bin/touch " + marker + "\n" +
		"[profile assume]\nrole_arn = arn:aws:iam::123456789012:role/x\ncredential_source = Environment\n"
	if err := os.WriteFile(conf, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", conf)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "none"))
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_MAX_ATTEMPTS", "")
	t.Setenv("AWS_CA_BUNDLE", "")
	creds := aws.Credentials{AccessKeyID: "AK", SecretAccessKey: "SK", SessionToken: "TK"}
	if _, err := gateConfig(context.Background(), creds, "eu-west-1", "src"); err == nil {
		t.Fatal("ca_bundle on the source profile was not read")
	}
	if _, err := NewIAMGate(context.Background(), creds, "eu-west-1", "src"); err == nil {
		t.Fatal("NewIAMGate did not pass the source profile on")
	}
	cfg, err := gateConfig(context.Background(), creds, "eu-west-1", "plain")
	if err != nil || cfg.RetryMaxAttempts != 5 {
		t.Fatalf("retry=%d err=%v, want the source profile's max_attempts", cfg.RetryMaxAttempts, err)
	}
	got, err := cfg.Credentials.Retrieve(context.Background())
	if err != nil || got.AccessKeyID != "AK" {
		t.Fatalf("creds = %q, %v: the static credentials must win", got.AccessKeyID, err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the profile's credential_process ran")
	}
	cfg, err = gateConfig(context.Background(), creds, "eu-west-1", "assume")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := cfg.Credentials.Retrieve(context.Background()); err != nil || got.AccessKeyID != "AK" {
		t.Fatalf("creds = %q, %v: a role_arn on the profile must not replace the static credentials", got.AccessKeyID, err)
	}
}

// A daemon AWS_PROFILE naming a profile that does not exist must not break a
// gate whose own source profile is fine.
func TestGateConfigIgnoresABogusEnvProfile(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "config")
	if err := os.WriteFile(conf, []byte("[profile src]\nregion = eu-west-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", conf)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "none"))
	t.Setenv("AWS_PROFILE", "no-such-profile")
	t.Setenv("AWS_CA_BUNDLE", "")
	creds := aws.Credentials{AccessKeyID: "AK", SecretAccessKey: "SK", SessionToken: "TK"}
	if _, err := gateConfig(context.Background(), creds, "eu-west-1", "src"); err != nil {
		t.Fatalf("a bogus AWS_PROFILE broke the gate: %v", err)
	}
}

func TestGateCallerIsSetAfterStepOne(t *testing.T) {
	f := newFakeGate(t, "ReadOnlyAccess.json", "guardrail.json")
	r := Gate(context.Background(), f, gateAcct, time.Now())
	if want := "arn:aws:sts::" + gateAcct + ":assumed-role/" + gateRole; r.Caller != want {
		t.Errorf("Caller = %q, want %q", r.Caller, want)
	}
	// role lookup fails: the caller is still known, the role ARN is not.
	f = newFakeGate(t, "ReadOnlyAccess.json", "guardrail.json")
	f.roleErr = &smithy.GenericAPIError{Code: "AccessDenied"}
	r = Gate(context.Background(), f, gateAcct, time.Now())
	if r.RoleARN != "" || r.Caller == "" {
		t.Errorf("role %q caller %q", r.RoleARN, r.Caller)
	}
	// step 1 refusals carry no caller.
	f = newFakeGate(t, "ReadOnlyAccess.json", "guardrail.json")
	f.caller = "arn:aws:iam::" + gateAcct + ":user/alice"
	if r = Gate(context.Background(), f, gateAcct, time.Now()); r.Caller != "" {
		t.Errorf("not-sso caller: %q", r.Caller)
	}
	f = newFakeGate(t, "ReadOnlyAccess.json", "guardrail.json")
	if r = Gate(context.Background(), f, "999999999999", time.Now()); r.Caller != "" {
		t.Errorf("account mismatch: %q", r.Caller)
	}
	f = newFakeGate(t, "ReadOnlyAccess.json", "guardrail.json")
	f.callerErr = errors.New("boom")
	if r = Gate(context.Background(), f, gateAcct, time.Now()); r.Caller != "" {
		t.Errorf("caller error: %q", r.Caller)
	}
}

func TestGatePolicyDigest(t *testing.T) {
	f := newFakeGate(t, "ReadOnlyAccess.json", "guardrail.json")
	r := Gate(context.Background(), f, gateAcct, time.Now())
	if len(r.PolicyDigest) != 64 {
		t.Fatalf("digest %q", r.PolicyDigest)
	}
	g := newFakeGate(t, "guardrail.json", "ReadOnlyAccess.json") // order swapped
	if r2 := Gate(context.Background(), g, gateAcct, time.Now()); r2.PolicyDigest != r.PolicyDigest {
		t.Errorf("order changed the digest: %s vs %s", r2.PolicyDigest, r.PolicyDigest)
	}
	h := newFakeGate(t, "ReadOnlyAccess.json")
	if r3 := Gate(context.Background(), h, gateAcct, time.Now()); r3.PolicyDigest == r.PolicyDigest {
		t.Error("different documents, same digest")
	}
	// Set once step 3 passed, even when a later step refuses structurally.
	n := newFakeGate(t, "guardrail.json")
	n.docs = append(n.docs, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","NotAction":"iam:*","Resource":"*"}]}`)
	if r4 := Gate(context.Background(), n, gateAcct, time.Now()); r4.PolicyDigest == "" || r4.State != GateNotReadOnly {
		t.Errorf("NotAction: state %s digest %q", r4.State, r4.PolicyDigest)
	}
	// Empty before step 3 completes.
	e := newFakeGate(t, "guardrail.json")
	e.docsErr = errors.New("boom")
	if r5 := Gate(context.Background(), e, gateAcct, time.Now()); r5.PolicyDigest != "" {
		t.Errorf("digest after a PolicyDocs error: %q", r5.PolicyDigest)
	}
	f2 := newFakeGate(t, "guardrail.json")
	f2.roleErr = errors.New("x")
	if r6 := Gate(context.Background(), f2, gateAcct, time.Now()); r6.PolicyDigest != "" {
		t.Errorf("digest after a role error: %q", r6.PolicyDigest)
	}
}

// TestGatePartialRunRecordsIncomplete: a breadth refusal found before the
// simulation still wins the state, but a simulation cut short records why in
// Incomplete, so an acknowledgement can tell a full run from a partial one.
func TestGatePartialRunRecordsIncomplete(t *testing.T) {
	breadth := `{"Statement":{"Effect":"Allow","Action":"*","Resource":"*"}}`
	for _, tc := range []struct {
		name string
		mut  func(*fakeGate)
		want Reason
	}{
		{"full run", func(*fakeGate) {}, ""},
		{"simulate denied", func(f *fakeGate) { f.simErr = apiErr("AccessDenied") }, ReasonSimulateDenied},
		{"simulate error", func(f *fakeGate) { f.simErr = errors.New("boom") }, ReasonError},
		{"simulate timeout", func(f *fakeGate) { f.simErr = fmt.Errorf("op: %w", context.DeadlineExceeded) }, ReasonTimeout},
		{"simulate expired", func(f *fakeGate) { f.simErr = apiErr("ExpiredToken") }, ReasonSSOExpired},
		{"decision missing", func(f *fakeGate) { f.omit = map[string]bool{"s3:deleteobject": true} }, ReasonError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeGate(t)
			f.docs = []string{breadth}
			f.def = gateAllowed
			tc.mut(f)
			r := Gate(context.Background(), f, gateAcct, time.Now())
			if r.State != GateNotReadOnly || !slices.Contains(refusedPatterns(r), "*") {
				t.Fatalf("state %s refused %v", r.State, r.Refused)
			}
			if r.Incomplete != tc.want {
				t.Errorf("Incomplete = %q, want %q", r.Incomplete, tc.want)
			}
			if r.Reason != "" {
				t.Errorf("Reason = %q on a not_read_only result", r.Reason)
			}
			if len(r.PolicyDigest) != 64 || r.RoleARN == "" || r.Caller == "" {
				t.Errorf("digest %q role %q caller %q", r.PolicyDigest, r.RoleARN, r.Caller)
			}
		})
	}
	// A run with no refusal keeps Incomplete empty: Reason carries it.
	f := newExtendedGuardrailGate(t)
	f.simErr = apiErr("AccessDenied")
	if r := Gate(context.Background(), f, gateAcct, time.Now()); r.State != GateUnverified || r.Incomplete != "" {
		t.Errorf("unverified run: state %s incomplete %q", r.State, r.Incomplete)
	}
}
