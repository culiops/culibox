package awsprobe

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
)

// Simulator runs an IAM policy simulation for roleARN with the given
// credentials and returns action -> decision.
type Simulator interface {
	Simulate(ctx context.Context, creds aws.Credentials, region, roleARN string, actions []string) (map[string]string, error)
}

// IAMSimulator is the SDK-backed Simulator.
type IAMSimulator struct {
	// NewClient builds the IAM client; nil = iam.NewFromConfig. A seam for the pagination test.
	NewClient func(aws.Config) iam.SimulatePrincipalPolicyAPIClient
}

// Simulate calls iam:SimulatePrincipalPolicy on roleARN, following pagination.
// Errors are returned unchanged so SimulateReason can classify them.
func (s IAMSimulator) Simulate(ctx context.Context, creds aws.Credentials, region, roleARN string, actions []string) (map[string]string, error) {
	cfg := aws.Config{
		Region:      region,
		Credentials: credentials.NewStaticCredentialsProvider(creds.AccessKeyID, creds.SecretAccessKey, creds.SessionToken),
	}
	var client iam.SimulatePrincipalPolicyAPIClient
	if s.NewClient != nil {
		client = s.NewClient(cfg)
	} else {
		client = iam.NewFromConfig(cfg)
	}
	p := iam.NewSimulatePrincipalPolicyPaginator(client, &iam.SimulatePrincipalPolicyInput{
		PolicySourceArn: aws.String(roleARN),
		ActionNames:     actions,
	})
	out := map[string]string{}
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, e := range page.EvaluationResults {
			out[aws.ToString(e.EvalActionName)] = string(e.EvalDecision)
		}
	}
	return out, nil
}
