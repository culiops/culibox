package config

import (
	"errors"
	"strings"
	"testing"
)

func TestHostBlock(t *testing.T) {
	aws := &AWSBinding{SourceProfile: "p", RoleARN: "arn:aws:iam::123456789012:role/r", Region: "us-east-1"}
	gh := &GitHubBinding{}
	cases := []struct {
		name string
		a    AgentConfig
		want string
	}{
		{"box only", AgentConfig{BoxOnly: true}, "this profile is set to box only"},
		{"box only and bound", AgentConfig{AWS: aws, GitHub: gh, BoxOnly: true}, "this profile is set to box only"},
		{"aws", AgentConfig{AWS: aws, Runtime: "box"}, ""},
		{"aws connections", AgentConfig{AWSConnections: []string{"prod"}}, ""},
		{"github", AgentConfig{GitHub: gh}, ""},
		{"token", AgentConfig{Tokens: []string{"x"}, Runtime: "box"}, ""},
		{"context paths", AgentConfig{ContextPaths: []string{"/srv/docs"}, Runtime: "box"}, ""},
		{"box-only env", AgentConfig{Env: map[string]string{CriticalRmEnv: "1"}}, ""},
		{"box-only option", AgentConfig{Options: map[string]any{CriticalRmKey: true}}, ""},
		{"free profile", AgentConfig{}, ""},
		{"host profile", AgentConfig{Runtime: "host"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := HostBlock(c.a); got != c.want {
				t.Fatalf("HostBlock = %q, want %q", got, c.want)
			}
		})
	}
}

func TestHostDrops(t *testing.T) {
	aws := &AWSBinding{SourceProfile: "p", RoleARN: "arn:aws:iam::123456789012:role/r", Region: "us-east-1"}
	gh := &GitHubBinding{}
	defs := legacyDefs()
	envItem := CriticalRmEnv + " setting"
	optItem := `"Skip the critical-rm prompt" option`
	cases := []struct {
		name string
		a    AgentConfig
		want []string
	}{
		{"unbound", AgentConfig{}, nil},
		{"host profile", AgentConfig{Runtime: "host"}, nil},
		{"aws", AgentConfig{AWS: aws}, []string{"AWS binding"}},
		{"aws connections", AgentConfig{AWSConnections: []string{"prod"}}, []string{"AWS binding"}},
		{"aws and github", AgentConfig{AWS: aws, GitHub: gh}, []string{"AWS binding", "GitHub binding"}},
		{"token", AgentConfig{Tokens: []string{"x"}}, []string{"token binding"}},
		{"context paths", AgentConfig{ContextPaths: []string{"/srv/docs"}}, []string{"context paths"}},
		{"env", AgentConfig{Env: map[string]string{CriticalRmEnv: "1"}}, []string{envItem}},
		{"secret", AgentConfig{Secrets: []string{CriticalRmEnv}}, []string{envItem}},
		{"option on", AgentConfig{Options: map[string]any{CriticalRmKey: true}}, []string{optItem}},
		{"option off", AgentConfig{Options: map[string]any{CriticalRmKey: false}}, nil},
		{"all", AgentConfig{
			AWS: aws, GitHub: gh, Tokens: []string{"x"}, ContextPaths: []string{"/srv/docs"},
			Env: map[string]string{CriticalRmEnv: "1"}, Options: map[string]any{CriticalRmKey: true},
		}, []string{"AWS binding", "GitHub binding", "token binding", "context paths", envItem, optItem}},
		{"box only alone adds nothing", AgentConfig{BoxOnly: true}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := HostDrops(c.a, defs)
			if strings.Join(got, "|") != strings.Join(c.want, "|") || (got == nil) != (c.want == nil) {
				t.Fatalf("HostDrops = %q, want %q", got, c.want)
			}
		})
	}
	t.Run("option without a label uses its key", func(t *testing.T) {
		d := map[string]OptionDef{"nolabel": {Key: "nolabel", Type: OptBool, Tier: RuntimeBox}}
		got := HostDrops(AgentConfig{Options: map[string]any{"nolabel": true}}, d)
		if len(got) != 1 || got[0] != `"nolabel" option` {
			t.Fatalf("HostDrops = %q", got)
		}
	})
}

func TestJoinKinds(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"AWS"}, "AWS"},
		{[]string{"AWS", "GitHub"}, "AWS and GitHub"},
		{[]string{"AWS", "GitHub", "token"}, "AWS, GitHub and token"},
	}
	for _, c := range cases {
		if got := JoinKinds(c.in); got != c.want {
			t.Errorf("JoinKinds(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBindingKinds(t *testing.T) {
	if got := (AgentConfig{}).BindingKinds(); len(got) != 0 {
		t.Fatalf("BindingKinds of a free profile = %v, want none", got)
	}
	a := AgentConfig{Tokens: []string{"x"}, GitHub: &GitHubBinding{}, AWSConnections: []string{"c"}}
	if got := strings.Join(a.BindingKinds(), ","); got != "AWS,GitHub,token" {
		t.Fatalf("BindingKinds = %q, want AWS,GitHub,token", got)
	}
}

func TestBindingPhrase(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, "an AWS binding"},
		{[]string{"AWS"}, "an AWS binding"},
		{[]string{"GitHub"}, "a GitHub binding"},
		{[]string{"token"}, "a token binding"},
		{[]string{"AWS", "GitHub"}, "AWS and GitHub bindings"},
		{[]string{"AWS", "GitHub", "token"}, "AWS, GitHub and token bindings"},
	}
	for _, c := range cases {
		if got := BindingPhrase(c.in); got != c.want {
			t.Errorf("BindingPhrase(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestValidateRefusesBoxOnlyHost(t *testing.T) {
	a := AgentConfig{Name: "p", Bin: "b", Runtime: "host", BoxOnly: true}
	if err := (Config{BindAddr: "127.0.0.1:1", Agents: []AgentConfig{a}}).Validate(); err == nil {
		t.Fatal("Validate accepted box_only with runtime host")
	}
	err := ValidateProfile(a, map[string]OptionDef{})
	if err == nil {
		t.Fatal("ValidateProfile accepted box_only with runtime host")
	}
	var fe *FieldError
	if !errors.As(err, &fe) || fe.Path != "box_only" {
		t.Fatalf("error = %#v, want a FieldError at box_only", err)
	}
	if !strings.Contains(err.Error(), `box_only cannot be combined with runtime "host"`) {
		t.Fatalf("error = %q, want the box_only/host sentence", err)
	}

	ok := AgentConfig{Name: "p", Bin: "b", BoxOnly: true}
	if err := (Config{BindAddr: "127.0.0.1:1", Agents: []AgentConfig{ok}}).Validate(); err != nil {
		t.Fatalf("Validate refused box_only with an empty runtime: %v", err)
	}
	if err := ValidateProfile(ok, map[string]OptionDef{}); err != nil {
		t.Fatalf("ValidateProfile refused box_only with an empty runtime: %v", err)
	}
}

// Host review 2026-11-23 (#346, #359): a host run still passes the profile's own
// secrets and env, so the Host card and confirm must be able to name them.
func TestHostKeeps(t *testing.T) {
	cases := []struct {
		name string
		a    AgentConfig
		want []string
	}{
		{"none", AgentConfig{}, nil},
		{"secrets in order", AgentConfig{Secrets: []string{"GH_TOKEN", "A_KEY"}}, []string{"GH_TOKEN secret", "A_KEY secret"}},
		{"env sorted", AgentConfig{Env: map[string]string{"ZED": "1", "ALPHA": "2"}}, []string{"ALPHA env", "ZED env"}},
		{"secret wins over env", AgentConfig{Secrets: []string{"GH_TOKEN"}, Env: map[string]string{"GH_TOKEN": "x", "B": "y"}}, []string{"GH_TOKEN secret", "B env"}},
		{"box-only names are dropped, not kept", AgentConfig{Secrets: []string{CriticalRmEnv}, Env: map[string]string{CriticalRmEnv: "1"}}, nil},
		{"duplicate secret once", AgentConfig{Secrets: []string{"X", "X"}}, []string{"X secret"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := HostKeeps(c.a)
			if strings.Join(got, "|") != strings.Join(c.want, "|") || (got == nil) != (c.want == nil) {
				t.Fatalf("HostKeeps = %q, want %q", got, c.want)
			}
		})
	}
}
