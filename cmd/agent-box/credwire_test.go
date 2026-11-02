package main

import (
	"errors"
	"io"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/credfile"
)

// A value used only by a web-defined entry must show up as referenced in the
// store CLI. Without the merge the CLI reads config.json alone and would let
// the operator delete a value a web entry still uses without a word.
func TestStoreRmRefusesValueUsedByWebEntry(t *testing.T) {
	r := newStoreRun(t, false)
	if code, out := r.run("init", "", ""); code != 0 {
		t.Fatalf("init = %d %q", code, out)
	}
	if code, out := r.run("set", "v1", testVal); code != 0 {
		t.Fatalf("set = %d %q", code, out)
	}
	err := credfile.Save(r.cfg.DataDir, credfile.File{StaticTokens: []config.StaticToken{{
		Name:   "svc",
		Value:  config.KeyRef{Type: "store", ID: "v1"},
		Env:    []string{"SVC_TOKEN"},
		Egress: []config.TokenEgress{{Host: "api.example.com"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}

	// Control: the unmerged config knows nothing of the web entry.
	if code, out := r.run("list", "", ""); code != 0 || strings.Contains(out, "static_tokens/svc") {
		t.Fatalf("unmerged list = %d %q", code, out)
	}

	merged, st := mergeCredentials(r.cfg, t.TempDir())
	if len(st.Errs) != 0 || st.Bad {
		t.Fatalf("merge state = %+v", st)
	}
	r.cfg = merged
	if code, out := r.run("list", "", ""); code != 0 || !strings.Contains(out, "static_tokens/svc") {
		t.Fatalf("merged list = %d %q, want the web entry as a reference", code, out)
	}
	code, out := r.run("rm", "v1", "")
	if !strings.Contains(out, "still referenced by static_tokens/svc") {
		t.Fatalf("rm = %d %q, want the static_tokens/svc reference", code, out)
	}
}

// A bad entry or a bad file is skipped; the process still has a usable config.
func TestMergeCredentialsNeverFails(t *testing.T) {
	log.SetOutput(io.Discard) // the skipped-entry line is expected
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	dir := t.TempDir()
	cfg := config.Config{DataDir: dir}
	if err := credfile.Save(dir, credfile.File{StaticTokens: []config.StaticToken{{Name: "BAD NAME"}}}); err != nil {
		t.Fatal(err)
	}
	merged, st := mergeCredentials(cfg, t.TempDir())
	if len(merged.StaticTokens) != 0 || len(st.Errs) != 1 || len(st.Problems()) != 1 {
		t.Fatalf("merged %v state %+v", merged.StaticTokens, st)
	}
	if !strings.HasPrefix(st.Problems()[0], "token BAD NAME: ") {
		t.Errorf("problem = %q", st.Problems()[0])
	}
}

// After a restart a profile that names a web entry must still pass
// Config.Validate, which only holds if the merge runs first.
func TestBootValidatesProfilesReferencingWebEntries(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Agents = []config.AgentConfig{{Name: "p", Bin: "claude", Runtime: config.RuntimeBox, Tokens: []string{"svc"}}}
	err := credfile.Save(dir, credfile.File{StaticTokens: []config.StaticToken{{
		Name:   "svc",
		Value:  config.KeyRef{Type: "store", ID: "svc"},
		Env:    []string{"SVC_TOKEN"},
		Egress: []config.TokenEgress{{Host: "api.example.com"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "not defined") {
		t.Fatalf("unmerged Validate = %v, want a not-defined error", err)
	}
	merged, _ := mergeCredentials(cfg, t.TempDir())
	if err := merged.Validate(); err != nil {
		t.Fatalf("merged Validate = %v", err)
	}
}

func bootCfg(dir string) config.Config {
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Agents = []config.AgentConfig{{Name: "p", Bin: "claude", Runtime: config.RuntimeBox, Tokens: []string{"svc"}}}
	return cfg
}

// The ref kinds config reports are the kinds credfile files its errors under.
func TestRefKindsMatchCredfileKinds(t *testing.T) {
	if config.RefAWS != credfile.KindAWS || config.RefGitHubApp != credfile.KindGitHubApp || config.RefToken != credfile.KindToken {
		t.Fatal("config.Ref* and credfile.Kind* have drifted")
	}
}

// A profile bound to a web entry that was skipped at load does not stop the
// daemon (spec section 7): it is warned about, and Validate itself still refuses.
func TestValidateBootToleratesSkippedWebEntry(t *testing.T) {
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	dir := t.TempDir()
	cfg := bootCfg(dir)
	// An invalid entry named "svc" is skipped at load.
	if err := credfile.Save(dir, credfile.File{StaticTokens: []config.StaticToken{{Name: "svc"}}}); err != nil {
		t.Fatal(err)
	}
	merged, st := mergeCredentials(cfg, t.TempDir())
	if len(st.Errs) != 1 {
		t.Fatalf("state = %+v", st)
	}
	if err := merged.Validate(); err == nil {
		t.Fatal("plain Validate accepted a profile naming a skipped entry")
	}
	warns, err := st.validateBoot(merged)
	if err != nil {
		t.Fatalf("validateBoot = %v, want it to continue", err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], `"p"`) || !strings.Contains(warns[0], `"svc"`) {
		t.Fatalf("warnings = %q", warns)
	}
}

// An unreadable credentials.json lost every web entry: any not-defined ref is
// tolerated then, nothing else.
func TestValidateBootToleratesUnreadableFile(t *testing.T) {
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	dir := t.TempDir()
	if err := os.WriteFile(credfile.Path(dir), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := bootCfg(dir)
	merged, st := mergeCredentials(cfg, t.TempDir())
	if !st.Bad {
		t.Fatalf("state = %+v", st)
	}
	if warns, err := st.validateBoot(merged); err != nil || len(warns) != 1 {
		t.Fatalf("warns %q err %v", warns, err)
	}
	merged.DefaultRuntime = "bogus"
	if _, err := st.validateBoot(merged); err == nil {
		t.Fatal("a genuine config error was tolerated")
	}
}

// A genuine seed error stays fatal: a typo in config.json is not a skipped web
// entry, and a skipped entry of another name does not excuse it.
func TestValidateBootStillFailsGenuineErrors(t *testing.T) {
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	dir := t.TempDir()
	if err := credfile.Save(dir, credfile.File{StaticTokens: []config.StaticToken{{Name: "other"}}}); err != nil {
		t.Fatal(err)
	}
	cfg := bootCfg(dir) // names "svc", which nothing skipped
	merged, st := mergeCredentials(cfg, t.TempDir())
	_, err := st.validateBoot(merged)
	var ur *config.UndefinedRefError
	if err == nil || !errors.As(err, &ur) || ur.Name != "svc" {
		t.Fatalf("validateBoot = %v, want the undefined-ref error for svc", err)
	}
	// An error that is not a missing reference.
	cfg2 := bootCfg(dir)
	cfg2.Agents[0].Runtime = "bogus"
	merged2, st2 := mergeCredentials(cfg2, t.TempDir())
	if _, err := st2.validateBoot(merged2); err == nil {
		t.Fatal("invalid runtime accepted")
	}
}
