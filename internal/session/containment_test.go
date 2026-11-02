package session

import (
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
	"github.com/culiops/culibox/internal/secrets"
)

// TestManager_CleanBaseContainsSecrets is the end-to-end containment proof: a
// clean-base agent is spawned as `env` (which prints its environment) while a
// decoy secret sits in the daemon env. The decoy must NOT appear in the child's
// environment; the declared agent env must.
func TestManager_CleanBaseContainsSecrets(t *testing.T) {
	t.Setenv("DECOY_LEAK", "should_not_appear")

	res, err := secrets.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	l := launcher.New([]config.AgentConfig{
		{Name: "envprint", Bin: "/usr/bin/env", Base: "clean", Env: map[string]string{"FOO": "bar"}},
	}, res, nil)
	m := NewManager(l, nil, 1<<16, time.Minute)

	s, err := m.Spawn(launcher.SpawnSpec{Agent: "envprint"})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)

	out, cancel, _ := s.Subscribe()
	defer cancel()

	var b strings.Builder
	deadline := time.After(5 * time.Second)
loop:
	for {
		select {
		case chunk, ok := <-out:
			if !ok {
				break loop
			}
			b.WriteString(string(chunk))
		case <-deadline:
			break loop
		}
	}
	got := b.String()

	if strings.Contains(got, "DECOY_LEAK") {
		t.Fatal("clean base LEAKED a daemon secret to the spawned child")
	}
	if !strings.Contains(got, "FOO=bar") {
		t.Fatalf("agent env not applied to the child; got:\n%s", got)
	}
	if !strings.Contains(got, "PATH=") {
		t.Fatalf("curated base PATH missing from the child; got:\n%s", got)
	}
}
