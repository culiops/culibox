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

var dataReadDenies = []string{
	"secretsmanager:GetSecretValue", "ssm:GetParameter", "ssm:GetParameters", "ssm:GetParametersByPath",
	"dynamodb:GetItem", "dynamodb:BatchGetItem", "dynamodb:Query", "dynamodb:Scan", "dynamodb:PartiQLSelect",
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

// iamWriteDenies are the IAM write prefixes denied unless allow_iam. PassRole
// is deliberately not among them.
var iamWriteDenies = []string{"iam:Create*", "iam:Delete*", "iam:Put*", "iam:Attach*", "iam:Detach*",
	"iam:Update*", "iam:Add*", "iam:Remove*", "iam:Tag*", "iam:Untag*", "iam:Set*", "iam:Change*"}

// ElevatedPolicy is the session policy for a write-role mint (M3b): the
// investigate statements unchanged (elevation adds writes, never data reads),
// the guard denies, and the IAM write denies unless allowIAM.
func ElevatedPolicy(partition string, s3Read []string, allowIAM bool) (string, error) {
	st, err := investigateStatements(partition, s3Read)
	if err != nil {
		return "", err
	}
	st = append(st,
		statement{Effect: "Deny", Action: elevatedGuardDenies, Resource: "*"},
		statement{Effect: "Deny", Action: "iam:*", Resource: "arn:" + partition + ":iam::*:role/agentbox-*"})
	if !allowIAM {
		st = append(st, statement{Effect: "Deny", Action: iamWriteDenies, Resource: "*"})
	}
	return marshalPolicy(st, "elevated session policy")
}
