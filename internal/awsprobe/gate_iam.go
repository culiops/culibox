package awsprobe

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/culiops/agent-box/internal/awshost"
)

// simulateBatch caps the ActionNames of one SimulatePrincipalPolicy request.
// The API documents no cap; a batch keeps each request small, and every
// batch must succeed or the whole simulation is an error.
const simulateBatch = 100

type gateIAMClient interface {
	iam.ListRolePoliciesAPIClient
	iam.ListAttachedRolePoliciesAPIClient
	iam.SimulatePrincipalPolicyAPIClient
	GetRole(context.Context, *iam.GetRoleInput, ...func(*iam.Options)) (*iam.GetRoleOutput, error)
	GetRolePolicy(context.Context, *iam.GetRolePolicyInput, ...func(*iam.Options)) (*iam.GetRolePolicyOutput, error)
	GetPolicy(context.Context, *iam.GetPolicyInput, ...func(*iam.Options)) (*iam.GetPolicyOutput, error)
	GetPolicyVersion(context.Context, *iam.GetPolicyVersionInput, ...func(*iam.Options)) (*iam.GetPolicyVersionOutput, error)
}

type gateSTSClient interface {
	GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

type iamGate struct {
	iam gateIAMClient
	sts gateSTSClient
}

// NewIAMGate is the SDK-backed GateAPI, calling AWS with the permission set's
// own credentials. Errors are returned wrapped, never swallowed, so Gate can
// classify them; a missing field in a reply is an error. The client config is
// the SDK's default load (host shared files, AWS_CA_BUNDLE / ca_bundle, retry
// settings) with only the credentials and region fixed, so the gate reaches
// AWS the way every other host-side client does. A config that cannot load
// is an error, which the caller treats as unverified.
func NewIAMGate(ctx context.Context, creds aws.Credentials, region string) (GateAPI, error) {
	cfg, err := gateConfig(ctx, creds, region)
	if err != nil {
		return nil, err
	}
	return iamGate{iam: iam.NewFromConfig(cfg), sts: sts.NewFromConfig(cfg)}, nil
}

// gateConfig loads the default config with creds as a static provider (it
// wins over any environment or profile credentials) and region.
func gateConfig(ctx context.Context, creds aws.Credentials, region string) (aws.Config, error) {
	opts := append(awshost.LoadOpts(), awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(creds.AccessKeyID, creds.SecretAccessKey, creds.SessionToken)))
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("load AWS config for the gate: %w", err)
	}
	return cfg, nil
}

func (g iamGate) CallerARN(ctx context.Context) (string, error) {
	out, err := g.sts.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", fmt.Errorf("sts:GetCallerIdentity: %w", err)
	}
	return aws.ToString(out.Arn), nil
}

func (g iamGate) RoleARN(ctx context.Context, roleName string) (string, error) {
	out, err := g.iam.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(roleName)})
	if err != nil {
		return "", fmt.Errorf("iam:GetRole: %w", err)
	}
	if out.Role == nil || aws.ToString(out.Role.Arn) == "" {
		return "", errors.New("iam:GetRole: reply has no role ARN")
	}
	return aws.ToString(out.Role.Arn), nil
}

// PolicyDocs returns every inline policy and the default version of every
// attached managed policy, reading every page. Any failure fails the whole
// call: a dropped Allow document would make the gate laxer.
func (g iamGate) PolicyDocs(ctx context.Context, roleName string) ([]string, error) {
	var docs []string
	inline := iam.NewListRolePoliciesPaginator(g.iam, &iam.ListRolePoliciesInput{RoleName: aws.String(roleName)})
	for inline.HasMorePages() {
		page, err := inline.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("iam:ListRolePolicies: %w", err)
		}
		for _, name := range page.PolicyNames {
			out, err := g.iam.GetRolePolicy(ctx, &iam.GetRolePolicyInput{RoleName: aws.String(roleName), PolicyName: aws.String(name)})
			if err != nil {
				return nil, fmt.Errorf("iam:GetRolePolicy %s: %w", name, err)
			}
			if aws.ToString(out.PolicyDocument) == "" {
				return nil, fmt.Errorf("iam:GetRolePolicy %s: empty document", name)
			}
			docs = append(docs, aws.ToString(out.PolicyDocument))
		}
	}
	attached := iam.NewListAttachedRolePoliciesPaginator(g.iam, &iam.ListAttachedRolePoliciesInput{RoleName: aws.String(roleName)})
	for attached.HasMorePages() {
		page, err := attached.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("iam:ListAttachedRolePolicies: %w", err)
		}
		for _, ap := range page.AttachedPolicies {
			arn := aws.ToString(ap.PolicyArn)
			if arn == "" {
				return nil, errors.New("iam:ListAttachedRolePolicies: attached policy without an ARN")
			}
			pol, err := g.iam.GetPolicy(ctx, &iam.GetPolicyInput{PolicyArn: aws.String(arn)})
			if err != nil {
				return nil, fmt.Errorf("iam:GetPolicy %s: %w", arn, err)
			}
			if pol.Policy == nil || aws.ToString(pol.Policy.DefaultVersionId) == "" {
				return nil, fmt.Errorf("iam:GetPolicy %s: no default version", arn)
			}
			ver, err := g.iam.GetPolicyVersion(ctx, &iam.GetPolicyVersionInput{PolicyArn: aws.String(arn), VersionId: pol.Policy.DefaultVersionId})
			if err != nil {
				return nil, fmt.Errorf("iam:GetPolicyVersion %s: %w", arn, err)
			}
			if ver.PolicyVersion == nil || aws.ToString(ver.PolicyVersion.Document) == "" {
				return nil, fmt.Errorf("iam:GetPolicyVersion %s: empty document", arn)
			}
			docs = append(docs, aws.ToString(ver.PolicyVersion.Document))
		}
	}
	return docs, nil
}

// Simulate runs SimulatePrincipalPolicy on roleARN in batches of
// simulateBatch, following each batch's pagination.
func (g iamGate) Simulate(ctx context.Context, roleARN string, actions []string) (map[string]SimDecision, error) {
	out := map[string]SimDecision{}
	for start := 0; start < len(actions); start += simulateBatch {
		batch := actions[start:min(start+simulateBatch, len(actions))]
		p := iam.NewSimulatePrincipalPolicyPaginator(g.iam, &iam.SimulatePrincipalPolicyInput{
			PolicySourceArn: aws.String(roleARN),
			ActionNames:     batch,
		})
		for p.HasMorePages() {
			page, err := p.NextPage(ctx)
			if err != nil {
				return nil, fmt.Errorf("iam:SimulatePrincipalPolicy: %w", err)
			}
			for _, e := range page.EvaluationResults {
				missing := len(e.MissingContextValues) > 0
				for _, r := range e.ResourceSpecificResults {
					missing = missing || len(r.MissingContextValues) > 0
				}
				out[aws.ToString(e.EvalActionName)] = SimDecision{Decision: string(e.EvalDecision), MissingContext: missing}
			}
		}
	}
	return out, nil
}
