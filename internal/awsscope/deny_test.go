package awsscope

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

// windowConfigs are elevated configs that differ in every input that changes
// the policy's shape: s3_read, both prefixes, allow_iam, the partition.
func windowConfigs(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for name, c := range map[string]struct {
		partition string
		s3Read    []string
		el        Elevation
	}{
		"plain":       {"aws", nil, Elevation{}},
		"s3_read":     {"aws", []string{"culishop-tfstate", "culishop-logs/AWSLogs/"}, Elevation{}},
		"box":         {"aws", []string{"culishop-tfstate"}, Elevation{BoxBucketPrefix: "culibox-"}},
		"everything":  {"aws", []string{"culishop-tfstate"}, Elevation{AllowIAM: true, BoxBucketPrefix: "culibox-", PassRolePrefix: "culibox-"}},
		"all-maximum": {"aws", longS3Read, Elevation{BoxBucketPrefix: longBoxPrefix, PassRolePrefix: longRolePrefix}},
	} {
		s, err := ElevatedPolicy(c.partition, c.s3Read, c.el)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out[name] = s
	}
	return out
}

// #322: S3 Access Grants hands out credentials of a location's IAM role
// (s3:GetDataAccess) that the session policy does not bound, so the window
// denies GetDataAccess and every Access Grants change that could create or
// widen a grant. A grant, location or instance ARN carries no bucket name, so
// box_bucket_prefix excepts none of them.
func TestElevatedPolicyDeniesS3AccessGrants(t *testing.T) {
	const (
		inst = "arn:aws:s3:us-east-1:123456789012:access-grants/default"
		loc  = inst + "/location/l-1"
		gr   = inst + "/grant/g-1"
	)
	denied := []evalReq{
		{"s3:GetDataAccess", inst, nil},
		{"s3:CreateAccessGrant", inst, nil}, {"s3:CreateAccessGrant", loc, nil},
		{"s3:CreateAccessGrantsInstance", inst, nil},
		{"s3:CreateAccessGrantsLocation", inst, nil},
		{"s3:UpdateAccessGrantsLocation", loc, nil},
		{"s3:AssociateAccessGrantsIdentityCenter", inst, nil},
		{"s3:PutAccessGrantsInstanceResourcePolicy", inst, nil}, // #307
		// the pattern also denies the Access Grants deletes: no grant changes in a window
		{"s3:DeleteAccessGrant", gr, nil}, {"s3:DeleteAccessGrantsLocation", loc, nil},
		{"s3:DeleteAccessGrantsInstance", inst, nil}, {"s3:DeleteAccessGrantsInstanceResourcePolicy", inst, nil},
		{"s3:DissociateAccessGrantsIdentityCenter", inst, nil},
	}
	// Reads stay allowed: Terraform's refresh of Access Grants resources, and
	// seeing which grants exist.
	allowed := []evalReq{
		{"s3:GetAccessGrant", gr, nil}, {"s3:GetAccessGrantsInstance", inst, nil}, {"s3:GetAccessGrantsLocation", loc, nil},
		{"s3:GetAccessGrantsInstanceForPrefix", inst, nil}, {"s3:GetAccessGrantsInstanceResourcePolicy", inst, nil},
		{"s3:ListAccessGrants", inst, nil}, {"s3:ListAccessGrantsInstances", "*", nil},
		{"s3:ListAccessGrantsLocations", inst, nil}, {"s3:ListCallerAccessGrants", inst, nil},
	}
	for name, p := range windowConfigs(t) {
		for _, r := range denied {
			if got := evaluate(t, p, r); got != "deny" {
				t.Errorf("%s: %s on %s is %s", name, r.action, r.resource, got)
			}
		}
		for _, r := range allowed {
			if got := evaluate(t, p, r); got != "allow" {
				t.Errorf("%s: %s on %s is %s, want allow", name, r.action, r.resource, got)
			}
		}
	}
}

// The Access Grants pattern is pinned against AWS's catalog: it covers every
// Access Grants write and no read.
func TestS3AccessGrantsPatternIsEveryWriteAndNoRead(t *testing.T) {
	got := expandCatalog(t, s3AccessGrantsWrites)
	want := []string{"s3:AssociateAccessGrantsIdentityCenter", "s3:CreateAccessGrant", "s3:CreateAccessGrantsInstance",
		"s3:CreateAccessGrantsLocation", "s3:DeleteAccessGrant", "s3:DeleteAccessGrantsInstance",
		"s3:DeleteAccessGrantsInstanceResourcePolicy", "s3:DeleteAccessGrantsLocation",
		"s3:DissociateAccessGrantsIdentityCenter", "s3:UpdateAccessGrantsLocation"}
	if !slices.Equal(got, want) {
		t.Fatalf("%s expands to %v, want %v", s3AccessGrantsWrites, got, want)
	}
	// Every catalog action naming Access Grants is either matched, a read, or
	// already denied by s3:Put*Policy* (#307).
	for _, a := range expandCatalog(t, "s3:*AccessGrant*") {
		_, name, _ := strings.Cut(a, ":")
		if !slices.Contains(want, a) && !strings.HasPrefix(name, "Get") && !strings.HasPrefix(name, "List") && a != "s3:PutAccessGrantsInstanceResourcePolicy" {
			t.Errorf("%s is neither denied nor a read", a)
		}
	}
}

// #323: S3 Express One Zone directory buckets are authorized through
// s3express:CreateSession, not s3:GetObject, so the S3 read deny and s3_read
// never covered them. ReadOnlyAccess allows CreateSession in ReadOnly mode
// (awsprobe testdata), so both scopes deny it on every directory bucket:
// s3_read does not map to directory buckets, and a box bucket gets no
// exception. The window also denies the directory bucket and access point
// policy writes.
func TestPoliciesDenyS3ExpressSessionsAndPolicyWrites(t *testing.T) {
	const (
		dir     = "arn:aws:s3express:us-east-1:123456789012:bucket/human-data--use1-az4--x-s3"
		dirRead = "arn:aws:s3express:us-east-1:123456789012:bucket/culishop-tfstate--use1-az4--x-s3"
		dirBox  = "arn:aws:s3express:us-east-1:123456789012:bucket/culibox-lab--use1-az4--x-s3"
		dirAP   = "arn:aws:s3express:us-east-1:123456789012:accesspoint/culibox-ap--use1-az4--xa-s3"
	)
	all := windowConfigs(t)
	win := maps.Clone(all)
	for name, r := range map[string][]string{"empty": nil, "s3_read": {"culishop-tfstate", "culishop-tfstate--use1-az4--x-s3"}} {
		p, err := SessionPolicy("aws", r)
		if err != nil {
			t.Fatal(err)
		}
		all["investigate "+name] = p
	}
	for name, p := range all {
		for _, res := range []string{dir, dirRead, dirBox} {
			for _, mode := range []map[string]string{nil, {"s3express:SessionMode": "ReadOnly"}, {"s3express:SessionMode": "ReadWrite"}} {
				if got := evaluate(t, p, evalReq{"s3express:CreateSession", res, mode}); got != "deny" {
					t.Errorf("%s: CreateSession on %s (%v) is %s", name, res, mode, got)
				}
			}
		}
		// reads and bucket creation stay allowed
		for _, r := range []evalReq{{"s3express:GetBucketPolicy", dir, nil}, {"s3express:ListAllMyDirectoryBuckets", "*", nil},
			{"s3express:GetAccessPointPolicy", dirAP, nil}, {"s3express:CreateBucket", dirBox, nil}} {
			if got := evaluate(t, p, r); got != "allow" {
				t.Errorf("%s: %s is %s, want allow", name, r.action, got)
			}
		}
	}
	for name, p := range win {
		for _, r := range []evalReq{{"s3express:PutBucketPolicy", dir, nil}, {"s3express:PutBucketPolicy", dirBox, nil},
			{"s3express:DeleteBucketPolicy", dir, nil}, {"s3express:DeleteBucketPolicy", dirBox, nil},
			{"s3express:PutAccessPointPolicy", dirAP, nil}} {
			if got := evaluate(t, p, r); got != "deny" {
				t.Errorf("%s: %s on %s is %s", name, r.action, r.resource, got)
			}
		}
	}
}

// The s3express policy-write pattern is pinned against AWS's catalog.
func TestS3ExpressPolicyPatternIsThePolicyPuts(t *testing.T) {
	got := expandCatalog(t, s3ExpressPolicyPuts)
	if want := []string{"s3express:PutAccessPointPolicy", "s3express:PutBucketPolicy"}; !slices.Equal(got, want) {
		t.Fatalf("%s expands to %v, want %v", s3ExpressPolicyPuts, got, want)
	}
}

// The investigate scope is not changed by #322 (issue scope: the window).
func TestSessionPolicyAccessGrantsUnchangedBy322(t *testing.T) {
	p, _ := SessionPolicy("aws", nil)
	if strings.Contains(p, "AccessGrant") || strings.Contains(p, "GetDataAccess") {
		t.Fatalf("investigate policy changed: %s", p)
	}
}
