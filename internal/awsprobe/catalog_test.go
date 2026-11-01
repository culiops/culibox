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
// ec2:GetPasswordData, ecr:GetDownloadUrlForLayer and lambda:GetFunction (all
// three listed by name); the rest are real minters, SecretReturning entries or
// actions not documented as harmless, and stay here (when unsure an
// action stays in mustDeny). Reviewed by hand: a change is a security change.
var guardrailMustDeny = []string{
	"acm:getacmeexternalaccountbindingcredentials",
	"aidevops:searchserviceaccessibleresource",
	"apigateway:get",
	"cloudfront-keyvaluestore:getkey",
	"cloudfront-keyvaluestore:listkeys",
	"cloudwatch:getaccessgrant",
	"cloudwatch:getaccessprofile",
	"cloudwatch:getdomainaccessgrantfororganization",
	"cloudwatch:getspacecredentials",
	"cloudwatch:getspacecredentialsfororganization",
	"cloudwatch:listaccessgrants",
	"cloudwatch:listaccessprofiles",
	"cloudwatch:listdomainaccessgrantsfororganization",
	"cloudwatch:listspaceaccess",
	"cloudwatch:listtelemetryquerysessions",
	"codebuild:listconnectedoauthaccounts",
	"ec2:describeipamexternalresourceverificationtokens",
	"ec2:describeverifiedaccesstrustproviders",
	"ec2:getipamrouteoriginauthorizations",
	"ec2:getpassworddata",
	"ecr:getauthorizationtoken",
	"ecr:getdownloadurlforlayer",
	"lambda:getfunction",
	"lambda:getlayerversion",
	"pi:describedimensionkeys",
	"pi:getdimensionkeydetails",
	"pricing:getpricelistfileurl",
	"s3:getdataaccess",
	"ssm:getaccesstoken",
	"wafv2:getdecryptedapikey",
	"wafv2:listapikeys",
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

func TestSecretReturning(t *testing.T) {
	for a, why := range SecretReturning {
		if a != strings.ToLower(a) || strings.TrimSpace(why) == "" || !Known(a) {
			t.Errorf("bad SecretReturning entry %q (lowercase, non-empty reason, in catalog)", a)
		}
		if _, ok := HarmlessMinting[a]; ok {
			t.Errorf("%s is in both SecretReturning and HarmlessMinting", a)
		}
	}
	for _, c := range []struct{ pattern, want string }{
		{"lambda:Get*", "lambda:GetFunction"},
		{"lambda:Get*", "lambda:GetLayerVersion"},
		{"cognito-idp:Describe*", "cognito-idp:DescribeUserPoolClient"},
		{"apigateway:GET", "apigateway:GET"},
		{"sts:Assume*", "sts:AssumeRole"},
		{"sts:Assume*", "sts:AssumeRoleWithSAML"},
		{"sts:Assume*", "sts:AssumeRoleWithWebIdentity"},
		{"sts:Assume*", "sts:AssumeRoot"},
		{"*", "lambda:GetFunction"},
	} {
		deny, _ := MintingScreen([]string{c.pattern})
		if !slices.Contains(deny, c.want) {
			t.Errorf("%s does not put %s in mustDeny", c.pattern, c.want)
		}
	}
	// Each entry is caught even if someone adds it to HarmlessMinting.
	HarmlessMinting["lambda:getfunction"] = "test"
	defer delete(HarmlessMinting, "lambda:getfunction")
	if deny, _ := MintingScreen([]string{"lambda:GetFunction"}); !slices.Contains(deny, "lambda:GetFunction") {
		t.Error("HarmlessMinting overrides SecretReturning")
	}
	// A neighbouring read is not swept in.
	if deny, _ := MintingScreen([]string{"lambda:GetFunctionConfiguration"}); len(deny) != 0 {
		t.Errorf("lambda:GetFunctionConfiguration mustDeny = %v", deny)
	}
}

func TestRetiredServicesNotInCatalog(t *testing.T) {
	loadCatalog()
	for svc, why := range RetiredServices {
		if svc != strings.ToLower(svc) || strings.TrimSpace(why) == "" {
			t.Errorf("bad RetiredServices entry %q", svc)
		}
		if _, ok := cat[svc]; ok {
			t.Errorf("%s is in the catalog again: remove it from RetiredServices", svc)
		}
	}
}

func TestRetiredServicesSuppressOnlyUnknown(t *testing.T) {
	_, unknown := MintingScreen(fixtureResidual(t, "ReadOnlyAccess.json"))
	for _, p := range unknown {
		svc, _, _ := strings.Cut(strings.ToLower(p), ":")
		if _, retired := RetiredServices[svc]; retired {
			t.Errorf("retired service pattern %s still unknown", p)
		}
	}
	for _, svc := range []string{"codestar", "deepcomposer", "evidently", "iot1click", "iotevents"} {
		if _, ok := RetiredServices[svc]; !ok {
			t.Errorf("%s missing from RetiredServices", svc)
		}
	}
	// Other unknowns still refuse, including a wildcard service that only
	// resembles a retired one.
	_, unknown = MintingScreen([]string{"codestar:GetProject", "nosuch:Get*", "iot1click*:Get*"})
	if !slices.Equal(unknown, []string{"iot1click*:Get*", "nosuch:Get*"}) {
		t.Errorf("unknown = %v", unknown)
	}
}

func TestLayerVersionByArnIsNotAnIAMAction(t *testing.T) {
	// GetLayerVersionByArn is an API operation authorised by
	// lambda:GetLayerVersion, so SecretReturning lists only the latter.
	if Known("lambda:GetLayerVersionByArn") || !Known("lambda:GetLayerVersion") {
		t.Error("catalog changed: revisit the lambda layer SecretReturning entries")
	}
}

// Reads whose reply really carries a secret under a name the screen words do
// not match. Each must be denied whenever a residual pattern covers it.
func TestSecretReturningCatchesRealSecretReads(t *testing.T) {
	for _, a := range []string{
		"acm:exportcertificate",
		"amplify:getapp", "amplify:getbranch", "amplify:listapps", "amplify:listbranches",
		"codepipeline:getjobdetails", "codepipeline:getthirdpartyjobdetails",
		"finspace:getkxconnectionstring",
		"glue:getconnection", "glue:getconnections",
		"mediapackage:describechannel", "mediapackage:listchannels",
		"kinesisvideo:geticeserverconfig",
		"directconnect:describevirtualinterfaces",
		"chime:getbot", "chime:listbots", "chime:getattendee",
		"chime:listattendees", "directconnect:describerouterconfiguration",
	} {
		service, _, _ := strings.Cut(a, ":")
		mustDeny, unknown := MintingScreen([]string{service + ":*"})
		if len(unknown) != 0 {
			t.Errorf("%s: pattern unknown to the catalog: %v", a, unknown)
		}
		if !slices.ContainsFunc(mustDeny, func(d string) bool { return strings.EqualFold(d, a) }) {
			t.Errorf("%s is not in mustDeny under %s:*", a, service)
		}
	}
}
