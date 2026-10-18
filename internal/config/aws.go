package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"

	"github.com/culiops/agent-box/internal/awsscope"
	"github.com/culiops/agent-box/internal/hostmatch"
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
}

// AWSElevate lets a human widen one running session to a second, write-capable
// role for a fixed window (M3b). It is part of the binding, so it is in Hash
// and therefore in the box key, and a binding with it gets exclusive boxes.
type AWSElevate struct {
	RoleARN    string `json:"role_arn"`
	MaxMinutes int    `json:"max_minutes,omitempty"` // 30 or 60; 0 means 60
	AllowIAM   bool   `json:"allow_iam,omitempty"`
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
	if !profileNameRe.MatchString(b.SourceProfile) {
		return fmt.Errorf("aws.source_profile %q must be a profile name from ~/.aws/config (letters, digits, . _ -)", b.SourceProfile)
	}
	if err := awsscope.ValidRoleARN(b.RoleARN); err != nil {
		return fmt.Errorf("aws.%w", err)
	}
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
		if _, err := awsscope.ElevatedPolicy(awsscope.PartitionOf(b.RoleARN), b.S3Read, e.AllowIAM); err != nil {
			return fmt.Errorf("aws.elevate: %w", err)
		}
	}
	if _, err := awsscope.SessionPolicy(awsscope.PartitionOf(b.RoleARN), b.S3Read); err != nil {
		return fmt.Errorf("aws: %w", err)
	}
	return nil
}

// Hash is 12 hex characters over the whole binding; the S3 and egress
// allowlists are compared as sets, so their order does not matter.
func (b AWSBinding) Hash() string {
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
	j, _ := json.Marshal(c)
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
		return errors.New("aws binding requires runtime: box (set it explicitly); the host tier cannot contain credentials")
	}
	return a.AWS.Validate()
}
