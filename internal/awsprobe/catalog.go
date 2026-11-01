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
		total := 0
		for _, names := range d.Services {
			total += len(names)
		}
		if d.Date != CatalogDate || len(d.Services) < 400 || total < 20000 {
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

// SecretReturning lists actions whose response carries a credential or secret
// although their names pass the screen words (or are not read verbs at all)
// (lowercased action -> reason). MintingScreen puts each in mustDeny whenever a
// residual pattern covers it, regardless of the name screen and HarmlessMinting.
//
// Added by hand, on purpose, and not complete. The SDK models' sensitive trait
// was measured as a source (2026-11-01, botocore 1.34): it flags roughly 600 read
// actions, mostly noise (NextToken, tag keys, a CloudFront Comment), and misses
// glue:GetConnection, whose password sits in a plain map. An action goes here when
// AWS's docs or model say its reply carries the secret.
//
// Known limit: not listed, because a deny would remove a core read and no live
// call has shown a value. redshift:DescribeClusters and the rds Describe calls
// document PendingModifiedValues.MasterUserPassword as a response field, which
// may be masked (unverified). ec2:DescribeVpnConnections models
// TunnelOptions.PreSharedKey as sensitive (unverified). fsx documents
// FsxAdminPassword as always redacted in responses. dms and datasync model a
// Password shared with their write APIs (unverified).
//
// Known limit: secrets a user put in configuration are NOT screened. Lambda
// environment variables (lambda:GetFunctionConfiguration, lambda:ListFunctions),
// EC2 user data and launch-template data, CloudFront origin custom headers and
// SSM parameters are plain data to the catalog; the gate's descriptive
// data-read step reports them and nothing here denies them.
var SecretReturning = map[string]string{
	"lambda:getfunction":                      "Code.Location in the response is a presigned URL to the function's code package",
	"lambda:getlayerversion":                  "Content.Location in the response is a presigned URL to the layer archive",
	"cognito-idp:describeuserpoolclient":      "the response includes the app client's ClientSecret",
	"apigateway:get":                          "GET /apikeys?includeValues=true returns API key values",
	"acm:exportcertificate":                   "PrivateKey in the response is the certificate's private key, under a passphrase the caller chooses",
	"amplify:getapp":                          "basicAuthCredentials in the response is the app's basic-auth username and password",
	"amplify:getbranch":                       "basicAuthCredentials in the response is the branch's basic-auth username and password",
	"amplify:listapps":                        "basicAuthCredentials in the response is each app's basic-auth username and password",
	"amplify:listbranches":                    "basicAuthCredentials in the response is each branch's basic-auth username and password",
	"codepipeline:getjobdetails":              "artifactCredentials in the response are temporary AWS credentials for the artifact store",
	"codepipeline:getthirdpartyjobdetails":    "artifactCredentials in the response are temporary AWS credentials for the artifact store",
	"finspace:getkxconnectionstring":          "signedConnectionString in the response is a signed connection string to the kdb cluster",
	"glue:getconnection":                      "ConnectionProperties in the response holds the connection's password unless the caller sets HidePassword",
	"glue:getconnections":                     "ConnectionProperties in the response holds each connection's password unless the caller sets HidePassword",
	"chime:getattendee":                       "JoinToken in the response is the attendee's meeting join token",
	"chime:getbot":                            "SecurityToken in the response is the bot's token",
	"chime:listbots":                          "SecurityToken in the response is each bot's token",
	"directconnect:describevirtualinterfaces": "authKey in the response is the BGP authentication key",
	"kinesisvideo:geticeserverconfig":         "the response carries TURN server usernames and passwords",
	"mediapackage:describechannel":            "HlsIngest.IngestEndpoints in the response carry the ingest username and password",
	"mediapackage:listchannels":               "HlsIngest.IngestEndpoints in the response carry each channel's ingest username and password",
	"sts:assumerole":                          "mints temporary credentials for the assumed role",
	"sts:assumerolewithsaml":                  "mints temporary credentials from a SAML assertion",
	"sts:assumerolewithwebidentity":           "mints temporary credentials from a web identity token",
	"sts:assumeroot":                          "mints temporary root credentials for a member account",
}

// RetiredServices lists service prefixes AWS has retired and removed from the
// service reference (lowercased prefix -> reason), although managed policies
// such as ReadOnlyAccess still grant them. A pattern of one of these services
// that expands to nothing is not reported as unknown: regenerating the
// snapshot cannot bring it back. Nothing else is exempted. A test fails the
// build if a refreshed catalog lists one of them again.
var RetiredServices = map[string]string{
	"codestar":     "retired by AWS: granted by ReadOnlyAccess, absent from the service reference snapshot (CatalogDate)",
	"deepcomposer": "retired by AWS: granted by ReadOnlyAccess, absent from the service reference snapshot (CatalogDate)",
	"evidently":    "retired by AWS: granted by ReadOnlyAccess, absent from the service reference snapshot (CatalogDate)",
	"iot1click":    "retired by AWS: granted by ReadOnlyAccess, absent from the service reference snapshot (CatalogDate)",
	"iotevents":    "retired by AWS: granted by ReadOnlyAccess, absent from the service reference snapshot (CatalogDate)",
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
			if svc, _, _ := strings.Cut(strings.ToLower(p), ":"); RetiredServices[svc] != "" {
				continue
			}
			if k := strings.ToLower(p); !seenUnknown[k] {
				seenUnknown[k] = true
				unknown = append(unknown, p)
			}
			continue
		}
		for _, a := range expanded {
			k := strings.ToLower(a)
			if seen[k] {
				continue
			}
			if _, secret := SecretReturning[k]; !secret {
				if !screened(a) {
					continue
				}
				if _, ok := HarmlessMinting[k]; ok {
					continue
				}
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
	"iam:getaccountpasswordpolicy":                            "returns the account password policy (length, rotation rules), not a password",
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
	"ec2:getebsdefaultkmskeyid":                               "returns the ID of the default EBS KMS key, not key material",
	"ec2:describekeypairs":                                    "EC2 key pair or serial-console setting; names, fingerprints and status only, no private key",
	"ec2:getserialconsoleaccessstatus":                        "returns whether serial console access is enabled for the account, not a credential",
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
	"s3:getaccountpublicaccessblock":                          "returns the account's S3 Block Public Access settings; no credential",
	"s3:getbucketpublicaccessblock":                           "returns a bucket's S3 Block Public Access settings; no credential",
	"elasticfilesystem:describeaccesspoints":                  "describes EFS access points (path, POSIX user, root directory); no credential",
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
	"ec2:describeclientvpnauthorizationrules":                 "describes Client VPN authorization rules (CIDR and group); no credential",
	"ec2:describetrafficmirrorsessions":                       "describes traffic mirror sessions (source, target, filter); no credential",
	"ec2:describevpcblockpublicaccessexclusions":              "describes VPC Block Public Access exclusions; configuration only",
	"ec2:describevpcblockpublicaccessoptions":                 "describes VPC Block Public Access options; configuration only",
	"ec2:getimageblockpublicaccessstate":                      "returns the AMI Block Public Access state; configuration only",
	"ec2:getsnapshotblockpublicaccessstate":                   "returns the snapshot Block Public Access state; configuration only",
	"ec2:getcapacitymanagermonitoredtagkeys":                  "returns the tag keys EC2 Capacity Manager monitors; tag names only",
	"lambda:getfunctionurlconfig":                             "Lambda function URL configuration: the public endpoint and its auth type, not a signed URL",
	"lambda:listfunctionurlconfigs":                           "Lambda function URL configuration: the public endpoint and its auth type, not a signed URL",
	"sqs:getqueueurl":                                         "returns an SQS queue URL, an unsigned endpoint address",
	"ssm:describesessions":                                    "session, service-access or tag-key metadata; no credential",
	"shield:describedrtaccess":                                "session, service-access or tag-key metadata; no credential",
	"resource-explorer-2:liststreamingaccessforservices":      "session, service-access or tag-key metadata; no credential",
	"route53:listvpcassociationauthorizations":                "session, service-access or tag-key metadata; no credential",
	"tag:gettagkeys":                                          "session, service-access or tag-key metadata; no credential",
	"wafv2:getratebasedstatementmanagedkeys":                  "session, service-access or tag-key metadata; no credential",
}
