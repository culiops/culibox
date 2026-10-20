package awsprobe

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
)

type fakeIAM struct {
	pages []*iam.SimulatePrincipalPolicyOutput
	err   error
	calls []*iam.SimulatePrincipalPolicyInput
}

func (f *fakeIAM) SimulatePrincipalPolicy(_ context.Context, in *iam.SimulatePrincipalPolicyInput, _ ...func(*iam.Options)) (*iam.SimulatePrincipalPolicyOutput, error) {
	f.calls = append(f.calls, in)
	if f.err != nil {
		return nil, f.err
	}
	p := f.pages[len(f.calls)-1]
	return p, nil
}

func evals(actions []string, dec iamtypes.PolicyEvaluationDecisionType) []iamtypes.EvaluationResult {
	var out []iamtypes.EvaluationResult
	for _, a := range actions {
		out = append(out, iamtypes.EvaluationResult{EvalActionName: aws.String(a), EvalDecision: dec})
	}
	return out
}

func TestIAMSimulatorPaginates(t *testing.T) {
	f := &fakeIAM{pages: []*iam.SimulatePrincipalPolicyOutput{
		{IsTruncated: true, Marker: aws.String("m1"), EvaluationResults: evals(WriteActions[:6], iamtypes.PolicyEvaluationDecisionTypeImplicitDeny)},
		{EvaluationResults: evals(WriteActions[6:], iamtypes.PolicyEvaluationDecisionTypeAllowed)},
	}}
	var cfg aws.Config
	s := IAMSimulator{NewClient: func(c aws.Config) iam.SimulatePrincipalPolicyAPIClient { cfg = c; return f }}
	role := "arn:aws:iam::123456789012:role/base"
	got, err := s.Simulate(context.Background(), aws.Credentials{AccessKeyID: "AK", SecretAccessKey: "SK", SessionToken: "TK"}, "ap-southeast-1", role, WriteActions)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 11 || got["s3:PutObject"] != "implicitDeny" || got["cloudformation:CreateStack"] != "allowed" {
		t.Fatalf("map = %v", got)
	}
	if len(f.calls) != 2 || aws.ToString(f.calls[1].Marker) != "m1" {
		t.Fatalf("calls = %+v", f.calls)
	}
	for _, c := range f.calls {
		if aws.ToString(c.PolicySourceArn) != role || !reflect.DeepEqual(c.ActionNames, WriteActions) {
			t.Fatalf("input = %+v", c)
		}
	}
	if cfg.Region != "ap-southeast-1" {
		t.Fatalf("region = %q", cfg.Region)
	}
	cr, err := cfg.Credentials.Retrieve(context.Background())
	if err != nil || cr.AccessKeyID != "AK" || cr.SecretAccessKey != "SK" || cr.SessionToken != "TK" {
		t.Fatalf("creds = %+v %v", cr, err)
	}
}

func TestIAMSimulatorError(t *testing.T) {
	boom := errors.New("boom")
	s := IAMSimulator{NewClient: func(aws.Config) iam.SimulatePrincipalPolicyAPIClient { return &fakeIAM{err: boom} }}
	_, err := s.Simulate(context.Background(), aws.Credentials{}, "us-east-1", "arn:aws:iam::1:role/x", WriteActions)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}
