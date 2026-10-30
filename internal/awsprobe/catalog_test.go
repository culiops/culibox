package awsprobe

import (
	"slices"
	"strings"
	"testing"
)

func fixtureResidual(t *testing.T, names ...string) []string {
	t.Helper()
	var docs []string
	for _, n := range names {
		docs = append(docs, fixture(t, n))
	}
	r, err := Residual(docs)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestExpand(t *testing.T) {
	if got := Expand("sqs:Receive*"); !slices.Equal(got, []string{"sqs:ReceiveMessage"}) {
		t.Errorf("sqs:Receive* = %v", got)
	}
	got := Expand("GAMELIFT:get*access")
	for _, want := range []string{"gamelift:GetComputeAccess", "gamelift:GetInstanceAccess"} {
		if !slices.Contains(got, want) {
			t.Errorf("GAMELIFT:get*access misses %s: %v", want, got)
		}
	}
	if got := Expand("nosuchservice:*"); len(got) != 0 {
		t.Errorf("unknown service expanded to %v", got)
	}
}

func TestKnown(t *testing.T) {
	if !Known("S3:getobject") || Known("s3:GetNewThing2099") {
		t.Error("Known must match ignoring case and reject unlisted actions")
	}
}

// The spec's known minters, every one of which the screen must catch.
var knownMinters = []string{
	"redshift:GetClusterCredentials", "redshift:GetClusterCredentialsWithIAM",
	"redshift-serverless:GetCredentials", "gamelift:GetComputeAccess", "gamelift:GetInstanceAccess",
	"cognito-identity:GetOpenIdTokenForDeveloperIdentity", "cognito-identity:GetCredentialsForIdentity",
	"codeartifact:GetAuthorizationToken", "ec2:GetPasswordData",
}

func lowerSet(in []string) map[string]bool {
	m := map[string]bool{}
	for _, a := range in {
		m[strings.ToLower(a)] = true
	}
	return m
}

func TestMintingScreenCatches(t *testing.T) {
	residual := fixtureResidual(t, "ReadOnlyAccess.json")
	deny, unknown := MintingScreen(residual)
	t.Logf("ReadOnlyAccess: %d mustDeny, %d unknown", len(deny), len(unknown))
	have := lowerSet(deny)
	granted := 0
	for _, a := range knownMinters {
		for _, p := range residual {
			if MatchAction(p, a) {
				granted++
				if !have[strings.ToLower(a)] {
					t.Errorf("ReadOnlyAccess grants %s but mustDeny misses it", a)
				}
				break
			}
		}
	}
	if granted < 4 {
		t.Errorf("only %d known minters are granted by the fixture; the check has gone vacuous", granted)
	}
	for _, a := range deny {
		if _, ok := HarmlessMinting[strings.ToLower(a)]; ok {
			t.Errorf("mustDeny lists harmless %s", a)
		}
	}
}

// ReadOnlyAccess v190 no longer grants the Redshift minters, so prove the screen
// catches every known one against the broad read patterns that would.
func TestMintingScreenCatchesKnownMinters(t *testing.T) {
	deny, unknown := MintingScreen([]string{
		"redshift:Get*", "redshift-serverless:Get*", "gamelift:Get*", "cognito-identity:Get*",
		"codeartifact:Get*", "ec2:Get*", "sts:Get*",
	})
	if len(unknown) != 0 {
		t.Errorf("unknown = %v", unknown)
	}
	have := lowerSet(deny)
	for _, a := range knownMinters {
		if !have[strings.ToLower(a)] {
			t.Errorf("mustDeny misses %s", a)
		}
	}
	for _, a := range []string{"sts:GetSessionToken", "sts:GetFederationToken"} {
		if have[strings.ToLower(a)] {
			t.Errorf("mustDeny lists %s, which a role session cannot call", a)
		}
	}
}

// The guardrail residual's mustDeny list: screened actions that a surviving
// ReadOnlyAccess pattern covers and HarmlessMinting does not excuse. The gate
// needs the simulation to prove each one denied. The guardrail itself denies
// only ec2:GetPasswordData and ecr:GetDownloadUrlForLayer; the rest are real
// minters or actions not documented as harmless, and stay here (when unsure an
// action stays in mustDeny). Reviewed by hand: a change is a security change.
var guardrailMustDeny = []string{
	"acm:GetAcmeExternalAccountBindingCredentials",
	"aidevops:SearchServiceAccessibleResource",
	"cloudfront-keyvaluestore:GetKey",
	"cloudfront-keyvaluestore:ListKeys",
	"cloudwatch:GetAccessGrant",
	"cloudwatch:GetAccessProfile",
	"cloudwatch:GetDomainAccessGrantForOrganization",
	"cloudwatch:GetSpaceCredentials",
	"cloudwatch:GetSpaceCredentialsForOrganization",
	"cloudwatch:ListAccessGrants",
	"cloudwatch:ListAccessProfiles",
	"cloudwatch:ListDomainAccessGrantsForOrganization",
	"cloudwatch:ListSpaceAccess",
	"cloudwatch:ListTelemetryQuerySessions",
	"codebuild:ListConnectedOAuthAccounts",
	"ec2:DescribeIpamExternalResourceVerificationTokens",
	"ec2:DescribeVerifiedAccessTrustProviders",
	"ec2:GetIpamRouteOriginAuthorizations",
	"ec2:GetPasswordData",
	"ecr:GetAuthorizationToken",
	"ecr:GetDownloadUrlForLayer",
	"pricing:GetPriceListFileUrl",
	"s3:GetDataAccess",
	"ssm:GetAccessToken",
	"wafv2:GetDecryptedAPIKey",
	"wafv2:ListAPIKeys",
}

func TestMintingScreenGuardrail(t *testing.T) {
	deny, unknown := MintingScreen(fixtureResidual(t, "ReadOnlyAccess.json", "guardrail.json"))
	t.Logf("guardrail: %d mustDeny, unknown %v", len(deny), unknown)
	if len(unknown) != 0 {
		t.Errorf("unknown actions: %v", unknown)
	}
	if !slices.Equal(lowerSorted(deny), lowerSorted(guardrailMustDeny)) {
		t.Errorf("guardrail mustDeny changed:\n got %v\nwant %v", lowerSorted(deny), lowerSorted(guardrailMustDeny))
	}
}

func TestUnknownAction(t *testing.T) {
	deny, unknown := MintingScreen([]string{"s3:GetNewThing2099", "nosuch:Get*", "s3:GetObject"})
	if !slices.Equal(unknown, []string{"nosuch:Get*", "s3:GetNewThing2099"}) {
		t.Errorf("unknown = %v", unknown)
	}
	if len(deny) != 0 {
		t.Errorf("mustDeny = %v", deny)
	}
}

func TestScreenWordsCaseInsensitive(t *testing.T) {
	deny, _ := MintingScreen([]string{"CODEARTIFACT:getauthorizationtoken", "ec2:DESCRIBEINSTANCES"})
	if !slices.Equal(lowerSorted(deny), []string{"codeartifact:getauthorizationtoken"}) {
		t.Errorf("mustDeny = %v", deny)
	}
}

func TestMintingScreenSortedDeduped(t *testing.T) {
	deny, _ := MintingScreen([]string{"ec2:GetPasswordData", "ec2:Get*Data", "EC2:getpassworddata"})
	n := 0
	for _, a := range deny {
		if strings.EqualFold(a, "ec2:GetPasswordData") {
			n++
		}
	}
	if n != 1 || !slices.IsSortedFunc(deny, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) }) {
		t.Errorf("mustDeny not deduped/sorted: %v", deny)
	}
}

func TestHarmlessHaveReasons(t *testing.T) {
	for a, why := range HarmlessMinting {
		if strings.TrimSpace(why) == "" {
			t.Errorf("%s has no reason", a)
		}
		if a != strings.ToLower(a) {
			t.Errorf("%s is not lowercased", a)
		}
		if !Known(a) {
			t.Errorf("%s is not in the catalog", a)
		}
	}
}
