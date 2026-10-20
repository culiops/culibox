package main

import (
	"io"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/credfile"
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
