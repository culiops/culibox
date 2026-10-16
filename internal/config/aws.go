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
}

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
	if _, err := awsscope.SessionPolicy(awsscope.PartitionOf(b.RoleARN), b.S3Read); err != nil {
		return fmt.Errorf("aws: %w", err)
	}
	return nil
}

// Hash is 12 hex characters over the whole binding; the S3 allowlist is
// compared as a set, so its order does not matter.
func (b AWSBinding) Hash() string {
	c := b
	c.S3Read = slices.Clone(b.S3Read)
	slices.Sort(c.S3Read)
	j, _ := json.Marshal(c)
	sum := sha256.Sum256(j)
	return hex.EncodeToString(sum[:6])
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
