package web

import (
	"testing"

	"github.com/culiops/culiops-agent-console/internal/config"
)

func TestProfiles(t *testing.T) {
	c := config.Config{
		Options: []config.OptionDef{{Key: "skip", Label: "Skip", Type: "bool", Flag: "-x"}},
		Agents: []config.AgentConfig{
			{Name: "claude", Bin: "claude", Options: map[string]any{"skip": true}},
			{Name: "bash", Bin: "bash"},
		},
	}
	pvs := Profiles(c)
	if len(pvs) != 2 {
		t.Fatalf("want 2 profiles, got %d", len(pvs))
	}
	if pvs[0].Name != "claude" || len(pvs[0].Options) != 1 ||
		pvs[0].Options[0].Key != "skip" || pvs[0].Options[0].Value != true {
		t.Fatalf("claude options wrong: %+v", pvs[0])
	}
	if len(pvs[1].Options) != 0 {
		t.Fatalf("bash should have no options: %+v", pvs[1])
	}
}
