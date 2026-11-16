package awsscope

import (
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

// The investigate scope is not changed by #322 (issue scope: the window).
func TestSessionPolicyAccessGrantsUnchangedBy322(t *testing.T) {
	p, _ := SessionPolicy("aws", nil)
	if strings.Contains(p, "AccessGrant") || strings.Contains(p, "GetDataAccess") {
		t.Fatalf("investigate policy changed: %s", p)
	}
}
