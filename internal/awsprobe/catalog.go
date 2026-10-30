package awsprobe

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"slices"
	"strings"
	"sync"
)

// CatalogDate is the day actions.json.gz was snapshotted from AWS's service
// reference (servicereference.us-east-1.amazonaws.com). Regenerate with
// `go run ./internal/awsprobe/gencatalog` and update this constant.
const CatalogDate = "2026-10-30"

//go:embed actions.json.gz
var catalogGz []byte

type catalogData struct {
	Date string `json:"date"`
	// Services maps a lowercased IAM service prefix to its action names in
	// AWS's own casing.
	Services map[string][]string `json:"services"`
}

var (
	catalogOnce sync.Once
	cat         map[string][]string // lowercased prefix -> canonical action names
	known       map[string]bool     // lowercased "service:action"
)

// loadCatalog decodes the embedded snapshot once. A corrupt snapshot is a
// build defect, so it panics rather than silently screening against nothing.
func loadCatalog() {
	catalogOnce.Do(func() {
		zr, err := gzip.NewReader(bytes.NewReader(catalogGz))
		if err != nil {
			panic("awsprobe: corrupt actions.json.gz: " + err.Error())
		}
		var d catalogData
		if err := json.NewDecoder(zr).Decode(&d); err != nil {
			panic("awsprobe: corrupt actions.json.gz: " + err.Error())
		}
		if d.Date != CatalogDate || len(d.Services) < 100 {
			panic("awsprobe: actions.json.gz does not match CatalogDate or is truncated")
		}
		cat = map[string][]string{}
		known = map[string]bool{}
		for svc, names := range d.Services {
			svc = strings.ToLower(svc)
			for _, n := range names {
				cat[svc] = append(cat[svc], n)
				known[svc+":"+strings.ToLower(n)] = true
			}
		}
	})
}

// Expand returns the catalog's concrete actions (as "service:Action", in AWS's
// casing, sorted) that an IAM action pattern matches, ignoring case.
func Expand(pattern string) []string {
	loadCatalog()
	svcPat, _, _ := strings.Cut(pattern, ":")
	var out []string
	for svc, names := range cat {
		// A literal service part lets us skip every other service.
		if !strings.ContainsAny(svcPat, "*?") && !strings.EqualFold(svcPat, svc) {
			continue
		}
		for _, n := range names {
			if a := svc + ":" + n; MatchAction(pattern, a) {
				out = append(out, a)
			}
		}
	}
	slices.SortFunc(out, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	return out
}

// Known reports whether the catalog lists the concrete action.
func Known(action string) bool {
	loadCatalog()
	return known[strings.ToLower(action)]
}

// screenWords are the substrings that mark an action name as one that may
// mint, return or reveal a credential (spec, gate step 5). Matching is
// case-insensitive and anywhere in the name.
var screenWords = []string{"credential", "token", "password", "access", "key", "secret", "session", "login", "auth", "url"}

func screened(action string) bool {
	_, name, _ := strings.Cut(strings.ToLower(action), ":")
	for _, w := range screenWords {
		if strings.Contains(name, w) {
			return true
		}
	}
	return false
}

// MintingScreen expands the residual Allow patterns against the catalog.
// mustDeny is every covered action whose name matches a screen word, minus
// HarmlessMinting; the gate then needs the simulation to prove each of them
// denied. unknown is every pattern that expands to nothing: a concrete action
// the snapshot does not list (AWS added it later) or a wildcard that matches
// nothing. Both are sorted and deduplicated ignoring case.
func MintingScreen(residual []string) (mustDeny []string, unknown []string) {
	seen := map[string]bool{}
	seenUnknown := map[string]bool{}
	for _, p := range residual {
		expanded := Expand(p)
		if len(expanded) == 0 {
			if k := strings.ToLower(p); !seenUnknown[k] {
				seenUnknown[k] = true
				unknown = append(unknown, p)
			}
			continue
		}
		for _, a := range expanded {
			k := strings.ToLower(a)
			if seen[k] || !screened(a) {
				continue
			}
			if _, ok := HarmlessMinting[k]; ok {
				continue
			}
			seen[k] = true
			mustDeny = append(mustDeny, a)
		}
	}
	cmp := func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) }
	slices.SortFunc(mustDeny, cmp)
	slices.SortFunc(unknown, cmp)
	return mustDeny, unknown
}

// HarmlessMinting lists screened actions reviewed as safe to leave allowed
// (lowercased action -> reason). The bar is AWS documentation: the response is
// metadata or configuration with no token, password, signed URL, temporary
// credential or secret value, or a role session cannot call the action at all.
// When unsure an action stays in mustDeny. The list covers the screen's false
// positives on the policy fixtures; an action no supported permission set
// grants is not pre-listed.
var HarmlessMinting = map[string]string{
	"sts:getsessiontoken":                                     "a role session cannot call it (GetSessionToken/GetFederationToken need long-term IAM user credentials)",
	"sts:getfederationtoken":                                  "a role session cannot call it (GetSessionToken/GetFederationToken need long-term IAM user credentials)",
	"iam:getaccesskeylastused":                                "returns key or credential metadata (IDs, dates, last use), never the secret",
	"iam:listaccesskeys":                                      "returns key or credential metadata (IDs, dates, last use), never the secret",
	"iam:listservicespecificcredentials":                      "returns key or credential metadata (IDs, dates, last use), never the secret",
	"iam:getloginprofile":                                     "returns key or credential metadata (IDs, dates, last use), never the secret",
	"iam:getcredentialreport":                                 "returns key or credential metadata (IDs, dates, last use), never the secret",
	"iam:generatecredentialreport":                            "returns key or credential metadata (IDs, dates, last use), never the secret",
	"iam:getaccountpasswordpolicy":                            "returns key or credential metadata (IDs, dates, last use), never the secret",
	"codebuild:listsourcecredentials":                         "returns key or credential metadata (IDs, dates, last use), never the secret",
	"secretsmanager:listsecrets":                              "returns key or credential metadata (IDs, dates, last use), never the secret",
	"secretsmanager:describesecret":                           "returns key or credential metadata (IDs, dates, last use), never the secret",
	"secretsmanager:listsecretversionids":                     "returns key or credential metadata (IDs, dates, last use), never the secret",
	"kafka:listscramsecrets":                                  "returns key or credential metadata (IDs, dates, last use), never the secret",
	"iam:generateorganizationsaccessreport":                   "IAM access-report or authorization metadata; returns policies and usage, no credential",
	"iam:generateservicelastaccesseddetails":                  "IAM access-report or authorization metadata; returns policies and usage, no credential",
	"iam:getaccountauthorizationdetails":                      "IAM access-report or authorization metadata; returns policies and usage, no credential",
	"iam:getorganizationsaccessreport":                        "IAM access-report or authorization metadata; returns policies and usage, no credential",
	"iam:getservicelastaccesseddetails":                       "IAM access-report or authorization metadata; returns policies and usage, no credential",
	"iam:getservicelastaccesseddetailswithentities":           "IAM access-report or authorization metadata; returns policies and usage, no credential",
	"iam:listpoliciesgrantingserviceaccess":                   "IAM access-report or authorization metadata; returns policies and usage, no credential",
	"iam:getcontextkeysforcustompolicy":                       "IAM access-report or authorization metadata; returns policies and usage, no credential",
	"iam:getcontextkeysforprincipalpolicy":                    "IAM access-report or authorization metadata; returns policies and usage, no credential",
	"iam:getcloudfrontpublickey":                              "returns a public key, which is not secret",
	"iam:listcloudfrontpublickeys":                            "returns a public key, which is not secret",
	"iam:getsshpublickey":                                     "returns a public key, which is not secret",
	"iam:listsshpublickeys":                                   "returns a public key, which is not secret",
	"kms:getpublickey":                                        "returns a public key, which is not secret",
	"cloudfront:getpublickey":                                 "returns a public key, which is not secret",
	"cloudfront:getpublickeyconfig":                           "returns a public key, which is not secret",
	"cloudfront:listpublickeys":                               "returns a public key, which is not secret",
	"cloudtrail:listpublickeys":                               "returns a public key, which is not secret",
	"kms:describecustomkeystores":                             "KMS key metadata, policy or rotation state; never key material",
	"kms:describekey":                                         "KMS key metadata, policy or rotation state; never key material",
	"kms:getkeylastusage":                                     "KMS key metadata, policy or rotation state; never key material",
	"kms:getkeypolicy":                                        "KMS key metadata, policy or rotation state; never key material",
	"kms:getkeyrotationstatus":                                "KMS key metadata, policy or rotation state; never key material",
	"kms:listkeypolicies":                                     "KMS key metadata, policy or rotation state; never key material",
	"kms:listkeyrotations":                                    "KMS key metadata, policy or rotation state; never key material",
	"kms:listkeys":                                            "KMS key metadata, policy or rotation state; never key material",
	"ec2:getebsdefaultkmskeyid":                               "KMS key metadata, policy or rotation state; never key material",
	"ec2:describekeypairs":                                    "EC2 key pair or serial-console setting; names, fingerprints and status only, no private key",
	"ec2:getserialconsoleaccessstatus":                        "EC2 key pair or serial-console setting; names, fingerprints and status only, no private key",
	"cloudfront:getkeygroup":                                  "CloudFront configuration (key groups, origin access identity or control, key-value store metadata)",
	"cloudfront:getkeygroupconfig":                            "CloudFront configuration (key groups, origin access identity or control, key-value store metadata)",
	"cloudfront:listkeygroups":                                "CloudFront configuration (key groups, origin access identity or control, key-value store metadata)",
	"cloudfront:listdistributionsbykeygroup":                  "CloudFront configuration (key groups, origin access identity or control, key-value store metadata)",
	"cloudfront:getcloudfrontoriginaccessidentity":            "CloudFront configuration (key groups, origin access identity or control, key-value store metadata)",
	"cloudfront:getcloudfrontoriginaccessidentityconfig":      "CloudFront configuration (key groups, origin access identity or control, key-value store metadata)",
	"cloudfront:listcloudfrontoriginaccessidentities":         "CloudFront configuration (key groups, origin access identity or control, key-value store metadata)",
	"cloudfront:getoriginaccesscontrol":                       "CloudFront configuration (key groups, origin access identity or control, key-value store metadata)",
	"cloudfront:getoriginaccesscontrolconfig":                 "CloudFront configuration (key groups, origin access identity or control, key-value store metadata)",
	"cloudfront:listoriginaccesscontrols":                     "CloudFront configuration (key groups, origin access identity or control, key-value store metadata)",
	"cloudfront:describekeyvaluestore":                        "CloudFront configuration (key groups, origin access identity or control, key-value store metadata)",
	"cloudfront:listkeyvaluestores":                           "CloudFront configuration (key groups, origin access identity or control, key-value store metadata)",
	"cloudfront-keyvaluestore:describekeyvaluestore":          "CloudFront configuration (key groups, origin access identity or control, key-value store metadata)",
	"s3:getaccessgrant":                                       "S3 access point, Access Grants or public-access-block configuration; no credential (s3:GetDataAccess mints and stays denied)",
	"s3:getaccessgrantsinstance":                              "S3 access point, Access Grants or public-access-block configuration; no credential (s3:GetDataAccess mints and stays denied)",
	"s3:getaccessgrantsinstanceforprefix":                     "S3 access point, Access Grants or public-access-block configuration; no credential (s3:GetDataAccess mints and stays denied)",
	"s3:getaccessgrantsinstanceresourcepolicy":                "S3 access point, Access Grants or public-access-block configuration; no credential (s3:GetDataAccess mints and stays denied)",
	"s3:getaccessgrantslocation":                              "S3 access point, Access Grants or public-access-block configuration; no credential (s3:GetDataAccess mints and stays denied)",
	"s3:listaccessgrants":                                     "S3 access point, Access Grants or public-access-block configuration; no credential (s3:GetDataAccess mints and stays denied)",
	"s3:listaccessgrantsinstances":                            "S3 access point, Access Grants or public-access-block configuration; no credential (s3:GetDataAccess mints and stays denied)",
	"s3:listaccessgrantslocations":                            "S3 access point, Access Grants or public-access-block configuration; no credential (s3:GetDataAccess mints and stays denied)",
	"s3:listcalleraccessgrants":                               "S3 access point, Access Grants or public-access-block configuration; no credential (s3:GetDataAccess mints and stays denied)",
	"s3:getaccesspoint":                                       "S3 access point, Access Grants or public-access-block configuration; no credential (s3:GetDataAccess mints and stays denied)",
	"s3:getaccesspointconfigurationforobjectlambda":           "S3 access point, Access Grants or public-access-block configuration; no credential (s3:GetDataAccess mints and stays denied)",
	"s3:getaccesspointforobjectlambda":                        "S3 access point, Access Grants or public-access-block configuration; no credential (s3:GetDataAccess mints and stays denied)",
	"s3:getaccesspointpolicy":                                 "S3 access point, Access Grants or public-access-block configuration; no credential (s3:GetDataAccess mints and stays denied)",
	"s3:getaccesspointpolicyforobjectlambda":                  "S3 access point, Access Grants or public-access-block configuration; no credential (s3:GetDataAccess mints and stays denied)",
	"s3:getaccesspointpolicystatus":                           "S3 access point, Access Grants or public-access-block configuration; no credential (s3:GetDataAccess mints and stays denied)",
	"s3:getaccesspointpolicystatusforobjectlambda":            "S3 access point, Access Grants or public-access-block configuration; no credential (s3:GetDataAccess mints and stays denied)",
	"s3:getmultiregionaccesspoint":                            "S3 access point, Access Grants or public-access-block configuration; no credential (s3:GetDataAccess mints and stays denied)",
	"s3:getmultiregionaccesspointpolicy":                      "S3 access point, Access Grants or public-access-block configuration; no credential (s3:GetDataAccess mints and stays denied)",
	"s3:getmultiregionaccesspointpolicystatus":                "S3 access point, Access Grants or public-access-block configuration; no credential (s3:GetDataAccess mints and stays denied)",
	"s3:getmultiregionaccesspointroutes":                      "S3 access point, Access Grants or public-access-block configuration; no credential (s3:GetDataAccess mints and stays denied)",
	"s3:listaccesspoints":                                     "S3 access point, Access Grants or public-access-block configuration; no credential (s3:GetDataAccess mints and stays denied)",
	"s3:listaccesspointsforobjectlambda":                      "S3 access point, Access Grants or public-access-block configuration; no credential (s3:GetDataAccess mints and stays denied)",
	"s3:listmultiregionaccesspoints":                          "S3 access point, Access Grants or public-access-block configuration; no credential (s3:GetDataAccess mints and stays denied)",
	"s3:getaccountpublicaccessblock":                          "S3 access point, Access Grants or public-access-block configuration; no credential (s3:GetDataAccess mints and stays denied)",
	"s3:getbucketpublicaccessblock":                           "S3 access point, Access Grants or public-access-block configuration; no credential (s3:GetDataAccess mints and stays denied)",
	"elasticfilesystem:describeaccesspoints":                  "S3 access point, Access Grants or public-access-block configuration; no credential (s3:GetDataAccess mints and stays denied)",
	"ec2:describeverifiedaccessendpoints":                     "EC2 Verified Access or network-analysis configuration; no credential",
	"ec2:describeverifiedaccessgroups":                        "EC2 Verified Access or network-analysis configuration; no credential",
	"ec2:describeverifiedaccessinstanceloggingconfigurations": "EC2 Verified Access or network-analysis configuration; no credential",
	"ec2:describeverifiedaccessinstances":                     "EC2 Verified Access or network-analysis configuration; no credential",
	"ec2:describeverifiedaccessinstancewebaclassociations":    "EC2 Verified Access or network-analysis configuration; no credential",
	"ec2:getverifiedaccessendpointpolicy":                     "EC2 Verified Access or network-analysis configuration; no credential",
	"ec2:getverifiedaccessendpointtargets":                    "EC2 Verified Access or network-analysis configuration; no credential",
	"ec2:getverifiedaccessgrouppolicy":                        "EC2 Verified Access or network-analysis configuration; no credential",
	"ec2:getverifiedaccessinstancewebacl":                     "EC2 Verified Access or network-analysis configuration; no credential",
	"ec2:describenetworkinsightsaccessscopeanalyses":          "EC2 Verified Access or network-analysis configuration; no credential",
	"ec2:describenetworkinsightsaccessscopes":                 "EC2 Verified Access or network-analysis configuration; no credential",
	"ec2:getnetworkinsightsaccessscopeanalysisfindings":       "EC2 Verified Access or network-analysis configuration; no credential",
	"ec2:getnetworkinsightsaccessscopecontent":                "EC2 Verified Access or network-analysis configuration; no credential",
	"ec2:describeclientvpnauthorizationrules":                 "EC2 Verified Access or network-analysis configuration; no credential",
	"ec2:describetrafficmirrorsessions":                       "EC2 Verified Access or network-analysis configuration; no credential",
	"ec2:describevpcblockpublicaccessexclusions":              "EC2 Verified Access or network-analysis configuration; no credential",
	"ec2:describevpcblockpublicaccessoptions":                 "EC2 Verified Access or network-analysis configuration; no credential",
	"ec2:getimageblockpublicaccessstate":                      "EC2 Verified Access or network-analysis configuration; no credential",
	"ec2:getsnapshotblockpublicaccessstate":                   "EC2 Verified Access or network-analysis configuration; no credential",
	"ec2:getcapacitymanagermonitoredtagkeys":                  "EC2 Verified Access or network-analysis configuration; no credential",
	"lambda:getfunctionurlconfig":                             "Lambda function URL configuration: the public endpoint and its auth type, not a signed URL",
	"lambda:listfunctionurlconfigs":                           "Lambda function URL configuration: the public endpoint and its auth type, not a signed URL",
	"sqs:getqueueurl":                                         "returns an SQS queue URL, an unsigned endpoint address",
	"ssm:describesessions":                                    "session, service-access or tag-key metadata; no credential",
	"shield:describedrtaccess":                                "session, service-access or tag-key metadata; no credential",
	"resource-explorer-2:liststreamingaccessforservices":      "session, service-access or tag-key metadata; no credential",
	"route53:listvpcassociationauthorizations":                "session, service-access or tag-key metadata; no credential",
	"tag:gettagkeys":                                          "session, service-access or tag-key metadata; no credential",
	"wafv2:getratebasedstatementmanagedkeys":                  "session, service-access or tag-key metadata; no credential",
	"pi:describedimensionkeys":                                "Performance Insights dimension data (metric dimensions, SQL text); data, not a credential",
	"pi:getdimensionkeydetails":                               "Performance Insights dimension data (metric dimensions, SQL text); data, not a credential",
}
