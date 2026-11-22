package config

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func legacyDefs() map[string]OptionDef {
	return map[string]OptionDef{
		"skip": {Key: "skip", Label: "Skip permissions", Type: OptBool, Flag: "--dangerously-skip-permissions", Bins: []string{"claude"}},
		PermissionModeKey: {Key: PermissionModeKey, Label: "Permission mode", Type: OptEnum, Flag: "--permission-mode",
			Choices: []string{"manual", "acceptEdits", "auto", "plan", "bypassPermissions"}, Bins: []string{"claude"}},
		CriticalRmKey: {Key: CriticalRmKey, Label: "Skip the critical-rm prompt", Type: OptBool, Env: CriticalRmEnv, Bins: []string{"claude"}, Tier: RuntimeBox},
	}
}

func TestArgsBypass(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want bool
	}{
		{"none", nil, false},
		{"bare flag", []string{"--dangerously-skip-permissions"}, true},
		{"mode two args", []string{"--permission-mode", "bypassPermissions"}, true},
		{"mode one arg", []string{"--permission-mode=bypassPermissions"}, true},
		{"another mode", []string{"--permission-mode", "plan"}, false},
		{"mode flag last", []string{"--permission-mode"}, false},
		{"unrecognised spelling", []string{"--permission-mode", "bypassPermissions "}, false},
		{"among others", []string{"--model", "opus", "--dangerously-skip-permissions"}, true},
	} {
		if got := ArgsBypass(c.args); got != c.want {
			t.Errorf("%s: ArgsBypass(%v) = %v, want %v", c.name, c.args, got, c.want)
		}
	}
}

func TestTruthy(t *testing.T) {
	for v, want := range map[any]bool{true: true, false: false, "true": true, "1": true, "0": false, "x": false, float64(1): true, float64(0): false, nil: false, 3: false} {
		if got := Truthy(v); got != want {
			t.Errorf("Truthy(%v) = %v, want %v", v, got, want)
		}
	}
}

func TestSupersedeLegacyBypass(t *testing.T) {
	defs := legacyDefs()
	for _, c := range []struct {
		name string
		opts map[string]any
		want []string
	}{
		{"mode wins", map[string]any{"skip": true, PermissionModeKey: "plan"}, []string{"--permission-mode", "plan"}},
		{"legacy alone", map[string]any{"skip": true}, []string{"--dangerously-skip-permissions"}},
		{"empty mode", map[string]any{"skip": true, PermissionModeKey: ""}, []string{"--dangerously-skip-permissions"}},
	} {
		got := OptionFlags(defs, SupersedeLegacyBypass(defs, c.opts))
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: flags = %v, want %v", c.name, got, c.want)
		}
	}
	in := map[string]any{"skip": true, PermissionModeKey: "plan"}
	SupersedeLegacyBypass(defs, in)
	if len(in) != 2 {
		t.Errorf("input changed: %v", in)
	}
}

func TestLegacyBypassKeyByFlagNotName(t *testing.T) {
	a := AgentConfig{Bin: "claude", Options: map[string]any{"skip_permissions": true}}
	noFlag := map[string]OptionDef{"skip_permissions": {Key: "skip_permissions", Type: OptBool, Env: "X"}}
	if k := LegacyBypassKey(noFlag, a); k != "" {
		t.Errorf("a bool with no flag is legacy: %q", k)
	}
	yolo := map[string]OptionDef{
		"yolo":            {Key: "yolo", Type: OptBool, Flag: "--dangerously-skip-permissions"},
		PermissionModeKey: legacyDefs()[PermissionModeKey],
	}
	for _, v := range []bool{false, true} {
		a := AgentConfig{Bin: "claude", Options: map[string]any{"yolo": v}}
		if k := LegacyBypassKey(yolo, a); k != "yolo" {
			t.Errorf("yolo=%v: key = %q, want yolo", v, k)
		}
	}
	// A legacy def that does not apply to the bin does not count.
	a = AgentConfig{Bin: "bash", Options: map[string]any{"skip": true}}
	if k := LegacyBypassKey(legacyDefs(), a); k != "" {
		t.Errorf("other bin: key = %q", k)
	}
}

func TestIsLegacyBypassDef(t *testing.T) {
	if !IsLegacyBypassDef(OptionDef{Type: OptBool, Flag: "--dangerously-skip-permissions"}) {
		t.Error("bool with the flag is legacy")
	}
	if IsLegacyBypassDef(OptionDef{Type: OptEnum, Flag: "--dangerously-skip-permissions", Choices: []string{"a"}}) {
		t.Error("an enum is not legacy")
	}
}

func TestOptionOfferedOnLegacyProfile(t *testing.T) {
	defs := legacyDefs()
	mode := defs[PermissionModeKey]
	plain := AgentConfig{Bin: "claude"}
	if OptionOffered(mode, plain, defs) {
		t.Error("permission_mode offered on a plain profile")
	}
	legacy := AgentConfig{Bin: "claude", Options: map[string]any{"skip": false}}
	if !OptionOffered(mode, legacy, defs) {
		t.Error("permission_mode not offered on a profile that sets the legacy bool")
	}
	if OptionOffered(defs["skip"], plain, defs) {
		t.Error("a bool is offered unset")
	}
	if !OptionOffered(defs["skip"], legacy, defs) {
		t.Error("a set key is offered")
	}
}

func TestProfileBypasses(t *testing.T) {
	defs := legacyDefs()
	for _, c := range []struct {
		name string
		a    AgentConfig
		want bool
	}{
		{"legacy bool", AgentConfig{Bin: "claude", Options: map[string]any{"skip": true}}, true},
		{"enum bypass", AgentConfig{Bin: "claude", Options: map[string]any{PermissionModeKey: BypassMode}}, true},
		{"args", AgentConfig{Bin: "claude", Args: []string{"--dangerously-skip-permissions"}}, true},
		{"enum plan with legacy true", AgentConfig{Bin: "claude", Options: map[string]any{"skip": true, PermissionModeKey: "plan"}}, false},
		{"nothing", AgentConfig{Bin: "claude"}, false},
	} {
		if got := ProfileBypasses(c.a, defs, c.a.Options); got != c.want {
			t.Errorf("%s: = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestProfileBypassesDoesNotMutateArgs(t *testing.T) {
	defs := legacyDefs()
	args := make([]string, 1, 10)
	args[0] = "--x"
	a := AgentConfig{Bin: "claude", Args: args}
	ProfileBypasses(a, defs, map[string]any{PermissionModeKey: BypassMode})
	if got := args[:2][1]; got != "" {
		t.Errorf("ProfileBypasses wrote into the caller's args backing array: %q", got)
	}
}

func TestValidateProfileCriticalRm(t *testing.T) {
	defs := legacyDefs()
	base := func(mode, runtime string) AgentConfig {
		return AgentConfig{Name: "p", Bin: "claude", Runtime: runtime,
			Options: map[string]any{CriticalRmKey: true, PermissionModeKey: mode}}
	}
	check := func(a AgentConfig, want string) {
		t.Helper()
		err := ValidateProfile(a, defs)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want containing %q", err, want)
		}
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Path != "options."+CriticalRmKey {
			t.Fatalf("field error = %v (%#v)", err, fe)
		}
	}
	check(base("plan", ""), "needs Permission mode: Bypass")
	if err := ValidateProfile(base(BypassMode, ""), defs); err != nil {
		t.Fatalf("bypass + critical-rm: %v", err)
	}
	check(base(BypassMode, RuntimeHost), "runs only in a box")
	// Off passes in any mode and on the host.
	off := AgentConfig{Name: "p", Bin: "claude", Runtime: RuntimeHost, Options: map[string]any{CriticalRmKey: false, PermissionModeKey: "plan"}}
	if err := ValidateProfile(off, defs); err != nil {
		t.Fatalf("off: %v", err)
	}
}

// The enum owns the older bypass bool only when the Permission mode def that applies to the bin can say Bypass.
func TestLegacyBypassKeyNeedsEnumThatCanBypass(t *testing.T) {
	noBypass := func() map[string]OptionDef {
		defs := legacyDefs()
		m := defs[PermissionModeKey]
		m.Choices = []string{"default", "acceptEdits", "plan"}
		defs[PermissionModeKey] = m
		return defs
	}
	a := AgentConfig{Bin: "claude", Options: map[string]any{"skip": true}}
	defs := noBypass()
	if k := LegacyBypassKey(defs, a); k != "" {
		t.Errorf("enum without bypassPermissions: key = %q, want none (the checkbox stays the bool's own)", k)
	}
	if OptionOffered(defs[PermissionModeKey], a, defs) {
		t.Error("permission_mode offered although the enum does not own the legacy bool")
	}
	if !OptionOffered(defs["skip"], a, defs) {
		t.Error("the legacy bool is offered as a normal set key")
	}
	set := AgentConfig{Bin: "claude", Options: map[string]any{"skip": true, PermissionModeKey: "plan"}}
	if !OptionOffered(defs[PermissionModeKey], set, defs) {
		t.Error("permission_mode set on the profile is offered")
	}
	// The mode still supersedes the bool when it is set (spec §2), owned or not.
	if got := OptionFlags(defs, SupersedeLegacyBypass(defs, set.Options)); !slices.Equal(got, []string{"--permission-mode", "plan"}) {
		t.Errorf("flags = %v", got)
	}
	// The enum that does not apply to the bin owns nothing either.
	other := legacyDefs()
	m := other[PermissionModeKey]
	m.Bins = []string{"codex"}
	other[PermissionModeKey] = m
	if k := LegacyBypassKey(other, a); k != "" {
		t.Errorf("enum for another bin: key = %q", k)
	}
	// No permission_mode def at all: no owner.
	bare := map[string]OptionDef{"skip": legacyDefs()["skip"]}
	if k := LegacyBypassKey(bare, a); k != "" {
		t.Errorf("no permission_mode def: key = %q", k)
	}
	// With bypassPermissions among the choices the enum owns it, as before.
	if k := LegacyBypassKey(legacyDefs(), a); k != "skip" {
		t.Errorf("key = %q, want skip", k)
	}
}

func TestValidateProfileCriticalRmReasonWithoutTier(t *testing.T) {
	// A config def that replaces the built-in without tier "box": the host refusal still says why.
	defs := legacyDefs()
	d := defs[CriticalRmKey]
	d.Tier = ""
	defs[CriticalRmKey] = d
	a := AgentConfig{Name: "x", Bin: "claude", Runtime: RuntimeHost,
		Options: map[string]any{CriticalRmKey: true, PermissionModeKey: BypassMode}}
	err := ValidateProfile(a, defs)
	want := `profile "x": "Skip the critical-rm prompt" runs only in a box`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want containing %q", err, want)
	}
	d.Label = ""
	defs[CriticalRmKey] = d
	err = ValidateProfile(a, defs)
	want = `profile "x": "` + CriticalRmKey + `" runs only in a box`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("no label: err = %v, want containing %q", err, want)
	}
}

func TestValidateRefusesBoxOnlyEnvWithoutBoxTier(t *testing.T) {
	cfg := Defaults()
	cfg.Options = []OptionDef{{Key: "x", Label: "X", Type: OptBool, Env: CriticalRmEnv}}
	err := cfg.Validate()
	want := `config: option "x" sets ` + CriticalRmEnv + `, which runs only in a box; add "tier": "box"`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	cfg.Options[0].Tier = RuntimeBox
	if err := cfg.Validate(); err != nil {
		t.Fatalf("with tier box: %v", err)
	}
	// The built-in def passes.
	if err := Defaults().Validate(); err != nil {
		t.Fatalf("defaults: %v", err)
	}
}
