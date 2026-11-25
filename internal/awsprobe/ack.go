package awsprobe

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
)

// The pseudo-patterns the gate and the broker put in Refused for an identity
// refusal (not an Identity Center session, wrong account, wrong role path, a
// profile that is not SSO). They say nothing about how broad the permission
// set is, so an acknowledgement can never cover them.
const (
	PatternCaller        = "caller"
	PatternAccount       = "account"
	PatternRole          = "role"
	PatternSourceProfile = "source_profile"
)

// AckSeedHint is the remedy every acknowledge hint carries: a connection
// defined in config.json never holds an acknowledgement (credfile.Merge
// strips it), and only the Credentials screen can save one. Static text.
const AckSeedHint = "a connection defined in config.json must be re-created on the Credentials screen to be acknowledged"

const (
	ackMaxItems   = 4096
	ackMaxItemLen = 256
	ackMaxSummary = 200
)

// Ack is a user's acknowledgement that an sso_direct connection has full
// access: the gate refused it for breadth and the user accepted what it found.
// It is stored on the connection and pins the permission-set caller and the
// refused items the user saw.
type Ack struct {
	State        GateState `json:"state"`            // not_read_only | unverified
	Reason       Reason    `json:"reason,omitempty"` // missing_permissions | missing_context | simulate_denied (unverified only)
	Caller       string    `json:"caller"`
	Refused      []string  `json:"refused,omitempty"`       // lowercased, sorted, unique patterns
	MissingPerms []string  `json:"missing_perms,omitempty"` // sorted, unique
	// Incomplete is simulate_denied when the ack is of a NotReadOnly result
	// whose simulation the permission set could not run. Empty otherwise.
	Incomplete Reason `json:"incomplete,omitempty"`
	// PolicyDigest pins the policy documents when the refused patterns cannot
	// stand for what the set grants: the ack holds a structural pattern
	// (NotAction, Action), which names no content, or it is of a permission
	// set that cannot run the simulation (Reason or Incomplete
	// simulate_denied). Empty otherwise.
	PolicyDigest string    `json:"policy_digest,omitempty"`
	At           time.Time `json:"at"`
	Actor        string    `json:"actor"`
}

// Admission is AdmitSSO's answer. OK means the credentials may reach an agent.
// UnderAck: OK only because of the acknowledgement. Eligible: the result is
// the kind an acknowledgement can cover. Stale: eligible, an ack is present,
// but it does not cover this result.
type Admission struct{ OK, UnderAck, Eligible, Stale bool }

// isStructural reports a refusal pattern that names no content: a NotAction
// or missing-Action statement. They stay ackable (PowerUserAccess uses
// NotAction) but are pinned to the policy digest.
func isStructural(p string) bool {
	p = strings.ToLower(p)
	return p == "notaction" || p == "action"
}

func hasStructural(pats []string) bool { return slices.ContainsFunc(pats, isStructural) }

// simDenied reports a result the permission set could not simulate: an
// Unverified simulate_denied, or a NotReadOnly whose simulation was denied
// after step 5 had already refused patterns.
func simDenied(res GateResult) bool {
	return res.Incomplete == ReasonSimulateDenied || (res.State == GateUnverified && res.Reason == ReasonSimulateDenied)
}

// simDenied reports an ack made from a result simDenied reports.
func (a Ack) simDenied() bool {
	return a.Incomplete == ReasonSimulateDenied || a.Reason == ReasonSimulateDenied
}

// pinsDigest reports an ack that must carry, and match, a policy digest.
func (a Ack) pinsDigest() bool { return hasStructural(a.Refused) || a.simDenied() }

func isPseudoPattern(p string) bool {
	switch strings.ToLower(p) {
	case PatternCaller, PatternAccount, PatternRole, PatternSourceProfile:
		return true
	}
	return false
}

// AckEligible reports whether an acknowledgement may cover res: a definite
// refusal for breadth from a run that finished, or one whose simulation the
// permission set cannot run (pinned to its policy digest), or an unverified
// result for missing permissions, missing condition context or a denied
// simulation. Transient failures (also when they cut short a run that had
// already refused patterns), identity refusals and a result without a known
// caller are never eligible.
func AckEligible(res GateResult) bool {
	if res.Caller == "" {
		return false
	}
	switch res.State {
	case GateNotReadOnly:
		// RoleARN is set only after the identity checks passed (gate step 2).
		if res.RoleARN == "" || len(res.Refused) == 0 {
			return false
		}
		switch res.Incomplete {
		case "":
		case ReasonSimulateDenied:
			if res.PolicyDigest == "" {
				return false
			}
		default: // transient: retry, never acknowledge
			return false
		}
		for _, v := range res.Refused {
			if isPseudoPattern(v.Pattern) {
				return false
			}
		}
		return true
	case GateUnverified:
		switch res.Reason {
		case ReasonMissingPerms:
			return len(res.MissingPerms) > 0
		case ReasonSimulateDenied:
			// The permission set cannot simulate its own policies. The policy
			// contents are what the ack pins, so they must be known.
			if res.RoleARN == "" || res.PolicyDigest == "" {
				return false
			}
			for _, v := range res.Refused {
				if isPseudoPattern(v.Pattern) {
					return false
				}
			}
			return true
		case ReasonMissingContext:
			if len(res.Refused) == 0 {
				return false
			}
			for _, v := range res.Refused {
				if isPseudoPattern(v.Pattern) {
					return false
				}
			}
			return true
		}
	}
	return false
}

func sortedUnique(in []string, fold bool) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if fold {
			s = strings.ToLower(s)
		}
		out = append(out, s)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// NewAck builds the acknowledgement of res. The caller checks AckEligible first.
func NewAck(res GateResult, actor string, at time.Time) Ack {
	pats := make([]string, 0, len(res.Refused))
	for _, v := range res.Refused {
		pats = append(pats, v.Pattern)
	}
	a := Ack{State: res.State, Caller: res.Caller, Refused: sortedUnique(pats, true),
		MissingPerms: sortedUnique(res.MissingPerms, false), At: at, Actor: actor}
	if res.State == GateUnverified {
		a.Reason = res.Reason
	} else if res.Incomplete == ReasonSimulateDenied {
		a.Incomplete = res.Incomplete
	}
	if a.pinsDigest() {
		a.PolicyDigest = res.PolicyDigest
	}
	if len(a.Refused) == 0 {
		a.Refused = nil
	}
	if len(a.MissingPerms) == 0 {
		a.MissingPerms = nil
	}
	return a
}

// Covers reports whether res is no wider than what a acknowledged: the same
// permission-set caller, every refused pattern (case-insensitive) already in
// the ack, and every missing permission already in the ack. A run cut short
// by a transient failure is never covered. A result the permission set could
// not simulate is covered only by an ack made from one, and an ack that pins
// the policy digest (structural or simulate_denied) covers only a result with
// that same, non-empty digest.
func (a Ack) Covers(res GateResult) bool {
	if a.Caller == "" || res.Caller != a.Caller {
		return false
	}
	if res.Incomplete != "" && res.Incomplete != ReasonSimulateDenied {
		return false
	}
	if simDenied(res) && !a.simDenied() {
		return false
	}
	if a.pinsDigest() && (res.PolicyDigest == "" || res.PolicyDigest != a.PolicyDigest) {
		return false
	}
	seen := make(map[string]bool, len(a.Refused))
	for _, p := range a.Refused {
		seen[strings.ToLower(p)] = true
	}
	for _, v := range res.Refused {
		if !seen[strings.ToLower(v.Pattern)] {
			return false
		}
	}
	perms := make(map[string]bool, len(a.MissingPerms))
	for _, p := range a.MissingPerms {
		perms[p] = true
	}
	for _, p := range res.MissingPerms {
		if !perms[p] {
			return false
		}
	}
	return true
}

func ackItem(kind, s string) error {
	if len(s) > ackMaxItemLen {
		return fmt.Errorf("%s item is longer than %d bytes", kind, ackMaxItemLen)
	}
	if strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return fmt.Errorf("%s item has a control character", kind)
	}
	return nil
}

// Validate checks the shape and bounds of a stored acknowledgement.
func (a Ack) Validate() error {
	switch a.State {
	case GateNotReadOnly:
		if a.Reason != "" {
			return errors.New("reason is only for an unverified acknowledgement")
		}
		if a.Incomplete != "" && a.Incomplete != ReasonSimulateDenied {
			return fmt.Errorf("a run cut short by %q cannot be acknowledged", a.Incomplete)
		}
	case GateUnverified:
		if a.Reason != ReasonMissingPerms && a.Reason != ReasonMissingContext && a.Reason != ReasonSimulateDenied {
			return fmt.Errorf("reason %q cannot be acknowledged", a.Reason)
		}
		if a.Incomplete != "" {
			return errors.New("incomplete is only for a not_read_only acknowledgement")
		}
	default:
		return fmt.Errorf("state %q cannot be acknowledged", a.State)
	}
	if a.Caller == "" {
		return errors.New("caller is empty")
	}
	if err := ackItem("caller", a.Caller); err != nil {
		return err
	}
	if err := ackItem("actor", a.Actor); err != nil {
		return err
	}
	if len(a.Refused) > ackMaxItems || len(a.MissingPerms) > ackMaxItems {
		return fmt.Errorf("more than %d items", ackMaxItems)
	}
	for _, p := range a.Refused {
		if err := ackItem("refused", p); err != nil {
			return err
		}
		if isPseudoPattern(p) {
			return fmt.Errorf("refused item %q is an identity refusal and cannot be acknowledged", p)
		}
	}
	for _, p := range a.MissingPerms {
		if err := ackItem("missing_perms", p); err != nil {
			return err
		}
	}
	if a.pinsDigest() {
		if len(a.PolicyDigest) != 64 || strings.Trim(a.PolicyDigest, "0123456789abcdef") != "" {
			return errors.New("an acknowledgement of a NotAction or Action refusal, or of a permission set that cannot simulate, needs a 64-hex policy_digest")
		}
	} else if a.PolicyDigest != "" {
		return errors.New("policy_digest is only for an acknowledgement of a NotAction or Action refusal, or of a permission set that cannot simulate")
	}
	return nil
}

// Summary is a short audit text for the acknowledgement, at most 200 bytes,
// ending in the number of items found.
func (a Ack) Summary() string {
	head := string(a.State)
	if a.Reason != "" {
		head += "/" + string(a.Reason)
	}
	if a.Incomplete != "" {
		head += "/" + string(a.Incomplete)
	}
	head += ": "
	tail := fmt.Sprintf(" (%d refused", len(a.Refused))
	if len(a.MissingPerms) > 0 {
		tail += fmt.Sprintf(", %d missing permissions", len(a.MissingPerms))
	}
	tail += ")"
	items := append(slices.Clone(a.Refused), a.MissingPerms...)
	const more = ", ..."
	var b strings.Builder
	b.WriteString(head)
	budget := ackMaxSummary - len(tail)
	for i, it := range items {
		sep := ""
		if i > 0 {
			sep = ", "
		}
		if b.Len()+len(sep)+len(it)+len(more) > budget {
			if i > 0 {
				b.WriteString(more)
			}
			break
		}
		b.WriteString(sep + it)
	}
	b.WriteString(tail)
	return b.String()
}

// AdmitSSO is the one place a gate result and an acknowledgement become pass
// or refuse. A read-only verdict with a role ARN passes without an ack. An
// eligible result passes only under an ack that covers it. Everything else,
// transient failures (also one that cut short a run with refusals) and
// identity refusals included, is refused whatever ack is held.
func AdmitSSO(res GateResult, ack *Ack) Admission {
	if res.State == GateReadOnly && res.RoleARN != "" {
		return Admission{OK: true}
	}
	if !AckEligible(res) {
		return Admission{}
	}
	adm := Admission{Eligible: true}
	if ack == nil {
		return adm
	}
	if ack.Covers(res) {
		adm.OK, adm.UnderAck = true, true
	} else {
		adm.Stale = true
	}
	return adm
}
