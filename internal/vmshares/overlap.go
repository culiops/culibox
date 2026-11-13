package vmshares

import (
	"errors"
	"fmt"
	"path/filepath"
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

// keys are the symlink-resolved spellings of list's paths: the VM mounts the
// resolved path, so every overlap rule compares those, while the list keeps
// the spelling the user typed.
func keys(list []Share) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = resolveSpelling(filepath.Clean(s.Path))
	}
	return out
}

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

// Add puts s in list under the rule that no share contains another, judged on
// the resolved paths.
func Add(list []Share, s Share, env Env) (Outcome, error) {
	// Resolved once: the value that is checked is the value that is compared.
	k := resolveSpelling(filepath.Clean(s.Path))
	if err := checkModeResolved(s.Path, k, s.Mode, env); err != nil {
		return Outcome{}, err
	}
	ks := keys(list)
	for i, e := range list {
		if sameKey(ks[i], k, env.Fold) {
			next, err := SetMode(list, e.Path, s.Mode, env)
			return Outcome{List: next}, err
		}
	}
	for i, e := range list {
		if !Within(k, ks[i], env.Fold) {
			continue
		}
		if e.Mode == RW || s.Mode == RO {
			return Outcome{List: list, CoveredBy: e.Path}, nil
		}
		return Outcome{}, &Conflict{Child: s.Path, Parent: e.Path, Home: env.Home}
	}
	next := []Share{}
	var absorbed []string
	for i, e := range list {
		if Within(ks[i], k, env.Fold) {
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

// SetMode changes the mode of the listed path (any spelling of it). The list
// invariant (no share contains another) means it can never conflict. The
// input is not modified.
func SetMode(list []Share, path string, m Mode, env Env) ([]Share, error) {
	out := append([]Share{}, list...)
	k := resolveSpelling(filepath.Clean(path))
	for i, rk := range keys(out) {
		if sameKey(rk, k, env.Fold) {
			if m == RW {
				if err := checkModeResolved(out[i].Path, rk, m, env); err != nil {
					return nil, err
				}
			}
			out[i].Mode = m
			return out, nil
		}
	}
	return nil, ErrNotListed
}

// Remove drops the listed path (any spelling of it). The input is not modified.
func Remove(list []Share, path string, fold bool) ([]Share, error) {
	out := []Share{}
	found := false
	k := resolveSpelling(filepath.Clean(path))
	for i, rk := range keys(list) {
		if sameKey(rk, k, fold) {
			found = true
			continue
		}
		out = append(out, list[i])
	}
	if !found {
		return nil, ErrNotListed
	}
	return out, nil
}

// Normalize makes list satisfy the invariants, judged on the resolved paths:
// refused entries are dropped, duplicates merged (strongest mode wins), a
// read-only parent of a read-write child becomes read-write so existing
// profiles keep working, covered children are dropped. Output is sorted by path.
func Normalize(list []Share, env Env) (out []Share, skipped []Skip) {
	type keyed struct {
		Share
		key string
	}
	var merged []keyed
outer:
	for _, s := range list {
		clean := filepath.Clean(s.Path)
		k := resolveSpelling(clean) // once: checked, compared and merged on this value
		if err := checkResolved(s.Path, k, env); err != nil {
			reason := err.Error()
			var r *Refused
			if errors.As(err, &r) {
				reason = r.Reason
			}
			skipped = append(skipped, Skip{Path: s.Path, Reason: reason})
			continue
		}
		if s.Mode == RW && insideClaude(clean, k, env) {
			s.Mode = RO
			skipped = append(skipped, Skip{Path: s.Path, Reason: "made read-only: it " + reasonReadOnly})
		}
		for i := range merged {
			if sameKey(merged[i].key, k, env.Fold) {
				if s.Mode == RW {
					merged[i].Mode = RW
				}
				continue outer
			}
		}
		merged = append(merged, keyed{s, k})
	}
	// Parents before children: sort by the resolved key.
	sortKey := func(k string) string {
		if env.Fold {
			return strings.ToLower(k)
		}
		return k
	}
	sort.SliceStable(merged, func(i, j int) bool { return sortKey(merged[i].key) < sortKey(merged[j].key) })
	var kept []keyed
	for _, s := range merged {
		covered := false
		for i := range kept {
			if !Within(s.key, kept[i].key, env.Fold) {
				continue
			}
			covered = true
			if kept[i].Mode == RO && s.Mode == RW {
				kept[i].Mode = RW
				skipped = append(skipped, Skip{Path: kept[i].Path, Reason: "made read & write because " + s.Path + " needs write access"})
			}
			break
		}
		if !covered {
			kept = append(kept, s)
		}
	}
	out = []Share{}
	for _, s := range kept {
		out = append(out, s.Share)
	}
	return sorted(out, env.Fold), skipped
}
