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
		{"aws", AgentConfig{AWS: aws, Runtime: "box"}, "this profile has an AWS binding, so it runs only in a box"},
		{"aws connections", AgentConfig{AWSConnections: []string{"prod"}}, "this profile has an AWS binding, so it runs only in a box"},
		{"aws and github", AgentConfig{AWS: aws, GitHub: gh, Runtime: "box"}, "this profile has AWS and GitHub bindings, so it runs only in a box"},
		{"github", AgentConfig{GitHub: gh}, "this profile has a GitHub binding, so it runs only in a box"},
		{"token", AgentConfig{Tokens: []string{"x"}, Runtime: "box"}, "this profile has a token binding, so it runs only in a box"},
		{"all three", AgentConfig{AWS: aws, GitHub: gh, Tokens: []string{"x"}}, "this profile has AWS, GitHub and token bindings, so it runs only in a box"},
		{"context paths", AgentConfig{ContextPaths: []string{"/srv/docs"}, Runtime: "box"}, "this profile has context paths, which only a box can mount"},
		{"box only", AgentConfig{BoxOnly: true}, "this profile is set to box only"},
		{"bindings win over box only", AgentConfig{AWS: aws, BoxOnly: true}, "this profile has an AWS binding, so it runs only in a box"},
		{"bindings win over context paths", AgentConfig{Tokens: []string{"x"}, ContextPaths: []string{"/srv/docs"}}, "this profile has a token binding, so it runs only in a box"},
		{"context paths win over box only", AgentConfig{ContextPaths: []string{"/srv/docs"}, BoxOnly: true}, "this profile has context paths, which only a box can mount"},
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
