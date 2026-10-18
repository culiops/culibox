package account

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// inheritedSettingsKeys are the only settings.json keys an InheritPersonal
// account copies from the default account. statusLine stays agent-box's own
// (mergeSettings), and everything else (including a top-level `deny`, which
// Claude Code does not read) is left behind on purpose.
var inheritedSettingsKeys = []string{"effortLevel", "enabledPlugins", "env", "hooks", "model", "permissions"}

// inheritedEnvRefused reports env names that must never be copied into an
// account: the same rules a session's environment already follows (every AB_*
// daemon variable, and the provider credential variables an account supplies
// for itself).
func inheritedEnvRefused(name string) bool {
	return strings.HasPrefix(name, "AB_") || IsAuthEnv(name)
}

// defaultInheritable returns the inheritable keys of the default account's
// settings.json. ok=false means the file exists but cannot be read or parsed
// (e.g. mid-write): the caller keeps the last good copy. A missing file is a
// normal "nothing to inherit".
func (s *Store) defaultInheritable() (map[string]any, bool) {
	b, err := os.ReadFile(filepath.Join(s.defaultDir, "settings.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, true
	}
	if err != nil {
		return nil, false
	}
	var all map[string]any
	if json.Unmarshal(b, &all) != nil {
		return nil, false
	}
	out := map[string]any{}
	for _, k := range inheritedSettingsKeys {
		v, present := all[k]
		if !present {
			continue
		}
		if k == "env" {
			env, isObj := v.(map[string]any)
			if !isObj {
				continue
			}
			kept := map[string]any{}
			var dropped []string
			for n, ev := range env {
				if inheritedEnvRefused(n) {
					dropped = append(dropped, n)
					continue
				}
				kept[n] = ev
			}
			sort.Strings(dropped)
			for _, n := range dropped { // names only, never values
				log.Printf("account settings: env %s not inherited (daemon or credential variable)", n)
			}
			if len(kept) == 0 && len(env) > 0 {
				continue // nothing safe to copy
			}
			v = kept
		}
		out[k] = v
	}
	return out, true
}

// syncSettings keeps the account's settings.json copies of the default's
// inheritable keys current, and records which keys it copied in the manifest.
//
// Ownership rule: while inherit is on, the default is the source of truth for
// the six keys. A key present there overwrites the account's same-named key
// (adopting hand-applied copies that were never recorded) and is recorded. A
// key absent there is removed only if agent-box recorded copying it earlier; a
// key the user set by hand and agent-box never copied survives. Turning inherit
// off removes exactly the recorded keys.
func (s *Store) syncSettings(cr *os.Root, on bool, man *manifest) error {
	want := map[string]any{}
	if on {
		w, ok := s.defaultInheritable()
		if !ok {
			return nil // keep the last good copy and its record
		}
		want = w
	}
	cur := map[string]any{}
	if b, err := cr.ReadFile("settings.json"); err == nil {
		if err := json.Unmarshal(b, &cur); err != nil {
			return errors.New("account: settings.json is not valid JSON; fix or remove it")
		}
		if cur == nil {
			cur = map[string]any{}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	next := make(map[string]any, len(cur))
	for k, v := range cur {
		next[k] = v
	}
	for _, k := range man.Settings {
		if _, keep := want[k]; !keep {
			delete(next, k)
		}
	}
	rec := make([]string, 0, len(want))
	for k, v := range want {
		next[k] = v
		rec = append(rec, k)
	}
	sort.Strings(rec)
	man.Settings = rec
	if reflect.DeepEqual(cur, next) {
		return nil
	}
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(cr, "settings.json", b, 0o600)
}

// Refresh re-provisions one account so a spawn sees the default's current
// settings, MCP servers and statusLine delegate rather than the daemon-start
// snapshot. The default account has nothing to provision.
func (s *Store) Refresh(name string) error {
	if name == "" || name == Default {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.byName[name]
	if !ok {
		return fmt.Errorf("%w %q", ErrUnknown, name)
	}
	return s.provision(s.resolvedLocked(a))
}
