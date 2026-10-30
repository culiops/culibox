// Package awsprobe measures whether an AWS base role can write, using IAM
// policy simulation. It is a leaf package: it holds the result model, the
// classification of a simulation reply, and the SDK simulator. The glue that
// mints credentials and calls the simulator lives in the broker.
//
// For an assume_role binding the probe is a warning, never a gate. A clean
// result means "no broad writes found" among a short fixed action list; scoped
// write grants are not detected. Anything the probe cannot establish is
// Unverified, never NoneFound.
//
// For an sso_direct binding the package also holds the pure half of a
// fail-closed gate: policy.go reads the permission set's policy documents,
// removes only what an unconditional Deny provably covers, and classifies what
// is left by verb (verbs.go). Anything it cannot prove read-only refuses.
package awsprobe

import (
	"context"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/aws/smithy-go"
)

// State is the outcome of a base-role probe.
type State string

const (
	Writes     State = "writes"
	NoneFound  State = "none_found"
	Unverified State = "unverified"
)

// Reason says why a result is Unverified. It is always one of these values and
// never carries AWS error text, an ARN or an account id.
type Reason string

const (
	ReasonSSOExpired     Reason = "sso_expired"
	ReasonAssumeDenied   Reason = "assume_denied"
	ReasonSimulateDenied Reason = "simulate_denied"
	ReasonTimeout        Reason = "timeout"
	ReasonError          Reason = "error"
	ReasonNotChecked     Reason = "not_checked"
	ReasonProbeDisabled  Reason = "probe_disabled"
)

// WriteActions are the actions the probe simulates. Any action shown to a user
// as "allowed" comes from this list.
var WriteActions = []string{"s3:PutObject", "s3:DeleteObject", "s3:CreateBucket", "ec2:RunInstances",
	"ec2:TerminateInstances", "iam:CreateUser", "iam:AttachRolePolicy", "lambda:UpdateFunctionCode",
	"rds:DeleteDBInstance", "dynamodb:PutItem", "cloudformation:CreateStack"}

// Result is one base-role probe outcome.
type Result struct {
	State     State     `json:"state"`
	Actions   []string  `json:"actions,omitempty"` // allowed WriteActions, in WriteActions order; Writes only
	Reason    Reason    `json:"reason,omitempty"`  // Unverified only
	CheckedAt time.Time `json:"checked_at,omitzero"`
}

// Fail is an Unverified result with the given reason.
func Fail(r Reason, at time.Time) Result {
	return Result{State: Unverified, Reason: r, CheckedAt: at}
}

// NotChecked is the result of a record that was never probed.
func NotChecked() Result { return Fail(ReasonNotChecked, time.Time{}) }

// Disabled is the result when the probe is switched off.
func Disabled() Result { return Fail(ReasonProbeDisabled, time.Time{}) }

// Definite reports whether the result is a real measurement.
func (r Result) Definite() bool { return r.State == Writes || r.State == NoneFound }

var knownReasons = map[Reason]bool{
	ReasonSSOExpired: true, ReasonAssumeDenied: true, ReasonSimulateDenied: true, ReasonTimeout: true,
	ReasonError: true, ReasonNotChecked: true, ReasonProbeDisabled: true,
}

// Normalize makes a possibly pre-feature or corrupt record safe to show: an
// unknown or empty state is NotChecked (never a clean result), an unknown
// reason is ReasonError, and only WriteActions survive in Actions.
func (r Result) Normalize() Result {
	switch r.State {
	case Writes:
		var acts []string
		for _, a := range WriteActions {
			if slices.Contains(r.Actions, a) {
				acts = append(acts, a)
			}
		}
		return Result{State: Writes, Actions: acts, CheckedAt: r.CheckedAt}
	case NoneFound:
		return Result{State: NoneFound, CheckedAt: r.CheckedAt}
	case Unverified:
		reason := r.Reason
		if reason == "" {
			reason = ReasonNotChecked
		} else if !knownReasons[reason] {
			reason = ReasonError
		}
		return Result{State: Unverified, Reason: reason, CheckedAt: r.CheckedAt}
	}
	return NotChecked()
}

// Lookup returns the normalized result for key; an absent key is NotChecked.
func Lookup(m map[string]Result, key string) Result {
	r, ok := m[key]
	if !ok {
		return NotChecked()
	}
	return r.Normalize()
}

// NeedsRecheck returns the keys whose result is not definite, sorted.
func NeedsRecheck(m map[string]Result, keys []string) []string {
	var out []string
	for _, k := range keys {
		if !Lookup(m, k).Definite() {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Classify turns a simulation reply (action -> decision) into a Result.
// It iterates WriteActions, never the map, so unrequested names are ignored.
// An allow is proof of writes; NoneFound needs every requested action present
// and denied; anything else is Unverified. CheckedAt is left zero.
func Classify(decisions map[string]string) Result {
	var allowed []string
	clean := true
	for _, a := range WriteActions {
		switch decisions[a] {
		case "allowed":
			allowed = append(allowed, a)
		case "implicitDeny", "explicitDeny":
		default: // missing or an unknown decision
			clean = false
		}
	}
	if len(allowed) > 0 {
		return Result{State: Writes, Actions: allowed}
	}
	if !clean {
		return Fail(ReasonError, time.Time{})
	}
	return Result{State: NoneFound}
}

// SimulateReason maps an error from Simulate to a Reason.
func SimulateReason(err error) Reason {
	if errors.Is(err, context.DeadlineExceeded) {
		return ReasonTimeout
	}
	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch ae.ErrorCode() {
		case "AccessDenied", "AccessDeniedException":
			return ReasonSimulateDenied
		}
	}
	return ReasonError
}

// ReasonText is the short user-facing text for a reason; any other value reads
// "check failed".
func ReasonText(r Reason) string {
	switch r {
	case ReasonSSOExpired:
		return "SSO expired"
	case ReasonAssumeDenied:
		return "assume-role denied"
	case ReasonSimulateDenied:
		return "role cannot simulate itself"
	case ReasonTimeout:
		return "timed out"
	case ReasonNotChecked:
		return "not checked"
	case ReasonProbeDisabled:
		return "probe disabled"
	}
	return "check failed"
}

// Fields is the wire shape shared by session.AWSInfo, session.AWSConnInfo and
// the aws.json row, so the surfaces cannot drift.
type Fields struct {
	BaseAccess       string     `json:"base_access"`
	BaseWriteActions []string   `json:"base_write_actions,omitempty"`
	BaseAccessReason string     `json:"base_access_reason,omitempty"`
	BaseCheckedAt    *time.Time `json:"base_checked_at,omitempty"`
}

// Fields is the wire form of the normalized result.
func (r Result) Fields() Fields {
	n := r.Normalize()
	f := Fields{BaseAccess: string(n.State), BaseWriteActions: n.Actions, BaseAccessReason: string(n.Reason)}
	if !n.CheckedAt.IsZero() {
		at := n.CheckedAt
		f.BaseCheckedAt = &at
	}
	return f
}
