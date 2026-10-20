package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// AWSConnection is a host-owned, named AWS binding. The host says which source
// profile and role it is; a boxed profile only chooses connections by name, and
// one session may bind several. The binding is embedded so config.json stays flat:
// {"name":"a","source_profile":"p","role_arn":"…","region":"…"}.
type AWSConnection struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	AWSBinding         // embedded, JSON flattened
}

// maxBoundAWSConnections caps how many connections one profile may bind.
const maxBoundAWSConnections = 16

// awsConnReserved are names a connection may not take: "default" is the
// legacy inline binding's profile and "agentbox" the box's own.
var awsConnReserved = []string{"default", "agentbox"}

// Validate checks the name, the description and every AWSBinding rule. The
// name rule and the description rule are the static-token ones (tokenNameRe,
// maxTokenDesc, hasControl in statictoken.go), kept in one place on purpose.
func (c AWSConnection) Validate() error {
	if !tokenNameRe.MatchString(c.Name) {
		return fmt.Errorf("name %q must be lowercase letters, digits and - (at most 63, starting with a letter or digit)", c.Name)
	}
	if slices.Contains(awsConnReserved, c.Name) {
		return fmt.Errorf("name %q is reserved", c.Name)
	}
	if len(c.Description) > maxTokenDesc || hasControl(c.Description) {
		return fmt.Errorf("description: at most %d characters, no control characters", maxTokenDesc)
	}
	return c.AWSBinding.Validate()
}

// Clone is a deep copy.
func (c AWSConnection) Clone() AWSConnection {
	out := c
	out.AWSBinding = *c.AWSBinding.Clone()
	return out
}

// DefHash is 12 hex over everything about a connection: its name, description
// and its binding in the same normalised form AWSBinding.Hash digests.
func (c AWSConnection) DefHash() string {
	n := c
	n.AWSBinding = c.AWSBinding.normalized()
	j, _ := json.Marshal(n)
	sum := sha256.Sum256(j)
	return hex.EncodeToString(sum[:6])
}

// ValidateAWSConnections checks each entry and that names are unique. Errors
// carry the entry's index: aws_connections[%d].
func ValidateAWSConnections(list []AWSConnection) error {
	for i, c := range list {
		if err := c.Validate(); err != nil {
			return fmt.Errorf("aws_connections[%d]: %w", i, err)
		}
		for _, p := range list[:i] {
			if p.Name == c.Name {
				return fmt.Errorf("aws_connections[%d]: duplicate name %q", i, c.Name)
			}
		}
	}
	return nil
}

// AWSConnByName finds a connection. The result is a deep copy.
func AWSConnByName(list []AWSConnection, name string) (AWSConnection, bool) {
	for _, c := range list {
		if c.Name == name {
			return c.Clone(), true
		}
	}
	return AWSConnection{}, false
}

// AWSConnsHash is 12 hex over the connections a session binds. Order does not
// matter.
func AWSConnsHash(conns []AWSConnection) string {
	lines := make([]string, 0, len(conns))
	for _, c := range conns {
		lines = append(lines, c.Name+":"+c.DefHash())
	}
	slices.Sort(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:6])
}

// AWSConnectionsRegion is the region every connection shares, or "" when they
// differ or there are none.
func AWSConnectionsRegion(conns []AWSConnection) string {
	if len(conns) == 0 {
		return ""
	}
	r := conns[0].Region
	for _, c := range conns[1:] {
		if c.Region != r {
			return ""
		}
	}
	return r
}

// validateAWSConnBinding checks a profile's aws_connections list on its own:
// boxed only (the runtime must be named "box", as with validateAWS), a bounded
// count, no duplicates, well-formed names, and not combined with the inline
// aws binding. Whether the connections exist is CheckAWSConnections' question.
// An empty list is the legacy shape and always passes.
func validateAWSConnBinding(a AgentConfig) error {
	if len(a.AWSConnections) == 0 {
		return nil
	}
	if a.Runtime != RuntimeBox {
		return errors.New("aws_connections require runtime: box (set it explicitly); the host tier cannot contain credentials")
	}
	if len(a.AWSConnections) > maxBoundAWSConnections {
		return fmt.Errorf("aws_connections: at most %d entries, got %d", maxBoundAWSConnections, len(a.AWSConnections))
	}
	for i, n := range a.AWSConnections {
		if !tokenNameRe.MatchString(n) {
			return fmt.Errorf("aws_connections[%d] %q is not a connection name", i, n)
		}
		if slices.Contains(a.AWSConnections[:i], n) {
			return fmt.Errorf("aws_connections[%d] %q is listed twice", i, n)
		}
	}
	if a.AWS != nil {
		return errors.New("use either the inline aws binding or aws_connections, not both")
	}
	return nil
}

// ValidateAWSConnBinding is validateAWSConnBinding for other packages: the
// launcher and session re-run the profile-binding rule at spawn.
func ValidateAWSConnBinding(a AgentConfig) error { return validateAWSConnBinding(a) }

// CheckAWSConnections refuses a profile that names a connection the instance
// does not define.
func CheckAWSConnections(a AgentConfig, list []AWSConnection) error {
	for i, n := range a.AWSConnections {
		if !slices.ContainsFunc(list, func(c AWSConnection) bool { return c.Name == n }) {
			return fmt.Errorf("aws_connections[%d] %q is not defined in aws_connections", i, n)
		}
	}
	return nil
}
