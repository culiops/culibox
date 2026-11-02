package web

import (
	"encoding/json"
	"testing"

	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/session"
)

func TestAWSStatusGitHubOnlySession(t *testing.T) {
	info := map[string]session.Info{
		"claude-7": {ID: "claude-7", GitHub: &session.GitHubInfo{Owners: []string{"acme", "other"}, Repos: 3}},
		"claude-8": {ID: "claude-8", AWS: &session.AWSInfo{Label: "sandbox"}},
	}
	rows := []broker.Row{
		{SessionID: "claude-7", Provider: "github", Subject: "acme", Installation: 42, Repos: 2, Outcome: broker.OutcomeMinted},
	}
	ad := awsAdapter(info, rows, nil, nil)

	st, ok := ad.AWSStatus("claude-7")
	if !ok {
		t.Fatal("a GitHub-only session reported no status")
	}
	raw, _ := json.Marshal(st)
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	gh, _ := got["github"].(map[string]any)
	if owners, _ := gh["owners"].([]any); len(owners) != 2 || owners[0] != "acme" || gh["repos"] != float64(3) {
		t.Fatalf("github %v", got["github"])
	}
	// AWS-only keys belong to AWS-bound sessions.
	for _, k := range []string{"label", "account_id", "role_arn", "prod"} {
		if _, has := got[k]; has {
			t.Errorf("a GitHub-only status carries the AWS key %q: %s", k, raw)
		}
	}
	recent, _ := got["recent"].([]any)
	if len(recent) != 1 {
		t.Fatalf("recent %v", got["recent"])
	}
	r0, _ := recent[0].(map[string]any)
	if r0["provider"] != "github" || r0["subject"] != "acme" || r0["repos"] != float64(2) {
		t.Fatalf("recent row %v", r0)
	}

	// An AWS-only payload is unchanged: no github key.
	st, _ = ad.AWSStatus("claude-8")
	raw, _ = json.Marshal(st)
	var aws map[string]any
	if err := json.Unmarshal(raw, &aws); err != nil {
		t.Fatal(err)
	}
	if _, has := aws["github"]; has {
		t.Fatalf("an AWS-only status carries a github key: %s", raw)
	}
}
