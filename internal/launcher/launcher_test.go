package launcher

import (
	"testing"

	"github.com/culiops/culiops-agent-console/internal/config"
)

func TestBuild_KnownAgent(t *testing.T) {
	l := New([]config.AgentConfig{{Name: "claude", Bin: "/usr/bin/claude", Args: []string{"--resume"}}})
	argv, env, err := l.Build(SpawnSpec{Agent: "claude", Cwd: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) != 2 || argv[0] != "/usr/bin/claude" || argv[1] != "--resume" {
		t.Fatalf("argv wrong: %v", argv)
	}
	if env != nil {
		t.Fatalf("env should be nil in M1: %v", env)
	}
}

func TestBuild_UnknownAgent(t *testing.T) {
	l := New(nil)
	if _, _, err := l.Build(SpawnSpec{Agent: "nope"}); err == nil {
		t.Fatal("unknown agent should error")
	}
}
