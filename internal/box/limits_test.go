package box

import (
	"slices"
	"strings"
	"testing"
)

func TestValidateLimits(t *testing.T) {
	ok := []struct {
		pids     int
		tmp, mem string
	}{{0, "", ""}, {512, "512m", "1g"}, {4096, "4g", "2048m"}, {0, "", "999999g"}}
	for _, c := range ok {
		if err := ValidateLimits(c.pids, c.tmp, c.mem); err != nil {
			t.Errorf("ValidateLimits(%d,%q,%q) = %v, want ok", c.pids, c.tmp, c.mem, err)
		}
	}
	bad := []struct {
		pids     int
		tmp, mem string
	}{{63, "", ""}, {65537, "", ""}, {-1, "", ""}, {0, "512", ""}, {0, "0m", ""}, {0, "512m,suid", ""}, {0, "1t", ""}, {0, "1234567m", ""}, {0, " 512m", ""},
		{0, "", "512m"}, {0, "", "1023m"}, {0, "", "999999k"}, {0, "", "1048576k"}, {0, "", "1.5g"}, {0, "", "3G"}, {0, "", "2048"}, {0, "", "0g"},
		{0, "", "3g,suid"}, {0, "", " 3g"}, {0, "", "1t"}, {0, "", "1234567m"}}
	for _, c := range bad {
		if err := ValidateLimits(c.pids, c.tmp, c.mem); err == nil {
			t.Errorf("ValidateLimits(%d,%q,%q) = nil, want an error", c.pids, c.tmp, c.mem)
		}
	}
	if err := ValidateLimits(0, "", "512m"); err == nil || !strings.Contains(err.Error(), "box_memory") {
		t.Errorf("a memory error must name box_memory, got %v", err)
	}
}

func TestDefaultMemoryIsValid(t *testing.T) {
	if err := ValidateLimits(0, "", DefaultMemory); err != nil {
		t.Fatalf("DefaultMemory %q must pass its own validation: %v", DefaultMemory, err)
	}
}

func TestRunArgsUseConfiguredLimits(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	m.opts.PidsLimit, m.opts.TmpSize, m.opts.Memory = 2048, "2g", "3g"
	got := m.runArgs("ab-box-abc", "/w", nil, false, false)
	if !containsPair(got, "--pids-limit", "2048") {
		t.Errorf("pids limit not applied: %q", got)
	}
	if !containsPair(got, "--tmpfs", "/tmp:rw,nosuid,nodev,exec,size=2g") {
		t.Errorf("tmp size not applied: %q", got)
	}
	if !containsPair(got, "--memory", "3g") || !containsPair(got, "--memory-swap", "3g") {
		t.Errorf("memory cap (swap equal to it) not applied: %q", got)
	}

	m.opts.PidsLimit, m.opts.TmpSize, m.opts.Memory = 0, "", ""
	got = m.runArgs("ab-box-abc", "/w", nil, false, false)
	if !containsPair(got, "--pids-limit", "512") || !containsPair(got, "--tmpfs", "/tmp:rw,nosuid,nodev,exec,size=512m") {
		t.Errorf("zero values must give the defaults: %q", got)
	}
	if !containsPair(got, "--memory", DefaultMemory) || !containsPair(got, "--memory-swap", DefaultMemory) {
		t.Errorf("an empty memory must give DefaultMemory for both flags: %q", got)
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
	_, err = New(Options{
		DockerBin: "/usr/bin/docker", Image: "culiops/agent-box:base", Instance: "TEST",
		StateRoot: t.TempDir(), Client: newFake(), Memory: "512m",
	})
	if err == nil {
		t.Fatal("a memory cap below 1g must be refused")
	}
}
