package web

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/store"
)

// revManager is a manager over a fake store with a counted apply.
func revManager(t *testing.T, seeds []config.AgentConfig, opts []config.OptionDef, fs profileStore) (*ProfileManager, *int) {
	t.Helper()
	applies := 0
	pm := NewProfileManager(seeds, opts, "", nil, fs, func([]config.AgentConfig) { applies++ })
	return pm, &applies
}

func mustGet(t *testing.T, pm *ProfileManager, name string) config.AgentConfig {
	t.Helper()
	p, ok := pm.Get(name)
	if !ok {
		t.Fatalf("profile %q not found", name)
	}
	return p
}

func TestProfileRevIsContentDerived(t *testing.T) {
	a := config.AgentConfig{Name: "p", Bin: "bash", Env: map[string]string{}}
	b := config.AgentConfig{Name: "p", Bin: "bash", Env: map[string]string{}}
	for _, k := range []string{"A", "B", "C", "D", "E", "F", "G", "H"} {
		a.Env[k] = "v" + k
	}
	for _, k := range []string{"H", "G", "F", "E", "D", "C", "B", "A"} {
		b.Env[k] = "v" + k
	}
	ra, rb := profileRev(a), profileRev(b)
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(ra) {
		t.Fatalf("rev %q is not 64 lowercase hex chars", ra)
	}
	if ra != rb {
		t.Fatalf("env insertion order changed the rev: %s vs %s", ra, rb)
	}

	open := a
	open.EgressOpen = true
	allow := a
	allow.EgressAllow = []string{"a.example"}
	renamed := a
	renamed.Name = "q"
	for name, changed := range map[string]config.AgentConfig{"egress_open": open, "egress_allow": allow, "name": renamed} {
		if profileRev(changed) == ra {
			t.Errorf("changing %s did not change the rev", name)
		}
	}

	// a restart: a second manager over the same store derives the same rev
	fs := newFakeStore()
	pm1, _ := revManager(t, nil, nil, fs)
	if err := pm1.Create(a); err != nil {
		t.Fatal(err)
	}
	pm2, _ := revManager(t, nil, nil, fs)
	if r1, r2 := profileRev(mustGet(t, pm1, "p")), profileRev(mustGet(t, pm2, "p")); r1 != r2 {
		t.Fatalf("a restarted manager derived another rev: %s vs %s", r1, r2)
	}
}

func TestUpdateAcceptsTheCurrentRevision(t *testing.T) {
	pm, _ := revManager(t, nil, nil, newFakeStore())
	p := config.AgentConfig{Name: "p", Bin: "bash"}
	if err := pm.Create(p); err != nil {
		t.Fatal(err)
	}
	rev := profileRev(mustGet(t, pm, "p"))
	p2 := config.AgentConfig{Name: "p", Bin: "bash", Args: []string{"--one"}}
	if err := pm.Update(p2, rev); err != nil {
		t.Fatalf("Update with the current rev = %v, want nil", err)
	}
	if got := mustGet(t, pm, "p"); !reflect.DeepEqual(got, p2) {
		t.Fatalf("stored = %+v, want %+v", got, p2)
	}
}

func TestUpdateRefusesAStaleRevision(t *testing.T) {
	pm, applies := revManager(t, nil, nil, newFakeStore())
	if err := pm.Create(config.AgentConfig{Name: "p", Bin: "bash"}); err != nil {
		t.Fatal(err)
	}
	rev := profileRev(mustGet(t, pm, "p"))
	p2 := config.AgentConfig{Name: "p", Bin: "bash", Args: []string{"--one"}}
	if err := pm.Update(p2, rev); err != nil {
		t.Fatal(err)
	}
	before := *applies

	p3 := config.AgentConfig{Name: "p", Bin: "bash", Args: []string{"--two"}}
	err := pm.Update(p3, rev)
	if !errors.Is(err, ErrProfileChanged) {
		t.Fatalf("Update with a stale rev = %v, want ErrProfileChanged", err)
	}
	if got := mustGet(t, pm, "p"); !reflect.DeepEqual(got, p2) {
		t.Fatalf("a refused save changed the profile (no merge allowed): %+v, want %+v", got, p2)
	}
	if *applies != before {
		t.Fatal("a refused save refreshed the launcher")
	}
}

func TestUpdateOfADeletedProfileIsRefusedAndNotRecreated(t *testing.T) {
	fs := newFakeStore()
	pm, _ := revManager(t, nil, nil, fs)
	if err := pm.Create(config.AgentConfig{Name: "p", Bin: "bash"}); err != nil {
		t.Fatal(err)
	}
	rev := profileRev(mustGet(t, pm, "p"))
	if err := pm.Delete("p"); err != nil {
		t.Fatal(err)
	}
	err := pm.Update(config.AgentConfig{Name: "p", Bin: "bash", Args: []string{"--two"}}, rev)
	if !errors.Is(err, ErrProfileGone) {
		t.Fatalf("Update of a deleted profile = %v, want ErrProfileGone", err)
	}
	if len(fs.m) != 0 {
		t.Fatalf("the deleted profile was recreated: %+v", fs.m)
	}
}

func TestUpdateRefusesAnotherProfilesRev(t *testing.T) {
	pm, _ := revManager(t, nil, nil, newFakeStore())
	for _, n := range []string{"a", "b"} {
		if err := pm.Create(config.AgentConfig{Name: n, Bin: "bash"}); err != nil {
			t.Fatal(err)
		}
	}
	revA := profileRev(mustGet(t, pm, "a"))
	err := pm.Update(config.AgentConfig{Name: "b", Bin: "bash", Args: []string{"--two"}}, revA)
	if !errors.Is(err, ErrProfileChanged) {
		t.Fatalf("Update of b with a's rev = %v, want ErrProfileChanged", err)
	}
	if len(mustGet(t, pm, "b").Args) != 0 {
		t.Fatal("b was changed by a save that carried a's rev")
	}
}

func TestUpdateOfASeedWithItsRevCreatesTheOverride(t *testing.T) {
	seeds := []config.AgentConfig{{Name: "bash", Bin: "bash"}}
	fs := newFakeStore()
	pm, _ := revManager(t, seeds, nil, fs)
	rev := profileRev(mustGet(t, pm, "bash"))
	if err := pm.Update(config.AgentConfig{Name: "bash", Bin: "bash", Args: []string{"--two"}}, rev); err != nil {
		t.Fatalf("Update of a seed with its rev = %v, want nil", err)
	}
	if _, ok := fs.m["bash"]; !ok {
		t.Fatal("the override was not stored")
	}
	found := false
	for _, v := range pm.Views() {
		if v.Name == "bash" {
			found = true
			if !v.Editable {
				t.Fatal("the overridden seed should now be editable")
			}
		}
	}
	if !found {
		t.Fatal("no view is named bash: the check above did not run")
	}
}

func TestUpdateAcceptsTheRevOfTheScopedProfile(t *testing.T) {
	opts := []config.OptionDef{{Key: "model", Label: "Model", Type: "enum", Choices: []string{"a", "b"}, Flag: "--model", Bins: []string{"claude"}}}
	fs := newFakeStore()
	// A legacy stored profile: bin bash, still carrying an option bound to claude.
	fs.m["p"] = config.AgentConfig{Name: "p", Bin: "bash", Options: map[string]any{"model": "a"}}
	pm, _ := revManager(t, nil, opts, fs)

	got := mustGet(t, pm, "p")
	if _, has := got.Options["model"]; has {
		t.Fatalf("Get should scope the inapplicable option away: %+v", got.Options)
	}
	// The editor holds what Get showed; the guard must hash that same value.
	if err := pm.Update(config.AgentConfig{Name: "p", Bin: "bash", Args: []string{"--two"}}, profileRev(got)); err != nil {
		t.Fatalf("Update with the rev of the scoped profile = %v, want nil", err)
	}
}

func TestUpdateStillValidatesBeforeComparing(t *testing.T) {
	pm, _ := revManager(t, nil, nil, newFakeStore())
	if err := pm.Create(config.AgentConfig{Name: "p", Bin: "bash"}); err != nil {
		t.Fatal(err)
	}
	stale := strings.Repeat("0", 64)
	err := pm.Update(config.AgentConfig{Name: "p", Bin: "bash", Base: "nope"}, stale)
	if err == nil {
		t.Fatal("an invalid profile was accepted")
	}
	if errors.Is(err, ErrProfileChanged) || errors.Is(err, ErrProfileGone) {
		t.Fatalf("validation must come before the revision check, got %v", err)
	}
}

func TestUpdateRefusesAnEmptyRev(t *testing.T) {
	pm, _ := revManager(t, nil, nil, newFakeStore())
	if err := pm.Create(config.AgentConfig{Name: "p", Bin: "bash"}); err != nil {
		t.Fatal(err)
	}
	if err := pm.Update(config.AgentConfig{Name: "p", Bin: "bash", Args: []string{"--two"}}, ""); !errors.Is(err, ErrProfileChanged) {
		t.Fatalf("Update with no rev = %v, want ErrProfileChanged", err)
	}
}

func TestCreateRefusesATakenName(t *testing.T) {
	seeds := []config.AgentConfig{{Name: "seed", Bin: "bash"}}
	fs := newFakeStore()
	pm, applies := revManager(t, seeds, nil, fs)
	if err := pm.Create(config.AgentConfig{Name: "mine", Bin: "bash"}); err != nil {
		t.Fatal(err)
	}
	before := *applies
	for _, name := range []string{"mine", "seed"} {
		err := pm.Create(config.AgentConfig{Name: name, Bin: "bash", Args: []string{"--two"}})
		if !errors.Is(err, ErrProfileExists) {
			t.Fatalf("Create(%q) = %v, want ErrProfileExists", name, err)
		}
	}
	if *applies != before {
		t.Fatal("a refused Create refreshed the launcher")
	}
	if len(mustGet(t, pm, "mine").Args) != 0 {
		t.Fatal("a refused Create overwrote the stored profile")
	}
	if _, ok := fs.m["seed"]; ok {
		t.Fatal("a refused Create stored an override of the seed")
	}
}

func TestConcurrentUpdatesWithOneRevisionOneWins(t *testing.T) {
	pm, _ := revManager(t, nil, nil, newFakeStore())
	if err := pm.Create(config.AgentConfig{Name: "p", Bin: "bash"}); err != nil {
		t.Fatal(err)
	}
	rev := profileRev(mustGet(t, pm, "p"))

	const n = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = pm.Update(config.AgentConfig{Name: "p", Bin: "bash", Args: []string{fmt.Sprint(i)}}, rev)
		}()
	}
	close(start)
	wg.Wait()

	won, changed := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, ErrProfileChanged):
			changed++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if won != 1 || changed != n-1 {
		t.Fatalf("%d saves won and %d were refused, want 1 and %d", won, changed, n-1)
	}
}

func TestUpdateOverAChangedFileLeavesMemoryAndTheLauncherAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	pm, applies := revManager(t, nil, nil, st)
	if err := pm.Create(config.AgentConfig{Name: "p", Bin: "bash"}); err != nil {
		t.Fatal(err)
	}
	rev := profileRev(mustGet(t, pm, "p"))
	hand := []byte(`[{"name":"p","bin":"bash","env":{"EDITED":"by hand"}}]`)
	if err := os.WriteFile(path, hand, 0o600); err != nil {
		t.Fatal(err)
	}
	before, beforeApplies := mustGet(t, pm, "p"), *applies

	err = pm.Update(config.AgentConfig{Name: "p", Bin: "bash", Args: []string{"--two"}}, rev)
	if !errors.Is(err, store.ErrChangedOnDisk) {
		t.Fatalf("Update over a changed file = %v, want store.ErrChangedOnDisk", err)
	}
	if *applies != beforeApplies {
		t.Fatal("the launcher was refreshed by a save the store refused")
	}
	if got := mustGet(t, pm, "p"); !reflect.DeepEqual(got, before) {
		t.Fatalf("memory changed: %+v, want %+v", got, before)
	}
	if b, _ := os.ReadFile(path); string(b) != string(hand) {
		t.Fatalf("the hand edit was overwritten: %s", b)
	}
}
