package launcher

import (
	"reflect"
	"testing"

	"github.com/culiops/culibox/internal/config"
)

func awsConnList() []config.AWSConnection {
	return []config.AWSConnection{
		{Name: "sbx-b", Description: "b", AWSBinding: config.AWSBinding{SourceProfile: "sandbox", RoleARN: "arn:aws:iam::226198813800:role/b", Region: "us-east-1", Label: "B", S3Read: []string{"bkt-b"}}},
		{Name: "sbx-a", AWSBinding: config.AWSBinding{SourceProfile: "sandbox", RoleARN: "arn:aws:iam::226198813800:role/a", Region: "ap-southeast-1", Label: "A", EgressAllow: []string{"a.example.com"}}},
	}
}

func awsConnLauncher() *AgentLauncher {
	l := New([]config.AgentConfig{
		{Name: "c", Bin: "claude", Runtime: config.RuntimeBox, AWSConnections: []string{"sbx-b", "sbx-a"}},
		{Name: "plain", Bin: "claude"},
		{Name: "bad", Bin: "claude", Runtime: config.RuntimeBox, AWSConnections: []string{"gone"}},
		{Name: "both", Bin: "claude", Runtime: config.RuntimeBox, AWSConnections: []string{"sbx-a"},
			AWS: &config.AWSBinding{SourceProfile: "p", RoleARN: "arn:aws:iam::226198813800:role/x", Region: "us-east-1"}},
		{Name: "hosttier", Bin: "claude", AWSConnections: []string{"sbx-a"}},
	}, fakeResolver{}, nil)
	l.SetAWSConnections(awsConnList())
	return l
}

func TestAWSConnectionsForUnknownRefuses(t *testing.T) {
	l := awsConnLauncher()
	got, err := l.AWSConnectionsFor("bad")
	if got != nil || err == nil || err.Error() != `profile "bad" names AWS connection "gone", which is not defined in aws_connections` {
		t.Fatalf("got %+v, %v", got, err)
	}
	// The spawn-time profile-binding rule is re-run: inline aws plus connections,
	// and a host-tier profile, are refused rather than spawned.
	if _, err := l.AWSConnectionsFor("both"); err == nil {
		t.Fatal("inline aws + aws_connections spawned")
	}
	if _, err := l.AWSConnectionsFor("hosttier"); err == nil {
		t.Fatal("aws_connections without runtime: box spawned")
	}
	if got, err := l.AWSConnectionsFor("plain"); got != nil || err != nil {
		t.Fatalf("profile without connections = %v, %v", got, err)
	}
	if got, err := l.AWSConnectionsFor("missing"); got != nil || err != nil {
		t.Fatalf("unknown profile = %v, %v", got, err)
	}
}

func TestAWSConnectionsForClones(t *testing.T) {
	l := awsConnLauncher()
	got, err := l.AWSConnectionsFor("c")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "sbx-a" || got[1].Name != "sbx-b" {
		t.Fatalf("got %+v, want sbx-a then sbx-b (sorted by name)", got)
	}
	got[0].Name = "mutated"
	got[0].EgressAllow[0] = "mutated"
	got[1].S3Read[0] = "mutated"
	again, _ := l.AWSConnectionsFor("c")
	want := awsConnList()
	if !reflect.DeepEqual(again[0], want[1]) || !reflect.DeepEqual(again[1], want[0]) {
		t.Fatalf("AWSConnectionsFor must return deep copies, got %+v", again)
	}
	// SetAWSConnections clones too.
	src := awsConnList()
	l.SetAWSConnections(src)
	src[0].S3Read[0] = "mutated"
	if g, _ := l.AWSConnectionsFor("c"); g[1].S3Read[0] != "bkt-b" {
		t.Fatal("SetAWSConnections kept the caller's backing arrays")
	}
}
