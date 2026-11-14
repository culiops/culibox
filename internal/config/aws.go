package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"

	"github.com/culiops/culibox/internal/awsscope"
	"github.com/culiops/culibox/internal/hostmatch"
)

// AWSBinding gives a boxed profile AWS access through the credential broker.
// Every field shapes the scope, so all of them go into Hash and therefore into
// the box key: two sessions share a box only when their scopes are identical.
type AWSBinding struct {
	SourceProfile string   `json:"source_profile"`
	RoleARN       string   `json:"role_arn"`
	Region        string   `json:"region"`
	S3Read        []string `json:"s3_read,omitempty"`
	Label         string   `json:"label,omitempty"`
	Prod          bool     `json:"prod,omitempty"`
	// EgressAllow adds hosts (exact or *.suffix) to the egress proxy's default
	// allowlist for this binding's boxes. omitempty keeps a binding without it
	// hashing exactly as it did before egress existed.
	EgressAllow []string `json:"egress_allow,omitempty"`
	// Elevate (M3b) names the write-capable role a human may widen a running
	// session to. omitempty on a nil pointer keeps a pre-M3b binding's hash
	// unchanged.
	Elevate *AWSElevate `json:"elevate,omitempty"`
	// Mode picks how the credentials are made. "" is assume_role (the agent
	// role plus a session policy); AWSModeSSODirect hands the agent the SSO
	// permission set's own credentials. omitempty keeps every existing
	// binding's hash unchanged.
	Mode string `json:"mode,omitempty"`
}

// AWSModeSSODirect is the opt-in binding mode in which the source profile's
// SSO role is used directly, with no AssumeRole and no session policy.
const AWSModeSSODirect = "sso_direct"

// SSODirect reports whether the binding uses the SSO role directly.
func (b AWSBinding) SSODirect() bool { return b.Mode == AWSModeSSODirect }

// AWSElevate lets a human widen one running session to a second, write-capable
// role for a fixed window (M3b). It is part of the binding, so it is in Hash
// and therefore in the box key, and a binding with it gets exclusive boxes.
type AWSElevate struct {
	RoleARN    string `json:"role_arn"`
	MaxMinutes int    `json:"max_minutes,omitempty"` // 30 or 60; 0 means 60
	AllowIAM   bool   `json:"allow_iam,omitempty"`
	// BoxBucketPrefix (#211, opt-in) lets a write window read S3 objects in
	// buckets whose name starts with it, the buckets the box creates, so
	// Terraform can refresh what it uploaded. omitempty keeps every existing
	// binding's hash unchanged.
	BoxBucketPrefix string `json:"box_bucket_prefix,omitempty"`
}

// Scope is what the elevated session policy depends on from this block.
func (e AWSElevate) Scope() awsscope.Elevation {
	return awsscope.Elevation{AllowIAM: e.AllowIAM, BoxBucketPrefix: e.BoxBucketPrefix}
}

// Max is the longest window a grant may ask for.
func (e AWSElevate) Max() int {
	if e.MaxMinutes == 0 {
		return 60
	}
	return e.MaxMinutes
}

const maxEgressAllow = 32

var profileNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// Validate refuses a binding whose fields could not be used safely: each value
// is later written into an INI file or a policy document.
func (b AWSBinding) Validate() error {
	switch b.Mode {
	case "", AWSModeSSODirect:
	default:
		return fmt.Errorf("aws.mode must be \"\" or %q, got %q", AWSModeSSODirect, b.Mode)
	}
	if !profileNameRe.MatchString(b.SourceProfile) {
		return fmt.Errorf("aws.source_profile %q must be a profile name from ~/.aws/config (letters, digits, . _ -)", b.SourceProfile)
	}
	if b.SSODirect() {
		// No role is assumed and no session policy is built, so these fields
		// would be silently ignored; refuse them instead. The source profile
		// being an SSO profile is checked where the profile is read (save,
		// spawn and every mint), not here.
		if b.RoleARN != "" {
			return errors.New("aws.role_arn is not used in sso_direct mode; clear it")
		}
		if b.Elevate != nil {
			return errors.New("aws.elevate is not used in sso_direct mode; clear it")
		}
		if len(b.S3Read) > 0 {
			return errors.New("aws.s3_read is not used in sso_direct mode; clear it")
		}
		return b.validateCommon()
	}
	if err := awsscope.ValidRoleARN(b.RoleARN); err != nil {
		return fmt.Errorf("aws.%w", err)
	}
	if err := b.validateCommon(); err != nil {
		return err
	}
	if e := b.Elevate; e != nil {
		if err := awsscope.ValidRoleARN(e.RoleARN); err != nil {
			return fmt.Errorf("aws.elevate.%w", err)
		}
		if awsscope.AccountOf(e.RoleARN) != awsscope.AccountOf(b.RoleARN) ||
			awsscope.PartitionOf(e.RoleARN) != awsscope.PartitionOf(b.RoleARN) {
			return errors.New("aws.elevate.role_arn must be in the same account and partition as aws.role_arn; cross-account elevation is refused")
		}
		if e.RoleARN == b.RoleARN {
			return errors.New("aws.elevate.role_arn must differ from aws.role_arn: the read role cannot write")
		}
		if e.MaxMinutes != 0 && e.MaxMinutes != 30 && e.MaxMinutes != 60 {
			return fmt.Errorf("aws.elevate.max_minutes must be 30 or 60, got %d", e.MaxMinutes)
		}
		if b.Prod && b.Label == "" {
			return errors.New("aws.label is required on a production binding with elevate: a grant asks you to type it")
		}
		if e.BoxBucketPrefix != "" {
			if err := awsscope.ValidBoxBucketPrefix(e.BoxBucketPrefix); err != nil {
				return fmt.Errorf("aws.elevate.%w", err)
			}
		}
		if _, err := awsscope.ElevatedPolicy(awsscope.PartitionOf(b.RoleARN), b.S3Read, e.Scope()); err != nil {
			return fmt.Errorf("aws.elevate: %w", err)
		}
	}
	if _, err := awsscope.SessionPolicy(awsscope.PartitionOf(b.RoleARN), b.S3Read); err != nil {
		return fmt.Errorf("aws: %w", err)
	}
	return nil
}

// validateCommon checks the fields both modes share: region, label, egress.
func (b AWSBinding) validateCommon() error {
	if err := awsscope.ValidRegion(b.Region); err != nil {
		return fmt.Errorf("aws.%w", err)
	}
	if len(b.Label) > 40 {
		return errors.New("aws.label is longer than 40 characters")
	}
	if len(b.EgressAllow) > maxEgressAllow {
		return fmt.Errorf("aws.egress_allow has more than %d entries", maxEgressAllow)
	}
	for i, p := range b.EgressAllow {
		if err := hostmatch.ValidPattern(p); err != nil {
			return fmt.Errorf("aws.egress_allow[%d] %q: %v", i, p, err)
		}
	}
	return nil
}

// normalized is the binding in the form Hash and AWSConnection.DefHash digest:
// the S3 and egress allowlists are compared as sets, [] and nil marshal alike,
// and an elevate window of 0 is the same as 60.
func (b AWSBinding) normalized() AWSBinding {
	c := b
	c.S3Read = slices.Clone(b.S3Read)
	slices.Sort(c.S3Read)
	c.EgressAllow = slices.Clone(b.EgressAllow)
	slices.Sort(c.EgressAllow)
	if len(c.EgressAllow) == 0 {
		c.EgressAllow = nil // [] and nil must marshal alike, and like the pre-egress hash
	}
	if c.Elevate != nil {
		e := *c.Elevate
		e.MaxMinutes = e.Max() // 0 and 60 are the same window, so the same box key
		c.Elevate = &e
	}
	return c
}

// Hash is 12 hex characters over the whole binding; the S3 and egress
// allowlists are compared as sets, so their order does not matter.
func (b AWSBinding) Hash() string {
	j, _ := json.Marshal(b.normalized())
	sum := sha256.Sum256(j)
	return hex.EncodeToString(sum[:6])
}

// Clone is a deep copy: a session keeps the binding it was spawned with, and a
// later profile edit must never reach it through a shared slice or pointer.
func (b AWSBinding) Clone() *AWSBinding {
	c := b
	c.S3Read = slices.Clone(b.S3Read)
	c.EgressAllow = slices.Clone(b.EgressAllow)
	if b.Elevate != nil {
		e := *b.Elevate
		c.Elevate = &e
	}
	return &c
}

// validateAWS is the one check for an agent's binding, shared by ValidateProfile
// (runtime profiles) and Config.Validate (config.json agents). The runtime must
// be named "box": an empty runtime means "the instance default", and a host
// default (PROD) would save the profile and then refuse every spawn.
func validateAWS(a AgentConfig) error {
	if a.AWS == nil {
		return nil
	}
	if a.Runtime != RuntimeBox {
		return WithField("aws", errors.New("aws binding requires runtime: box (set it explicitly); the host tier cannot contain credentials"))
	}
	return WithField("aws", a.AWS.Validate())
}
