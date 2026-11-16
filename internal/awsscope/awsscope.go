// Package awsscope validates AWS binding fields and builds the session policy
// that downscopes an agent's credentials. It does no I/O, so both the config
// validator and the broker can import it.
package awsscope

import (
	"encoding/json"
	"errors"
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
// "arn:<p>:s3:::<prefix>*/*" in the S3 read deny's NotResource and
// "arn:<p>:s3:::<prefix>*" in the replication and exposure deny's NotResource
// (#307), so anything that could widen it (empty, "*", "?", "/") must never
// get that far.
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
// records (GetRecords), and s3:GetDataAccess, which returns S3 Access Grants
// credentials that the session policy does not bound, so s3_read would not
// hold for them (#322, #329 review; ReadOnlyAccess allows it through s3:Get*).
// Explicit names only: the AWS gate simulates each one.
var dataReadDenies = []string{
	"secretsmanager:GetSecretValue", "ssm:GetParameter", "ssm:GetParameters", "ssm:GetParametersByPath",
	"ssm:GetParameterHistory",
	"dynamodb:GetItem", "dynamodb:BatchGetItem", "dynamodb:Query", "dynamodb:Scan", "dynamodb:PartiQLSelect",
	"dynamodb:GetRecords", "s3:GetDataAccess",
}

// DataReadDenies returns a copy of the data-read actions the investigate scope
// denies outright (secret values, SSM parameters, DynamoDB items, S3 Access
// Grants credentials).
func DataReadDenies() []string { return slices.Clone(dataReadDenies) }

// s3ExpressSession is how S3 Express One Zone directory buckets authorize
// object reads and writes: a CreateSession on the bucket returns session
// credentials for every object in it (ReadOnly or ReadWrite), and s3:GetObject
// is never evaluated (#323). It sits in the S3 read deny, whose exceptions are
// all S3 general purpose ARNs ("arn:<p>:s3:::"), so it is denied on every
// directory bucket ("arn:<p>:s3express:"). s3_read does not reach directory
// buckets: a session covers the whole bucket, so a prefix entry could not be
// honoured, and each mapped bucket would cost the window's tight size budget.
// Basis: ReadOnlyAccess allows it (awsprobe testdata, Sid
// S3ExpressReadOnlySessionObjectAccess); action name from AWS's catalog.
const s3ExpressSession = "s3express:CreateSession"

// investigateStatements are the "investigate" scope's statements, in order:
// Allow *, then S3 object reads outside the allowlist (directory bucket
// sessions everywhere), secret/parameter values, DynamoDB items, and KMS
// decryption other than through S3 denied.
func investigateStatements(partition string, s3Read []string) ([]statement, error) {
	if !partitionRe.MatchString(partition) {
		return nil, fmt.Errorf("session policy: partition %q is not aws, aws-cn or aws-us-gov", partition)
	}
	s3 := statement{Effect: "Deny", Action: []string{"s3:GetObject", "s3:GetObjectVersion", s3ExpressSession}}
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
			return "", &sizeError{n: len(b)}
		}
		return "", fmt.Errorf("the s3_read allowlist makes the %s %d characters; STS allows %d — use fewer or shorter entries", what, len(b), MaxPolicyChars)
	}
	return string(b), nil
}

// sizeError is an elevated policy over STS's limit; ElevatedPolicy turns it
// into the message the config load shows.
type sizeError struct {
	n, base  int
	allowIAM bool
}

func (e *sizeError) Error() string {
	iam := "off"
	if e.allowIAM {
		iam = "on"
	}
	return fmt.Sprintf("the elevated session policy is %d characters; STS allows %d. CuliBox's own denies take %d of them (with allow_iam %s), and a newer CuliBox can deny more, so a config that loaded before an upgrade can be refused after it. The rest is every s3_read entry, elevate.box_bucket_prefix and elevate.pass_role_prefix: use fewer or shorter s3_read entries or shorter prefixes", e.n, MaxPolicyChars, e.base, iam)
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
// intact (spec §6 item 3). sts:AssumeRo* (AssumeRole, AssumeRoleWithSAML,
// AssumeRoleWithWebIdentity and AssumeRoot, in AWS's catalog) in particular stops
// a chained credential, or a member account's root credential, from outliving
// the window by up to an hour.
var elevatedGuardDenies = []string{
	"sts:AssumeRo*", "sts:GetFederationToken", "sts:GetSessionToken",
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

// S3 Access Grants (#322): Access Grants hands out credentials of a location's
// IAM role through s3:GetDataAccess, and those credentials are not bound by the
// session policy, so its S3 read deny would not hold for them. Both scopes deny
// GetDataAccess as a data read (dataReadDenies), and the window also denies
// every Access Grants change (s3AccessGrantsWrites: create, update, associate,
// delete and dissociate of grants, locations, instances and the Identity
// Center link; the instance resource policy put is already s3:Put*Policy*,
// #307), so it can neither use a grant nor make one for another principal. A
// grant, location or instance ARN carries no bucket name, so box buckets get
// no exception. Basis: AWS's action catalog (internal/awsprobe snapshot) for the
// names; that GetDataAccess credentials escape the session policy is inferred
// from what they are (the location role's), not measured.
//
// s3AccessGrantsWrites matches every S3 action whose name has "AccessGrant"
// after a verb ending in "e" (Create, Update, Associate, Delete, Dissociate)
// and no read (Get..., List... end the verb in "t"). Written as a pattern to fit
// STS's limit (#329); a test pins its expansion against the catalog.
const s3AccessGrantsWrites = "s3:*eAccessGrant*"

// s3ExpressExposureDenies (#323) are #307's exposure denies for directory
// buckets: their bucket policies and their access points' policies can grant
// another account, and removing a bucket policy can drop the deny that kept
// one out. Directory buckets have no ACLs and their public access block
// cannot be turned off (S3 Express docs, from memory), so nothing else is
// needed. Unlike #307 there is no box-bucket exception: it would cost a
// directory bucket ARN of the prefix's length, which the size budget does not
// hold at the maximum config (#329), and with CreateSession denied the window
// cannot use a directory bucket anyway.
var s3ExpressExposureDenies = []string{s3ExpressPolicyPuts, "s3express:DeleteBucketPolicy"}

// s3ExpressPolicyPuts is PutBucketPolicy and PutAccessPointPolicy, the only
// s3express actions it matches in AWS's catalog (a test pins it).
const s3ExpressPolicyPuts = "s3express:Put*Policy"

// dataResourcePolicyDenies (#324) are the resource-policy writes that can give
// another principal the data the window denies the session (DynamoDB items
// and stream records, Parameter Store values, secret values), with no read by
// the session: the #307 class outside S3. Denied in every window, with no
// exception: no config names a resource prefix for tables, parameters or
// secrets. Basis per action:
//   - dynamodb:PutResourcePolicy (tables, streams, indexes), ssm:PutResourcePolicy
//     (parameters, shareable to other accounts) and
//     secretsmanager:PutResourcePolicy: each attaches a resource policy that
//     can grant another account. Names sourced from AWS's action catalog
//     (internal/awsprobe snapshot); cross-account use sourced from each
//     service's docs, from memory (the docs site is not reachable from here).
//   - the matching DeleteResourcePolicy: a resource policy can hold the deny
//     that keeps a principal out, so removing it can widen access with no new
//     grant (inferred, as #307 did for s3:DeleteBucketPolicy).
//
// Not denied, by evidence:
//   - KMS key policies and grants (kms:PutKeyPolicy, CreateGrant,
//     ReplicateKey): on their own they give no access to an item, parameter or
//     secret value; the outside account would also need the resource policy
//     above (inferred). CreateGrant is what EBS, RDS and other services call on
//     the caller's behalf during an apply, so a deny would break common stacks.
//   - GetResourcePolicy and the like: reads (Terraform refreshes them).
//   - Sharing a parameter through AWS RAM: RAM attaches the parameter's
//     resource policy, which needs ssm:PutResourcePolicy (inferred, not
//     measured).
var dataResourcePolicyDenies = []string{"dynamodb:PutResourcePolicy", "dynamodb:DeleteResourcePolicy",
	ssmResourcePolicyWrites, "secretsmanager:PutResourcePolicy", "secretsmanager:DeleteResourcePolicy"}

// ssmResourcePolicyWrites is ssm:PutResourcePolicy and ssm:DeleteResourcePolicy
// in AWS's catalog (the read is GetResourcePolicies); a test pins it.
const ssmResourcePolicyWrites = "ssm:*ResourcePolicy"

// ssmParamReads are the SSM parameter reads, split out of dataReadDenies in
// the elevated policy so that AWS's public parameters can be carved out.
var ssmParamReads = []string{"ssm:GetParameter", "ssm:GetParameters", "ssm:GetParametersByPath", "ssm:GetParameterHistory"}

// ssmParamReadsPattern is how the elevated policy writes ssmParamReads, to save
// characters (#329). Basis: AWS's action catalog (internal/awsprobe snapshot)
// lists exactly these four SSM actions starting with GetParameter; a test pins
// that, so a fifth would be seen before it is denied by accident.
const ssmParamReadsPattern = "ssm:GetParameter*"

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
//   - s3:Put*Policy*: resource policies that grant access to other principals:
//     PutBucketPolicy, PutAccessPointPolicy, PutAccessPointPolicyForObjectLambda,
//     PutMultiRegionAccessPointPolicy, PutAccessGrantsInstanceResourcePolicy.
//     No S3 read or ordinary write is named Put...Policy... (GetBucketPolicy,
//     GetBucketPolicyStatus stay allowed).
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
// Access point (Object Lambda included), Multi-Region Access Point and Access
// Grants policies carry no bucket name, so they are denied even for a box
// bucket's access point.
var s3ExposureDenies = []string{"s3:Put*Policy*", "s3:DeleteBucketPolicy", "s3:Put*Acl",
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
//
// Size (#329): STS takes at most 2,048 characters, so denies that share an
// exception share a statement. Merging two Deny statements into one whose
// Action and NotResource are the unions is exact when no action of one can
// ever be requested on a resource the other excepts. Each ARN carries its
// service ("arn:<p>:s3:::", "arn:<p>:ssm:", "arn:<p>:iam:"), and S3 object
// reads are only ever authorized on S3 ARNs, SSM reads on SSM ARNs and PassRole
// on role ARNs, so the read statement holds all three exceptions. The bucket
// denies are kept apart: an s3_read entry names objects, and must not except
// an object ACL there. equiv_test.go checks every decision against the policies
// main rendered before.
func ElevatedPolicy(partition string, s3Read []string, el Elevation) (string, error) {
	st, err := investigateStatements(partition, s3Read)
	if err != nil {
		return "", err
	}
	// st[1], the read deny: the S3 object reads outside s3_read (and the box
	// buckets), the SSM parameter reads outside AWS's public parameters (#211),
	// and, with pass_role_prefix, PassRole outside <prefix>* roles (#302).
	read := &st[1]
	read.Resource = nil // Resource "*" when s3_read is empty; the SSM exception follows
	read.Action = append(slices.Clone(read.Action.([]string)), ssmParamReadsPattern)
	read.NotResource = slices.Clone(read.NotResource)
	if el.BoxBucketPrefix != "" {
		if err := ValidBoxBucketPrefix(el.BoxBucketPrefix); err != nil {
			return "", err
		}
		read.NotResource = append(read.NotResource, "arn:"+partition+":s3:::"+el.BoxBucketPrefix+"*/*")
	}
	read.NotResource = append(read.NotResource, "arn:"+partition+":ssm:*::parameter/aws/service/*")
	if el.PassRolePrefix != "" {
		if err := ValidPassRolePrefix(el.PassRolePrefix); err != nil {
			return "", err
		}
		read.Action = append(read.Action.([]string), "iam:PassRole")
		read.NotResource = append(read.NotResource, "arn:"+partition+":iam::*:role/"+el.PassRolePrefix+"*")
	}
	// Every unconditional deny of the window is one Resource "*" statement, to
	// stay under STS's 2,048 characters (#307, #329): the data reads other than
	// S3 and SSM, the guards, the IAM writes unless allow_iam, the managed copies
	// and the account public access block. With the IAM write denies present
	// (allow_iam off), a guard they already match (iam:CreateAccessKey under
	// iam:Create*) is not written twice; it is denied either way.
	always := slices.DeleteFunc(slices.Clone(dataReadDenies), func(a string) bool { return slices.Contains(ssmParamReads, a) })
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
	always = append(always, s3AccessGrantsWrites)        // #322: no bucket in their ARNs, so no box exception; GetDataAccess is a data read above
	always = append(always, s3ExpressExposureDenies...)  // #323: no box exception
	always = append(always, dataResourcePolicyDenies...) // #324: no resource prefix exists for them
	// Replication and the S3 exposure calls (#307) have an exception only for
	// box buckets: an s3_read entry may name just a prefix of a bucket, and
	// replication copies the whole bucket. The box-bucket ARN "<prefix>*" also
	// covers its objects ("*" crosses "/"), so object ACLs there are excepted too.
	bucketDenies := append([]string{"s3:PutReplicationConfiguration"}, s3ExposureDenies...)
	if el.BoxBucketPrefix == "" {
		always = append(always, bucketDenies...)
	}
	st[2].Action = packActions(always)
	st = append(st, statement{Effect: "Deny", Action: "iam:*", Resource: "arn:" + partition + ":iam::*:role/agentbox-*"})
	if el.BoxBucketPrefix != "" {
		st = append(st, statement{Effect: "Deny", Action: packActions(bucketDenies),
			NotResource: []string{"arn:" + partition + ":s3:::" + el.BoxBucketPrefix + "*"}})
	}
	p, err := marshalPolicy(st, "elevated session policy")
	var se *sizeError
	if errors.As(err, &se) {
		// What the config cannot shrink: no s3_read, no prefixes (#329). When
		// this policy is already that one, it is the base itself; otherwise the
		// base is rendered once, and its own call takes this branch, so the
		// recursion is at most one level deep.
		se.base, se.allowIAM = se.n, el.AllowIAM
		if len(s3Read) > 0 || el.BoxBucketPrefix != "" || el.PassRolePrefix != "" {
			base, berr := ElevatedPolicy(partition, nil, Elevation{AllowIAM: el.AllowIAM})
			var bse *sizeError
			if berr == nil {
				se.base = len(base)
			} else if errors.As(berr, &bse) {
				se.base = bse.n
			}
		}
	}
	return p, err
}

// actionPacks are shorter ways to write a group of denied actions in the
// elevated policy (#329). Each pattern matches, in AWS's action catalog
// (internal/awsprobe snapshot), exactly the actions its covers match, no more
// and no fewer; TestActionPacksAreExact pins that. Each pattern also matches
// every name its covers can match, so an action AWS adds later can only be
// denied more by it, never less. (iam:*tach*Policy for iam:Attach* and
// iam:Detach* was tried and dropped in the #329 review: it would let a future
// iam:Attach... action not ending in "Policy" through.)
var actionPacks = []struct {
	pattern string
	covers  []string
}{
	// GetItem and BatchGetItem; TransactGetItems ends in "s" and is authorized
	// as GetItem anyway.
	{"dynamodb:*GetItem", []string{"dynamodb:GetItem", "dynamodb:BatchGetItem"}},
	{"dynamodb:Export*", []string{"dynamodb:ExportTableToPointInTime"}},
	{"s3:Put*Replication*", []string{"s3:PutReplicationConfiguration"}},
}

// packActions replaces, in order, each pack whose covers are all in actions
// by its pattern, written where the first of them was.
func packActions(actions []string) []string {
	out := slices.Clone(actions)
	for _, p := range actionPacks {
		at := -1
		for _, c := range p.covers {
			i := slices.Index(out, c)
			if i < 0 {
				at = -1
				break
			}
			if at < 0 || i < at {
				at = i
			}
		}
		if at < 0 {
			continue
		}
		// at is the first cover, so nothing before it is deleted
		out = slices.DeleteFunc(out, func(a string) bool { return slices.Contains(p.covers, a) })
		out = slices.Insert(out, at, p.pattern)
	}
	return out
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
