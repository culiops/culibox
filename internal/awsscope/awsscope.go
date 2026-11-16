// Package awsscope validates AWS binding fields and builds the session policy
// that downscopes an agent's credentials. It does no I/O, so both the config
// validator and the broker can import it.
package awsscope

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// MaxPolicyChars is STS's limit on an inline session policy's plaintext.
const MaxPolicyChars = 2048

// partitionAlt is the one list of accepted AWS partitions; the role ARN and
// the partition check are both built from it.
const partitionAlt = `aws|aws-cn|aws-us-gov`

var (
	roleARNRe   = regexp.MustCompile(`^arn:(` + partitionAlt + `):iam::(\d{12}):role/[\w+=,.@/-]{1,512}$`)
	partitionRe = regexp.MustCompile(`^(?:` + partitionAlt + `)$`)
	regionRe    = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-\d{1,2}$`)
	bucketRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	prefixRe    = regexp.MustCompile(`^[A-Za-z0-9._=-]+(/[A-Za-z0-9._=-]+)*/$`)
	nameBadRe   = regexp.MustCompile(`[^\w+=,.@-]`)
	// boxPrefixRe: 3 to 40 bucket-name characters ending in "-", so the
	// prefix names a naming convention ("culibox-"), never a wildcard or a
	// stem that would match unrelated buckets. Dots are refused.
	boxPrefixRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,38}-$`)
)

func ValidRoleARN(s string) error {
	if !roleARNRe.MatchString(s) || strings.HasSuffix(s, "/") {
		return fmt.Errorf("role_arn %q is not an IAM role ARN (arn:aws:iam::<12-digit account>:role/<name>)", s)
	}
	return nil
}

func AccountOf(roleARN string) string {
	if m := roleARNRe.FindStringSubmatch(roleARN); m != nil {
		return m[2]
	}
	return ""
}

func PartitionOf(roleARN string) string {
	if m := roleARNRe.FindStringSubmatch(roleARN); m != nil {
		return m[1]
	}
	return ""
}

func ValidRegion(s string) error {
	if !regionRe.MatchString(s) {
		return fmt.Errorf("region %q is not an AWS region name (e.g. ap-southeast-1)", s)
	}
	return nil
}

// ValidS3Entry accepts "bucket" or "bucket/prefix[/]". Wildcards, quotes and
// path tricks are refused: an entry becomes an ARN in a Deny's NotResource, so
// anything that could widen it must never get that far.
func ValidS3Entry(s string) error {
	bucket, prefix, hasPrefix := strings.Cut(s, "/")
	if !bucketRe.MatchString(bucket) || strings.Contains(bucket, "..") {
		return fmt.Errorf("s3_read entry %q: bucket name is not valid", s)
	}
	if hasPrefix && !prefixRe.MatchString(prefix) {
		return fmt.Errorf("s3_read entry %q: a prefix must end in / and may use letters, digits and . _ = - / only (no wildcards)", s)
	}
	return nil
}

// ValidBoxBucketPrefix accepts an elevate.box_bucket_prefix: lowercase
// letters, digits and "-", 3 to 40 characters, ending in "-". It becomes
// "arn:<p>:s3:::<prefix>*/*" in the S3 deny's NotResource, so anything that
// could widen it (empty, "*", "?", "/") must never get that far.
func ValidBoxBucketPrefix(s string) error {
	if !boxPrefixRe.MatchString(s) {
		return fmt.Errorf("box_bucket_prefix %q must be 3 to 40 lowercase letters, digits and -, ending in - (for example culibox-), with no wildcards", s)
	}
	if r, ok := reservedStem(s, reservedBucketStems); ok {
		return fmt.Errorf("box_bucket_prefix %q overlaps %q, a name AWS tools or other CuliBox features create in shared buckets: reading every object there is not what this option is for; pick your own stem (for example culibox-)", s, r)
	}
	return nil
}

// reservedBucketStems are bucket-name stems that AWS services and tools create
// in an account, or that CuliBox itself uses. A box_bucket_prefix that is one
// of them, starts with one, or is a stem of one would open reads on buckets the
// box did not create (CDK and CloudFormation assets, SageMaker data, ...).
var reservedBucketStems = []string{"aws", "cdk-", "sagemaker-", "elasticbeanstalk-", "amplify-",
	"cf-templates-", "codepipeline-", "config-bucket-", "agentbox-"}

// reservedRoleStems are IAM role-name stems that AWS creates (service-linked
// and service roles) or that CuliBox's own roles use.
var reservedRoleStems = []string{"agentbox-", "aws", "service-role"}

// reservedStem reports the reserved stem that s equals, starts with or is a
// stem of, case-insensitively.
func reservedStem(s string, stems []string) (string, bool) {
	l := strings.ToLower(s)
	for _, r := range stems {
		if strings.HasPrefix(l, r) || strings.HasPrefix(r, l) {
			return r, true
		}
	}
	return "", false
}

// SessionName is the RoleSessionName and SourceIdentity for a session.
func SessionName(sessionID string) string {
	n := "agentbox-" + nameBadRe.ReplaceAllString(sessionID, "-")
	if len(n) > 64 {
		n = n[:64]
	}
	return n
}

type statement struct {
	Effect      string         `json:"Effect"`
	Action      any            `json:"Action"`
	Resource    any            `json:"Resource,omitempty"`
	NotResource []string       `json:"NotResource,omitempty"`
	Condition   map[string]any `json:"Condition,omitempty"`
}

// dataReadDenies are the actions that return secret values, Parameter Store
// values (the four value reads: GetParameter, GetParameters,
// GetParametersByPath and GetParameterHistory, per the SSM
// ps-retrieval-authorization doc) and DynamoDB item images, including stream
// records (GetRecords). Explicit names only: the AWS gate simulates each one.
var dataReadDenies = []string{
	"secretsmanager:GetSecretValue", "ssm:GetParameter", "ssm:GetParameters", "ssm:GetParametersByPath",
	"ssm:GetParameterHistory",
	"dynamodb:GetItem", "dynamodb:BatchGetItem", "dynamodb:Query", "dynamodb:Scan", "dynamodb:PartiQLSelect",
	"dynamodb:GetRecords",
}

// DataReadDenies returns a copy of the data-read actions the investigate scope
// denies outright (secret values, SSM parameters, DynamoDB items).
func DataReadDenies() []string { return slices.Clone(dataReadDenies) }

// investigateStatements are the "investigate" scope's statements, in order:
// Allow *, then S3 object reads outside the allowlist, secret/parameter values,
// DynamoDB items, and KMS decryption other than through S3 denied.
func investigateStatements(partition string, s3Read []string) ([]statement, error) {
	if !partitionRe.MatchString(partition) {
		return nil, fmt.Errorf("session policy: partition %q is not aws, aws-cn or aws-us-gov", partition)
	}
	s3 := statement{Effect: "Deny", Action: []string{"s3:GetObject", "s3:GetObjectVersion"}}
	if len(s3Read) == 0 {
		s3.Resource = "*"
	}
	for _, e := range s3Read {
		if err := ValidS3Entry(e); err != nil {
			return nil, err
		}
		s3.NotResource = append(s3.NotResource, "arn:"+partition+":s3:::"+e+"*")
	}
	// A bare bucket entry must cover its objects: "bucket" -> "bucket/*".
	for i, r := range s3.NotResource {
		if !strings.Contains(strings.TrimPrefix(r, "arn:"+partition+":s3:::"), "/") {
			s3.NotResource[i] = strings.TrimSuffix(r, "*") + "/*"
		}
	}
	return []statement{
		{Effect: "Allow", Action: "*", Resource: "*"},
		s3,
		{Effect: "Deny", Action: dataReadDenies, Resource: "*"},
		{Effect: "Deny", Action: "kms:Decrypt", Resource: "*",
			Condition: map[string]any{"StringNotLike": map[string]string{"kms:ViaService": "s3.*.amazonaws.com"}}},
	}, nil
}

// marshalPolicy renders statements as a policy document and enforces STS's
// size limit; what names the policy in the size error.
func marshalPolicy(st []statement, what string) (string, error) {
	doc := struct {
		Version   string      `json:"Version"`
		Statement []statement `json:"Statement"`
	}{"2012-10-17", st}
	b, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	if len(b) > MaxPolicyChars {
		if what == "elevated session policy" {
			return "", fmt.Errorf("the elevated session policy is %d characters; STS allows %d. It grows with every s3_read entry, with elevate.box_bucket_prefix and elevate.pass_role_prefix, and when allow_iam is off — use fewer or shorter s3_read entries or shorter prefixes", len(b), MaxPolicyChars)
		}
		return "", fmt.Errorf("the s3_read allowlist makes the %s %d characters; STS allows %d — use fewer or shorter entries", what, len(b), MaxPolicyChars)
	}
	return string(b), nil
}

// SessionPolicy is the "investigate" scope: everything the role allows, minus
// S3 object reads outside the allowlist, secret/parameter values, DynamoDB
// items, and KMS decryption other than through S3.
func SessionPolicy(partition string, s3Read []string) (string, error) {
	st, err := investigateStatements(partition, s3Read)
	if err != nil {
		return "", err
	}
	return marshalPolicy(st, "session policy")
}

// elevatedGuardDenies keep an elevation window a real bound and keep Prove
// intact (spec §6 item 3). AssumeRole* in particular stops a chained
// credential from outliving the window by up to an hour.
var elevatedGuardDenies = []string{
	"sts:AssumeRole*", "sts:GetFederationToken", "sts:GetSessionToken",
	"iam:CreateAccessKey", "iam:CreateLoginProfile", "iam:UpdateLoginProfile",
	"iam:CreateServiceSpecificCredential", "iam:UploadSSHPublicKey",
	"cloudtrail:StopLogging", "cloudtrail:DeleteTrail", "cloudtrail:UpdateTrail", "cloudtrail:PutEventSelectors",
}

// iamWriteDenies are the IAM write prefixes denied unless allow_iam.
var iamWriteDenies = []string{"iam:Create*", "iam:Delete*", "iam:Put*", "iam:Attach*", "iam:Detach*",
	"iam:Update*", "iam:Add*", "iam:Remove*", "iam:Tag*", "iam:Untag*", "iam:Set*", "iam:Change*"}

// Elevation is what a write-role mint's session policy depends on beyond the
// binding's s3_read: allow_iam and the opt-in box bucket prefix.
type Elevation struct {
	AllowIAM bool
	// BoxBucketPrefix, when set, lets the window read S3 objects in buckets
	// whose name starts with it (the buckets the box creates). Empty = off.
	BoxBucketPrefix string
	// PassRolePrefix, when set, lets the window pass roles whose name starts
	// with it (Lambda, ECS and similar applies need iam:PassRole). Empty = no
	// role may be passed (#302).
	PassRolePrefix string
}

// rolePrefixRe: IAM role-name characters without a wildcard or "/", 3 to 40.
var rolePrefixRe = regexp.MustCompile(`^[A-Za-z0-9+=,.@_-]{3,40}$`)

// ValidPassRolePrefix accepts an elevate.pass_role_prefix. It becomes
// "arn:<p>:iam::*:role/<prefix>*" in a NotResource, so a wildcard, "/" or
// anything that could widen it never gets that far, and neither does a stem
// that covers the agent's own roles or AWS's service roles.
func ValidPassRolePrefix(s string) error {
	if !rolePrefixRe.MatchString(s) {
		return fmt.Errorf("pass_role_prefix %q must be 3 to 40 IAM role-name characters (letters, digits, + = , . @ _ -), with no wildcards or /", s)
	}
	if r, ok := reservedStem(s, reservedRoleStems); ok {
		return fmt.Errorf("pass_role_prefix %q overlaps %q, a role name CuliBox or AWS uses: passing those roles is exactly what this limit prevents; pick your own stem (for example culibox-)", s, r)
	}
	return nil
}

// ssmParamReads are the SSM parameter reads, split out of dataReadDenies in
// the elevated policy so that AWS's public parameters can be carved out.
var ssmParamReads = []string{"ssm:GetParameter", "ssm:GetParameters", "ssm:GetParametersByPath", "ssm:GetParameterHistory"}

// boxBucketCopyDenies are, with box_bucket_prefix on, the managed copies that
// can land data the session may not read in a box bucket without the session
// ever calling s3:GetObject on the source: a DynamoDB export runs with the
// caller's S3 write; the others run as a role passed with iam:PassRole, which
// the window allows (ECS and Lambda applies need it), so the session policy
// never reaches their reads. Replication is denied separately, except from a
// box bucket. Data classes the window does not deny (logs, EC2 disks, RDS
// rows through SQL) are not listed: denying their exports would protect nothing.
var boxBucketCopyDenies = []string{"dynamodb:ExportTableToPointInTime", "rds:StartExportTask",
	"s3:CreateJob", "datasync:StartTaskExecution", "backup:StartRestoreJob"}

// s3ExposureDenies are the S3 calls that open data to the internet or to
// another account (#307). The data-read deny binds only this session; once a
// bucket is public, an unsigned GET (the box egress allows *.amazonaws.com) or
// an outside account reads it with no check on the session. Denied in the
// window except on box buckets. Basis: the S3 API reference's permission notes
// (each API names the IAM action it needs), from memory, not re-fetched.
//   - s3:Put*Policy: resource policies that grant access to other principals:
//     PutBucketPolicy, PutAccessPointPolicy, PutMultiRegionAccessPointPolicy,
//     PutAccessGrantsInstanceResourcePolicy. No S3 read or ordinary write is
//     named Put...Policy (GetBucketPolicy, GetBucketPolicyStatus stay allowed).
//   - s3:DeleteBucketPolicy: a bucket policy often holds the Deny that keeps
//     an older ACL grant or a cross-account allow from working; removing it
//     can expose data with no new grant.
//   - s3:Put*Acl: PutBucketAcl, PutObjectAcl, PutObjectVersionAcl. This also
//     covers CreateBucket, PutObject and CopyObject sent with an ACL header,
//     which need the same actions. No other S3 action ends in Acl.
//   - s3:PutBucketPublicAccessBlock: both PutPublicAccessBlock and
//     DeletePublicAccessBlock on a bucket need it (there is no IAM action
//     named s3:DeletePublicAccessBlock).
//   - s3:PutBucketOwnershipControls: Put and Delete of ownership controls
//     both need it. Leaving BucketOwnerEnforced turns ACLs back on, so ACLs
//     set before they were disabled would apply again (inferred).
//
// Access point, Multi-Region Access Point and Access Grants policies carry no
// bucket name, so they are denied even for a box bucket's access point.
var s3ExposureDenies = []string{"s3:Put*Policy", "s3:DeleteBucketPolicy", "s3:Put*Acl",
	"s3:PutBucketPublicAccessBlock", "s3:PutBucketOwnershipControls"}

// s3AccountExposureDeny is the account-level public access block: the S3
// Control PutPublicAccessBlock and DeletePublicAccessBlock both need it. It
// has no bucket resource, so box_bucket_prefix cannot except it: always denied.
const s3AccountExposureDeny = "s3:PutAccountPublicAccessBlock"

// ElevatedPolicy is the session policy for a write-role mint (M3b): the
// investigate statements (elevation adds writes, not data reads), the guard
// denies, and the IAM write denies unless AllowIAM. Two narrow exceptions
// (#211), both for running Terraform in the window:
//   - AWS's public SSM parameters (/aws/service/..., e.g. AMI ids) are
//     readable. Their ARNs have an empty account field; every parameter in
//     an account carries its account id, so it stays denied.
//   - With BoxBucketPrefix, objects in buckets named <prefix>... are
//     readable, and the managed copies that could fill such a bucket with data
//     the session may not read are denied (boxBucketCopyDenies, replication
//     except from a box bucket).
//
// The S3 exposure calls (s3ExposureDenies, #307) are denied except on box
// buckets, the same way: making a box bucket public exposes only what the box
// put there. The account-level public access block is always denied.
func ElevatedPolicy(partition string, s3Read []string, el Elevation) (string, error) {
	st, err := investigateStatements(partition, s3Read)
	if err != nil {
		return "", err
	}
	if el.BoxBucketPrefix != "" {
		if err := ValidBoxBucketPrefix(el.BoxBucketPrefix); err != nil {
			return "", err
		}
		s3 := &st[1]
		s3.Resource = nil // Resource "*" when s3_read is empty
		s3.NotResource = append(slices.Clone(s3.NotResource), "arn:"+partition+":s3:::"+el.BoxBucketPrefix+"*/*")
	}
	rest := slices.DeleteFunc(slices.Clone(dataReadDenies), func(a string) bool { return slices.Contains(ssmParamReads, a) })
	st[2].Action = rest
	st = slices.Insert(st, 3, statement{Effect: "Deny", Action: ssmParamReads,
		NotResource: []string{"arn:" + partition + ":ssm:*::parameter/aws/service/*"}})
	if el.PassRolePrefix != "" {
		if err := ValidPassRolePrefix(el.PassRolePrefix); err != nil {
			return "", err
		}
	}
	// Every unconditional deny of the window is one Resource "*" statement, to
	// stay under STS's 2,048 characters (#307): the guards, the IAM writes
	// unless allow_iam, the managed copies and the account public access block.
	// With the IAM writes on, a guard they already match (iam:CreateAccessKey
	// under iam:Create*) is not written twice; it is denied either way.
	var always []string
	for _, g := range elevatedGuardDenies {
		if el.AllowIAM || !matchesAny(iamWriteDenies, g) {
			always = append(always, g)
		}
	}
	if el.PassRolePrefix == "" {
		always = append(always, "iam:PassRole") // no role may be passed (#302)
	}
	if !el.AllowIAM {
		always = append(always, iamWriteDenies...)
	}
	// The managed-copy denies are unconditional (#301): a DynamoDB export needs
	// no PassRole and can target a bucket outside the account or one the agent
	// made public, so they matter even when no bucket is readable in the window.
	always = append(always, boxBucketCopyDenies...)
	always = append(always, s3AccountExposureDeny)
	// Replication and the S3 exposure calls (#307) have an exception only for
	// box buckets: an s3_read entry may name just a prefix of a bucket, and
	// replication copies the whole bucket. The box-bucket ARN "<prefix>*" also
	// covers its objects ("*" crosses "/"), so object ACLs there are excepted too.
	bucketDenies := append([]string{"s3:PutReplicationConfiguration"}, s3ExposureDenies...)
	if el.BoxBucketPrefix == "" {
		always = append(always, bucketDenies...)
	}
	st = append(st,
		statement{Effect: "Deny", Action: always, Resource: "*"},
		statement{Effect: "Deny", Action: "iam:*", Resource: "arn:" + partition + ":iam::*:role/agentbox-*"})
	if el.PassRolePrefix != "" {
		st = append(st, statement{Effect: "Deny", Action: "iam:PassRole",
			NotResource: []string{"arn:" + partition + ":iam::*:role/" + el.PassRolePrefix + "*"}})
	}
	if el.BoxBucketPrefix != "" {
		st = append(st, statement{Effect: "Deny", Action: bucketDenies,
			NotResource: []string{"arn:" + partition + ":s3:::" + el.BoxBucketPrefix + "*"}})
	}
	return marshalPolicy(st, "elevated session policy")
}

// matchesAny reports whether action is matched by one of the patterns, each
// a literal or a literal followed by one trailing "*" (the only forms
// iamWriteDenies uses). IAM compares action names case-insensitively.
func matchesAny(patterns []string, action string) bool {
	a := strings.ToLower(action)
	for _, p := range patterns {
		p = strings.ToLower(p)
		if stem, ok := strings.CutSuffix(p, "*"); ok && !strings.Contains(stem, "*") {
			if strings.HasPrefix(a, stem) {
				return true
			}
		} else if a == p {
			return true
		}
	}
	return false
}
