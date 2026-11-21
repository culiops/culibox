package config

import (
	"slices"
	"strings"
	"testing"
)

func tierDefs() map[string]OptionDef {
	return map[string]OptionDef{
		CriticalRmKey:     {Key: CriticalRmKey, Label: "Skip the critical-rm prompt", Type: OptBool, Env: CriticalRmEnv, Tier: RuntimeBox},
		"plain":           {Key: "plain", Type: OptBool, Flag: "--plain"},
		PermissionModeKey: {Key: PermissionModeKey, Type: OptEnum, Choices: []string{"manual", "plan"}, Flag: "--permission-mode"},
	}
}

func TestOptionTierValidated(t *testing.T) {
	for _, c := range []struct {
		tier    string
		wantErr bool
	}{{"", false}, {"box", false}, {"host", true}} {
		cfg := Defaults()
		cfg.Options = []OptionDef{{Key: "x", Label: "X", Type: OptBool, Flag: "--x", Tier: c.tier}}
		err := cfg.Validate()
		if c.wantErr {
			if err == nil || !strings.Contains(err.Error(), "invalid tier") {
				t.Errorf("tier %q: err = %v, want invalid tier", c.tier, err)
			}
		} else if err != nil {
			t.Errorf("tier %q: unexpected error %v", c.tier, err)
		}
	}
}

func TestOptionOn(t *testing.T) {
	b := OptionDef{Type: OptBool}
	e := OptionDef{Type: OptEnum, Choices: []string{"plan", "auto"}}
	for _, c := range []struct {
		name string
		d    OptionDef
		v    any
		want bool
	}{
		{"bool true", b, true, true},
		{"bool string 1", b, "1", true},
		{"bool false", b, false, false},
		{"enum plan", e, "plan", true},
		{"enum empty", e, "", false},
		{"enum bogus", e, "bogus", false},
		{"enum non-string", e, true, false},
		{"enum nil", e, nil, false},
	} {
		if got := c.d.On(c.v); got != c.want {
			t.Errorf("%s: On(%v) = %v, want %v", c.name, c.v, got, c.want)
		}
	}
}

func TestBoxOnlyOptionReason(t *testing.T) {
	defs := tierDefs()
	if got, want := BoxOnlyOptionReason(defs, map[string]any{CriticalRmKey: true}), `"Skip the critical-rm prompt" runs only in a box`; got != want {
		t.Errorf("reason = %q, want %q", got, want)
	}
	if got := BoxOnlyOptionReason(defs, map[string]any{CriticalRmKey: false}); got != "" {
		t.Errorf("off: reason = %q, want empty", got)
	}
	if got := BoxOnlyOptionReason(defs, map[string]any{"plain": true}); got != "" {
		t.Errorf("host-tier option: reason = %q, want empty", got)
	}
	// An empty label falls back to the key.
	defs["nolabel"] = OptionDef{Key: "nolabel", Type: OptBool, Flag: "--n", Tier: RuntimeBox}
	if got, want := BoxOnlyOptionReason(defs, map[string]any{"nolabel": true}), `"nolabel" runs only in a box`; got != want {
		t.Errorf("reason = %q, want %q", got, want)
	}
}

func TestStripBoxOnly(t *testing.T) {
	defs := tierDefs()
	in := map[string]any{CriticalRmKey: true, "plain": true, "unknown": 1}
	got := StripBoxOnly(defs, in)
	if _, has := got[CriticalRmKey]; has || got["plain"] != true || got["unknown"] != 1 || len(got) != 2 {
		t.Errorf("StripBoxOnly = %v", got)
	}
	if len(in) != 3 || in[CriticalRmKey] != true {
		t.Errorf("input changed: %v", in)
	}
}

func TestOptionFlags(t *testing.T) {
	defs := tierDefs()
	got := OptionFlags(defs, map[string]any{
		"plain": true, CriticalRmKey: true, PermissionModeKey: "plan", "bogus": "x",
	})
	if want := []string{"--permission-mode", "plan", "--plain"}; !slices.Equal(got, want) {
		t.Errorf("flags = %v, want %v", got, want)
	}
	if got := OptionFlags(defs, map[string]any{PermissionModeKey: "nope", "plain": false}); len(got) != 0 {
		t.Errorf("invalid enum / false bool flags = %v, want none", got)
	}
}
