package awsprobe

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// MatchAction reports whether the IAM action pattern (with * and ? globs)
// matches action. Matching ignores case, as IAM does for action names.
func MatchAction(pattern, action string) bool {
	p, a := []rune(strings.ToLower(pattern)), []rune(strings.ToLower(action))
	pi, ai := 0, 0
	star, mark := -1, 0
	for ai < len(a) {
		switch {
		case pi < len(p) && (p[pi] == '?' || p[pi] == a[ai]):
			pi++
			ai++
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, ai
			pi++
		case star >= 0:
			pi = star + 1
			mark++
			ai = mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

// literal is the part of a pattern before its first wildcard.
func literal(pattern string) string {
	if i := strings.IndexAny(pattern, "*?"); i >= 0 {
		return pattern[:i]
	}
	return pattern
}

// statement is the part of an IAM policy statement the analyser reads. The
// has* fields record a key's presence, whatever its value.
type statement struct {
	effect                   string
	action, notAction        []string
	resource                 []string
	hasAction, hasNotAction  bool
	hasNotResource, hasCondn bool
}

// parseDoc decodes one policy document, plain or URL-encoded (as GetRolePolicy
// and GetPolicyVersion return it). Statement may be an object or a list;
// Action, NotAction and Resource may be a string or a list. Key names are
// matched ignoring case, so an oddly-cased key is never silently skipped.
func parseDoc(doc string) ([]statement, error) {
	doc = strings.TrimSpace(doc)
	if !strings.HasPrefix(doc, "{") {
		dec, err := url.PathUnescape(doc)
		if err != nil {
			return nil, fmt.Errorf("policy document is neither JSON nor URL-encoded JSON: %w", err)
		}
		doc = strings.TrimSpace(dec)
	}
	top, err := lowerObject([]byte(doc))
	if err != nil {
		return nil, fmt.Errorf("policy document: %w", err)
	}
	raw, ok := top["statement"]
	if !ok {
		return nil, errors.New("policy document has no Statement")
	}
	var list []json.RawMessage
	if strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
		list = []json.RawMessage{raw}
	} else if err := json.Unmarshal(raw, &list); err != nil {
		return nil, errors.New("policy Statement is neither an object nor a list")
	}
	out := make([]statement, 0, len(list))
	for _, r := range list {
		f, err := lowerObject(r)
		if err != nil {
			return nil, fmt.Errorf("policy statement: %w", err)
		}
		var s statement
		if err := json.Unmarshal(f["effect"], &s.effect); err != nil {
			return nil, errors.New("policy statement has no string Effect")
		}
		if s.action, s.hasAction, err = stringOrList(f, "action"); err != nil {
			return nil, err
		}
		if s.notAction, s.hasNotAction, err = stringOrList(f, "notaction"); err != nil {
			return nil, err
		}
		if s.resource, _, err = stringOrList(f, "resource"); err != nil {
			return nil, err
		}
		if s.hasAction && s.hasNotAction {
			return nil, errors.New("policy statement has both Action and NotAction")
		}
		_, s.hasNotResource = f["notresource"]
		_, s.hasCondn = f["condition"]
		out = append(out, s)
	}
	return out, nil
}

// lowerObject unmarshals a JSON object with lowercased keys. Two keys that
// differ only in case are an error.
func lowerObject(b []byte) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil || m == nil {
		return nil, errors.New("not a JSON object")
	}
	out := make(map[string]json.RawMessage, len(m))
	for k, v := range m {
		lk := strings.ToLower(k)
		if _, dup := out[lk]; dup {
			return nil, fmt.Errorf("duplicate key %q", k)
		}
		out[lk] = v
	}
	return out, nil
}

// stringOrList reads key as a string or a list of strings. present is false
// only when the key is absent or null.
func stringOrList(f map[string]json.RawMessage, key string) (vals []string, present bool, err error) {
	raw, ok := f[key]
	if !ok || string(raw) == "null" {
		return nil, false, nil
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}, true, nil
	}
	if err := json.Unmarshal(raw, &vals); err != nil {
		return nil, false, fmt.Errorf("policy %s is neither a string nor a list of strings", key)
	}
	return vals, true, nil
}

// unconditionalAll reports whether a Deny applies to every request: no
// Condition (even an empty one), no NotResource, and Resource includes "*".
func (s statement) unconditionalAll() bool {
	return !s.hasCondn && !s.hasNotResource && slices.Contains(s.resource, "*")
}

// denyCovers reports whether Deny pattern d provably covers every action Allow
// pattern a can match: d is "*", equal to a, or a single trailing * whose
// prefix starts a's literal part. Anything else (a broader allow, a deny with
// an inner wildcard) does not cover.
func denyCovers(d, a string) bool {
	d, a = strings.ToLower(d), strings.ToLower(a)
	if d == "*" || d == a {
		return true
	}
	pre, ok := strings.CutSuffix(d, "*")
	return ok && !strings.ContainsAny(pre, "*?") && strings.HasPrefix(literal(a), pre)
}

// notActionCovers reports whether a Deny with NotAction exempt covers Allow
// pattern a: a names one service and no exempt entry can name that service.
// An exempt entry without a service, or one whose service glob matches, shares
// it; so does an Allow with a wildcard service.
func notActionCovers(exempt []string, a string) bool {
	svc, _, ok := strings.Cut(strings.ToLower(a), ":")
	if !ok || svc == "" || strings.ContainsAny(svc, "*?") || len(exempt) == 0 {
		return false
	}
	for _, e := range exempt {
		esvc, _, ok := strings.Cut(e, ":")
		if !ok || MatchAction(esvc, svc) {
			return false
		}
	}
	return true
}

// The Allow shapes Residual refuses: well-formed policy, not read-only. Other
// Residual errors mean the document could not be read.
var (
	errAllowNotAction = errors.New("an Allow with NotAction grants everything it does not list")
	errAllowNoAction  = errors.New("an Allow without Action")
)

// Residual returns the Allow action patterns across the permission set's
// policy documents that no unconditional Resource "*" Deny provably covers
// (gate step 4). Patterns are deduplicated ignoring case and sorted. Read
// verbs are kept: later gate steps expand them. An Allow with NotAction, an
// Allow without Action, or an unparseable document is an error.
func Residual(docs []string) ([]string, error) {
	var allows []string
	var denies [][]string // each: the Action list of one covering Deny
	var exempts [][]string
	for i, d := range docs {
		stmts, err := parseDoc(d)
		if err != nil {
			return nil, fmt.Errorf("document %d: %w", i+1, err)
		}
		for _, s := range stmts {
			switch {
			case strings.EqualFold(s.effect, "Allow"):
				if s.hasNotAction {
					return nil, fmt.Errorf("document %d: %w", i+1, errAllowNotAction)
				}
				if !s.hasAction {
					return nil, fmt.Errorf("document %d: %w", i+1, errAllowNoAction)
				}
				allows = append(allows, s.action...)
			case s.effect == "Deny":
				if !s.unconditionalAll() {
					continue
				}
				if s.hasNotAction {
					exempts = append(exempts, s.notAction)
				} else if s.hasAction {
					denies = append(denies, s.action)
				}
			default:
				return nil, fmt.Errorf("document %d: unknown Effect %q", i+1, s.effect)
			}
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, a := range allows {
		k := strings.ToLower(a)
		if seen[k] || covered(a, denies, exempts) {
			continue
		}
		seen[k] = true
		out = append(out, a)
	}
	slices.SortFunc(out, func(x, y string) int { return strings.Compare(strings.ToLower(x), strings.ToLower(y)) })
	return out, nil
}

func covered(a string, denies, exempts [][]string) bool {
	for _, ds := range denies {
		for _, d := range ds {
			if denyCovers(d, a) {
				return true
			}
		}
	}
	for _, ex := range exempts {
		if notActionCovers(ex, a) {
			return true
		}
	}
	return false
}

// Verdict names a residual pattern the gate refuses, and why.
type Verdict struct{ Pattern, Why string }

// Classified is the outcome of gate step 5 (before the credential-minting
// screen). Refused lists every refusing pattern, not just the first, so an
// admin can fix them all at once. SideEffect maps a pattern to the concrete
// actions the simulation must show denied.
type Classified struct {
	Refused    []Verdict
	SideEffect map[string][]string
}

// ClassifyResidual classifies each residual pattern: a NonStandard entry
// decides first (so the table can only tighten), then a read verb passes, and
// anything else refuses.
func ClassifyResidual(residual []string) Classified {
	c := Classified{SideEffect: map[string][]string{}}
	for _, p := range residual {
		if e, ok := NonStandard[strings.ToLower(p)]; ok {
			if len(e.SideEffect) > 0 {
				c.SideEffect[p] = slices.Clone(e.SideEffect)
			}
			continue
		}
		if isReadVerb(p) {
			continue
		}
		c.Refused = append(c.Refused, Verdict{Pattern: p, Why: refuseWhy(p)})
	}
	return c
}

func refuseWhy(p string) string {
	low := strings.ToLower(p)
	if strings.Trim(low, "*?") == "" {
		return "grants every action"
	}
	svc, verb, ok := strings.Cut(low, ":")
	switch {
	case !ok || svc == "":
		return "is not a service:action pattern"
	case strings.ContainsAny(svc, "*?"):
		return "has a wildcard in the service name"
	case literal(verb) == "":
		return "grants every " + svc + " action"
	}
	return "its verb is neither a read verb nor in the reviewed non-standard table"
}
