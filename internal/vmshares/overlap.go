package vmshares

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrNotListed is returned when a path is not in the share list.
var ErrNotListed = errors.New("that folder is not in the shared list")

// Skip is an entry Normalize or Derive dropped or changed, with the reason.
type Skip struct{ Path, Reason string }

// Outcome is the result of Add. CoveredBy is set (and List unchanged) when an
// existing share already gives the new one everything it asked for; Absorbed
// lists the shares the new one swallowed.
type Outcome struct {
	List      []Share
	CoveredBy string
	Absorbed  []string
}

// Conflict is a write-access request inside, or over, a read-only share.
type Conflict struct {
	Child, Parent string
	Home          string
}

func (c *Conflict) Error() string {
	ch, pa := Display(c.Child, c.Home), Display(c.Parent, c.Home)
	return fmt.Sprintf("%s needs write access but %s is shared read-only; make %s read-write or remove it", ch, pa, pa)
}

func sameKey(a, b string, fold bool) bool { return Within(a, b, fold) && Within(b, a, fold) }

func sorted(list []Share, fold bool) []Share {
	out := append([]Share{}, list...)
	key := func(s Share) string {
		if fold {
			return strings.ToLower(s.Path)
		}
		return s.Path
	}
	sort.SliceStable(out, func(i, j int) bool {
		if ki, kj := key(out[i]), key(out[j]); ki != kj {
			return ki < kj
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// Add puts s in list under the rule that no share contains another.
func Add(list []Share, s Share, env Env) (Outcome, error) {
	if err := Check(s.Path, env); err != nil {
		return Outcome{}, err
	}
	for _, e := range list {
		if sameKey(e.Path, s.Path, env.Fold) {
			next, err := SetMode(list, e.Path, s.Mode, env)
			return Outcome{List: next}, err
		}
	}
	for _, e := range list {
		if !Within(s.Path, e.Path, env.Fold) {
			continue
		}
		if e.Mode == RW || s.Mode == RO {
			return Outcome{List: list, CoveredBy: e.Path}, nil
		}
		return Outcome{}, &Conflict{Child: s.Path, Parent: e.Path, Home: env.Home}
	}
	next := []Share{}
	var absorbed []string
	for _, e := range list {
		if Within(e.Path, s.Path, env.Fold) {
			if s.Mode == RO && e.Mode == RW {
				return Outcome{}, &Conflict{Child: e.Path, Parent: s.Path, Home: env.Home}
			}
			absorbed = append(absorbed, e.Path)
			continue
		}
		next = append(next, e)
	}
	next = sorted(append(next, s), env.Fold)
	sort.Strings(absorbed)
	return Outcome{List: next, Absorbed: absorbed}, nil
}

// SetMode changes the mode of the listed path. The list invariant (no share
// contains another) means it can never conflict. The input is not modified.
func SetMode(list []Share, path string, m Mode, env Env) ([]Share, error) {
	out := append([]Share{}, list...)
	for i := range out {
		if sameKey(out[i].Path, path, env.Fold) {
			out[i].Mode = m
			return out, nil
		}
	}
	return nil, ErrNotListed
}

// Remove drops the listed path. The input is not modified.
func Remove(list []Share, path string, fold bool) ([]Share, error) {
	out := []Share{}
	found := false
	for _, e := range list {
		if sameKey(e.Path, path, fold) {
			found = true
			continue
		}
		out = append(out, e)
	}
	if !found {
		return nil, ErrNotListed
	}
	return out, nil
}

// Normalize makes list satisfy the invariants: refused entries are dropped,
// duplicates merged (strongest mode wins), a read-only parent of a read-write
// child becomes read-write so existing profiles keep working, covered children
// are dropped. Output is sorted by path.
func Normalize(list []Share, env Env) (out []Share, skipped []Skip) {
	var merged []Share
outer:
	for _, s := range list {
		if err := Check(s.Path, env); err != nil {
			reason := err.Error()
			var r *Refused
			if errors.As(err, &r) {
				reason = r.Reason
			}
			skipped = append(skipped, Skip{Path: s.Path, Reason: reason})
			continue
		}
		for i := range merged {
			if sameKey(merged[i].Path, s.Path, env.Fold) {
				if s.Mode == RW {
					merged[i].Mode = RW
				}
				continue outer
			}
		}
		merged = append(merged, s)
	}
	out = []Share{}
	for _, s := range sorted(merged, env.Fold) {
		covered := false
		for i := range out {
			if !Within(s.Path, out[i].Path, env.Fold) {
				continue
			}
			covered = true
			if out[i].Mode == RO && s.Mode == RW {
				out[i].Mode = RW
				skipped = append(skipped, Skip{Path: out[i].Path, Reason: "made read & write because " + s.Path + " needs write access"})
			}
			break
		}
		if !covered {
			out = append(out, s)
		}
	}
	return out, skipped
}
