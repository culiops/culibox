package box

import (
	"slices"
	"testing"
)

func TestValidateLimits(t *testing.T) {
	ok := []struct {
		pids int
		tmp  string
	}{{0, ""}, {512, "512m"}, {4096, "4g"}}
	for _, c := range ok {
		if err := ValidateLimits(c.pids, c.tmp); err != nil {
			t.Errorf("ValidateLimits(%d,%q) = %v, want ok", c.pids, c.tmp, err)
		}
	}
	bad := []struct {
		pids int
		tmp  string
	}{{63, ""}, {65537, ""}, {-1, ""}, {0, "512"}, {0, "0m"}, {0, "512m,suid"}, {0, "1t"}, {0, "1234567m"}, {0, " 512m"}}
	for _, c := range bad {
		if err := ValidateLimits(c.pids, c.tmp); err == nil {
			t.Errorf("ValidateLimits(%d,%q) = nil, want an error", c.pids, c.tmp)
		}
	}
}

func TestRunArgsUseConfiguredLimits(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	m.opts.PidsLimit, m.opts.TmpSize = 2048, "2g"
	got := m.runArgs("ab-box-abc", "/w", nil, false, false)
	if !containsPair(got, "--pids-limit", "2048") {
		t.Errorf("pids limit not applied: %q", got)
	}
	if !containsPair(got, "--tmpfs", "/tmp:rw,nosuid,nodev,exec,size=2g") {
		t.Errorf("tmp size not applied: %q", got)
	}

	m.opts.PidsLimit, m.opts.TmpSize = 0, ""
	got = m.runArgs("ab-box-abc", "/w", nil, false, false)
	if !containsPair(got, "--pids-limit", "512") || !containsPair(got, "--tmpfs", "/tmp:rw,nosuid,nodev,exec,size=512m") {
		t.Errorf("zero values must give the defaults: %q", got)
	}
}

// A deleted pinned tag must fail the spawn, not be fetched from Docker Hub.
func TestBoxRunsNeverPull(t *testing.T) {
	m := newTestManager(t)
	got := m.runArgs("ab-box-abc", "/w", nil, false, false)
	p := slices.Index(got, "--pull=never")
	if p < 0 {
		t.Fatalf("box run does not carry --pull=never: %q", got)
	}
	if img := slices.Index(got, m.opts.Image); img < 0 || p > img {
		t.Fatalf("--pull=never (%d) must come before the image (%d): %q", p, img, got)
	}
}

func TestNewRefusesBadLimits(t *testing.T) {
	_, err := New(Options{
		DockerBin: "/usr/bin/docker", Image: "culiops/agent-box:base", Instance: "TEST",
		StateRoot: t.TempDir(), Client: newFake(), TmpSize: "1g,suid",
	})
	if err == nil {
		t.Fatal("a tmp size that adds mount options must be refused")
	}
}
